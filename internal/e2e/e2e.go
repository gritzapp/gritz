// Package e2e stands up gritz for one test and drives it as its users do (see
// proposals/draft/e2e-harness.md).
package e2e

import (
	"cmp"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/server/atlassianserver"
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
	// Runner stands up h.Backend, h.Runner and h.Agent for the default org;
	// without it they are nil and the test may act as the runner with
	// Org.SubmitRunnerEvents.
	Runner *RunnerOptions
}

// Harness is gritz for one test.
type Harness struct {
	// Server is the API server.
	Server *Server
	// Org is the default org, for tests that need only one.
	Org *Org
	// Backend is the local backend; nil unless Options.Runner.
	Backend *Backend
	// Runner is the runner serving Org; nil unless Options.Runner.
	Runner *Runner
	// Agent is the puppet agent's runs; nil unless Options.Runner.
	Agent *Agent
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
	// The server needs its own URL (the Atlassian webhook URL is built from
	// it); an unstarted test server has already bound its listener.
	ts := httptest.NewUnstartedServer(nil)
	t.Cleanup(ts.Close)
	baseURL := "http://" + ts.Listener.Addr().String()
	srv := server.New(server.Options{
		Store:     st,
		Auth:      auth,
		Publisher: ps,
		Atlassian: atlassianserver.New(atlassianserver.Options{
			Store:     st,
			BaseURL:   baseURL,
			Publisher: ps,
		}),
		Notify: notifyserver.New(notifyserver.Options{
			Subscriber:  ps,
			OrgResolver: resolver,
		}),
		AppKey:      appKey,
		OrgResolver: resolver,
		Wrap:        s.route,
	})
	ts.Config.Handler = srv.Handler()
	ts.Start()
	s.URL = ts.URL
	h := &Harness{Server: s, Org: s.createOrg(opts.Runner == nil)}
	// Cleanups run in reverse: remove the mocks; fail the runs left open; stop
	// the runner; destroy the sandboxes, killing their drivers, and remove the
	// root; close the agent's endpoint and the server.
	if opts.Runner != nil {
		log := newLog(t)
		h.Agent = newAgent(t, h.Org)
		h.Backend = newBackend(t, s.timeout, log)
		h.Runner = newRunner(t, h.Org, h.Backend, h.Agent, opts.Runner, log)
		t.Cleanup(h.Agent.server.Close)
	}
	t.Cleanup(func() {
		s.Mock(nil)
		if h.Backend != nil {
			h.Backend.unmock()
		}
	})
	return h
}

// newLog returns a logger writing to t's output. A local backend's driver
// reapers may still log after the sandboxes are destroyed, so it drops what is
// written once the cleanup registered here has run, after the runner's and the
// backend's.
func newLog(t *testing.T) *slog.Logger {
	w := &testWriter{w: t.Output()}
	t.Cleanup(w.close)
	return slog.New(slog.NewTextHandler(w, nil))
}

type testWriter struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(p), nil
	}
	return w.w.Write(p)
}

func (w *testWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
}
