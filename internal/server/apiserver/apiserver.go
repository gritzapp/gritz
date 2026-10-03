package apiserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/icholy/gritz/internal/server/atlassianserver"
	"github.com/icholy/gritz/internal/server/eventrouter"
	"github.com/icholy/gritz/internal/server/pubsub"
	"github.com/icholy/gritz/internal/store"
	"github.com/icholy/gritz/internal/version"
)

//go:generate go tool moq -out github_moq_test.go . GithubServer
//go:generate go tool moq -out shell_moq_test.go . ShellRegistry

// GithubServer is the subset of *githubserver.Server the apiserver depends on.
// It is an interface so LinkGitHubInstallation's membership check can be mocked
// in tests without a real GitHub App.
type GithubServer interface {
	AppInstallURL() string
	VerifyInstallationAccess(ctx context.Context, installationID int64, user *model.User) error
}

type Server struct {
	gritzv1connect.UnimplementedGritzServiceHandler
	log       *slog.Logger
	store     *store.Store
	baseURL   string
	publisher pubsub.Publisher
	atlassian *atlassianserver.Server
	github    GithubServer
	// appKey signs the app JWTs minted by CreateTaskToken; it is the same key the
	// auth layer uses for every other app JWT, so the minted token verifies on the
	// normal VerifyAppToken path.
	appKey ed25519.PrivateKey
	// shells registers debug-shell rendezvous sessions for OpenShell. May be nil
	// in tests that don't exercise OpenShell.
	shells ShellRegistry
	// router runs the shared read-only matcher (Router.Plan) for TestEvent's
	// dry-run path. May be nil in tests that don't exercise TestEvent; the
	// handler errors if it's called without one.
	router *eventrouter.Router
}

// ShellRegistry registers a debug-shell rendezvous session so the driver and
// operator legs can meet on the relay. Backed by *shellserver.Registry in
// production; an interface here keeps apiserver testable without the relay.
type ShellRegistry interface {
	Seed(id string, orgID, taskID int64) error
}

type Options struct {
	Log       *slog.Logger
	Store     *store.Store
	BaseURL   string
	Publisher pubsub.Publisher
	Atlassian *atlassianserver.Server
	GitHub    GithubServer
	AppKey    ed25519.PrivateKey
	Shells    ShellRegistry
	Router    *eventrouter.Router
}

func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:       log,
		store:     opts.Store,
		baseURL:   opts.BaseURL,
		publisher: opts.Publisher,
		atlassian: opts.Atlassian,
		github:    opts.GitHub,
		appKey:    opts.AppKey,
		shells:    opts.Shells,
		router:    opts.Router,
	}
}

func (s *Server) publish(n model.Notification) {
	if n.Ignore {
		return
	}
	if s.publisher == nil {
		return
	}
	if err := s.publisher.Publish(context.Background(), n); err != nil {
		s.log.Warn("failed to publish notification", "err", err, "type", n.Type, "resources", n.Resources)
	}
}

func (s *Server) Ping(ctx context.Context, req *gritzv1.PingRequest) (*gritzv1.PingResponse, error) {
	return &gritzv1.PingResponse{
		Version: version.String(),
	}, nil
}

func (s *Server) GetProfile(ctx context.Context, req *gritzv1.GetProfileRequest) (*gritzv1.GetProfileResponse, error) {
	u := apiauth.Caller(ctx)
	if u == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	orgs, err := s.store.ListOrgsByMember(ctx, nil, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	user, err := s.store.GetUser(ctx, nil, u.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GetProfileResponse{
		Profile: &gritzv1.Profile{
			Id:    u.ID,
			Email: u.Email,
			Name:  u.Name,
		},
		DefaultOrgId:     user.DefaultOrgID,
		GithubAccount:    user.GitHubAccountProto(),
		AtlassianAccount: user.AtlassianAccountProto(),
		Orgs:             model.ProtoMap(orgs),
	}, nil
}
