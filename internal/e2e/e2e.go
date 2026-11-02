// Package e2e stands up gritz for one test and drives it as its users do (see
// proposals/draft/e2e-harness.md).
package e2e

import (
	"cmp"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/server/notifyserver"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
)

// The runner and workspace every harness task is created on.
const (
	runnerID  = "e2e"
	workspace = "e2e"
)

// Options configures a Harness; zero values take defaults.
type Options struct {
	// Timeout bounds every wait; default 30s.
	Timeout time.Duration
}

// Harness is gritz for one test.
type Harness struct {
	Server *Server
}

// New stands up a harness and registers its teardown with t.Cleanup.
func New(t *testing.T, opts *Options) *Harness {
	t.Helper()
	if opts == nil {
		opts = &Options{}
	}
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)
	userKey := createKey(t, st, org, "e2e-user")
	runnerKey := createKey(t, st, org, "e2e-runner")

	appKey, err := apiauth.CreateAppPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	resolver := server.NewStoreUserResolver(st)
	auth, err := apiauth.New(t.Context(), apiauth.Config{
		KeyValidator: server.NewStoreKeyValidator(st),
		UserResolver: resolver,
		AppKey:       appKey,
		// A DevUser skips the OIDC discovery a real login needs. Like the
		// --no-auth dev user it holds no org and no scopes, so a request without
		// a token is authenticated but allowed nothing.
		DevUser: &apiauth.UserInfo{ID: org.UserID},
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := pubsub.NewLocalPubSub()
	s := &Server{t: t, timeout: cmp.Or(opts.Timeout, 30*time.Second)}
	srv := server.New(server.Options{
		Store:     st,
		Auth:      auth,
		Publisher: ps,
		Notify: notifyserver.New(notifyserver.Options{
			Subscriber:  ps,
			OrgResolver: resolver,
		}),
		AppKey:      appKey,
		OrgResolver: resolver,
		Wrap:        s.route,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// Registered after ts.Close, so it runs first.
	t.Cleanup(func() { s.Mock(nil) })
	s.URL = ts.URL
	s.Client = gritzclient.New(gritzclient.Options{BaseURL: ts.URL, Token: userKey})

	// The runner registers its workspaces on startup; until the harness has a
	// runner, it registers the one CreateTask uses with the runner's key.
	_, err = gritzclient.New(gritzclient.Options{BaseURL: ts.URL, Token: runnerKey}).
		RegisterWorkspaces(t.Context(), &gritzv1.RegisterWorkspacesRequest{
			RunnerId:   runnerID,
			Workspaces: []*gritzv1.RegisteredWorkspace{{Name: workspace}},
		})
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{Server: s}
}

// createKey creates an admin xat_ key in org, as the --no-auth bootstrap does,
// and returns its raw token.
func createKey(t *testing.T, st *store.Store, org *teststore.Org, name string) string {
	t.Helper()
	token, err := apiauth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateKey(t.Context(), nil, &model.Key{
		ID:        uuid.NewString(),
		Name:      name,
		TokenHash: apiauth.HashKey(token),
		OrgID:     org.OrgID,
		Scopes:    authscope.Admin(),
	}); err != nil {
		t.Fatal(err)
	}
	return token
}
