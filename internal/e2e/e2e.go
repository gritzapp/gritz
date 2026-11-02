// Package e2e stands up gritz for one test and drives it as its users do (see
// proposals/draft/e2e-harness.md).
package e2e

import (
	"cmp"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/server/notifyserver"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store/teststore"
)

// The runner and workspace CreateTask uses where its request leaves them unset.
const (
	DefaultRunner    = "e2e"
	DefaultWorkspace = "e2e"
)

// Options configures a Harness; zero values take defaults.
type Options struct {
	// Timeout bounds every wait; default 30s.
	Timeout time.Duration
}

// Harness is gritz for one test.
type Harness struct {
	// Server is the API server.
	Server *Server
	// Org is the default org, for tests that need only one.
	Org *Org
}

// New stands up a harness and registers its teardown with t.Cleanup.
func New(t *testing.T, opts *Options) *Harness {
	t.Helper()
	if opts == nil {
		opts = &Options{}
	}
	st := teststore.New(t)
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
		DevUser: &apiauth.UserInfo{ID: "e2e-dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := pubsub.NewLocalPubSub()
	s := &Server{
		t:       t,
		Store:   st,
		appKey:  appKey,
		timeout: cmp.Or(opts.Timeout, 30*time.Second),
	}
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
	return &Harness{Server: s, Org: s.CreateOrg()}
}
