package e2e

import (
	"crypto/ed25519"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
)

// The handler and client interfaces share their methods, so a ClientMock can
// stand in for the server's RPC implementation.
var _ gritzv1connect.GritzServiceHandler = (*gritzclient.ClientMock)(nil)

// Server is the harness's API server.
type Server struct {
	// URL is the server's base URL.
	URL string

	t       *testing.T
	store   *store.Store
	appKey  ed25519.PrivateKey
	timeout time.Duration
	mock    atomic.Pointer[gritzclient.ClientMock]
}

// Client returns a client that calls the server with token.
func (s *Server) Client(token string) gritzclient.Client {
	return gritzclient.New(gritzclient.Options{BaseURL: s.URL, Token: token})
}

// CreateOrg creates an org with its own user, and registers the harness's
// workspace in it.
func (s *Server) CreateOrg() *Org {
	s.t.Helper()
	org := teststore.CreateOrg(s.t, s.store, nil)
	user, err := s.store.GetUser(s.t.Context(), nil, org.UserID)
	if err != nil {
		s.t.Fatal(err)
	}
	owner := &Member{
		UserID: user.ID,
		Email:  user.Email,
		Client: s.Client(s.userToken(user.ID, org.OrgID)),
	}
	o := &Org{
		ID:     org.OrgID,
		Owner:  owner,
		Client: owner.Client,
		server: s,
	}
	// The runner registers its workspaces on startup; until the harness has a
	// runner, it registers the one CreateTask uses with a runner key.
	_, runnerKey := s.createKey(o.ID, "e2e-runner", authscope.Admin())
	o.runner = s.Client(runnerKey)
	_, err = o.runner.
		RegisterWorkspaces(s.t.Context(), &gritzv1.RegisterWorkspacesRequest{
			RunnerId:   DefaultRunner,
			Workspaces: []*gritzv1.RegisteredWorkspace{{Name: DefaultWorkspace}},
		})
	if err != nil {
		s.t.Fatalf("RegisterWorkspaces: %v", err)
	}
	return o
}

// Mock installs m, replacing any previous mock: calls go to m's non-nil
// <Method>Func fields, and every method left nil goes to the real server.
// Mock(nil) removes it.
func (s *Server) Mock(m *gritzclient.ClientMock) {
	s.mock.Store(m)
}

// userToken returns the app JWT /auth/token issues the web UI for userID
// acting in orgID. A key would not do: keys carry no user.
func (s *Server) userToken(userID string, orgID int64) string {
	s.t.Helper()
	token, err := apiauth.SignAppToken(s.appKey, apiauth.NewAppClaims(&apiauth.UserInfo{ID: userID, OrgID: orgID}))
	if err != nil {
		s.t.Fatal(err)
	}
	return token
}

// createKey creates an xat_ key in orgID, as the --no-auth bootstrap does, and
// returns its id and raw token.
func (s *Server) createKey(orgID int64, name string, scopes authscope.Scopes) (string, string) {
	s.t.Helper()
	token, err := apiauth.GenerateKey()
	if err != nil {
		s.t.Fatal(err)
	}
	id := uuid.NewString()
	if err := s.store.CreateKey(s.t.Context(), nil, &model.Key{
		ID:        id,
		Name:      name,
		TokenHash: apiauth.HashKey(token),
		OrgID:     orgID,
		Scopes:    scopes,
	}); err != nil {
		s.t.Fatal(err)
	}
	return id, token
}

// route wraps the server's RPC implementation: each call goes to the installed
// mock's method when its <Method>Func is set, and to real otherwise. It sits
// behind the auth and observability interceptors. It isn't an interceptor
// itself because an interceptor would have to wrap the mock's message in a
// *connect.Response[Res], and Res is only known by reflection.
func (s *Server) route(real gritzv1connect.GritzServiceHandler) gritzv1connect.GritzServiceHandler {
	router := &gritzclient.ClientMock{}
	fields := reflect.ValueOf(router).Elem()
	methods := reflect.TypeFor[gritzv1connect.GritzServiceHandler]()
	for i := range methods.NumMethod() {
		name := methods.Method(i).Name
		field := fields.FieldByName(name + "Func")
		if !field.IsValid() {
			panic("e2e: ClientMock has no " + name + "Func; regenerate client_moq.go")
		}
		realMethod := reflect.ValueOf(real).MethodByName(name)
		field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
			if m := s.mock.Load(); m != nil && !reflect.ValueOf(m).Elem().FieldByName(name+"Func").IsNil() {
				return reflect.ValueOf(m).MethodByName(name).Call(args)
			}
			return realMethod.Call(args)
		}))
	}
	return router
}
