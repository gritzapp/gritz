package shellserver_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gritzapp/gritz/internal/auth/agentauth"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/server/shellserver"
	"github.com/gritzapp/gritz/internal/shell/shellwire"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// closeRecorder captures the args of every onClose invocation. The callback runs
// on the session's teardown goroutine, so access is mutex-guarded.
type closeRecorder struct {
	mu    sync.Mutex
	calls [][2]any // {session string, orgID int64}
}

func (c *closeRecorder) fn() func(string, int64) {
	return func(session string, orgID int64) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls = append(c.calls, [2]any{session, orgID})
	}
}

func (c *closeRecorder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// testOrg owns seeded sessions in these tests, and testTask is the task whose
// sandbox serves them — the driver leg is bound to this task.
const (
	testOrg  int64 = 1
	testTask int64 = 7
)

// send writes a binary message and fails the test on error.
func send(t *testing.T, conn *websocket.Conn, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	assert.NilError(t, conn.Write(ctx, websocket.MessageBinary, data))
}

// recv reads one message and fails the test on error.
func recv(t *testing.T, conn *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	assert.NilError(t, err)
	return typ, data
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	assert.Assert(t, cond(), "condition not met within %s", timeout)
}

func TestRelayPassesBytesBothDirections(t *testing.T) {
	t.Parallel()
	// The attach leg is wrapped with a test caller in testOrg so the inline org
	// check admits it, standing in for the Bearer auth middleware in production.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: testOrg}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	driver, _, err := websocket.Dial(ctx, base+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })
	attach, resp, err := websocket.Dial(ctx, base+"/shell/attach?session=s1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.NilError(t, err)
	assert.Equal(t, resp.StatusCode, http.StatusSwitchingProtocols)
	assert.Equal(t, attach.Subprotocol(), shellwire.Subprotocol)
	t.Cleanup(func() { attach.Close(websocket.StatusNormalClosure, "test done") })

	// driver -> attach, including arbitrary non-UTF-8 bytes.
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 0x80, 'h', 'i', 0x00}
	send(t, driver, payload)
	typ, got := recv(t, attach)
	assert.Equal(t, typ, websocket.MessageBinary)
	assert.DeepEqual(t, got, payload)

	// attach -> driver.
	reply := []byte{0x02, 0xde, 0xad, 0xbe, 0xef}
	send(t, attach, reply)
	typ, got = recv(t, driver)
	assert.Equal(t, typ, websocket.MessageBinary)
	assert.DeepEqual(t, got, reply)
}

func TestAttachRejectsVersionMismatch(t *testing.T) {
	t.Parallel()
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: testOrg}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	// The subprotocol is negotiated by websocket.Accept, so an unsupported version
	// completes the upgrade (no matching subprotocol selected) and is then closed
	// by the handler as a policy violation rather than rejected pre-upgrade.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session=s1", &websocket.DialOptions{
		Subprotocols: []string{"gritz-shell.v99"},
	})
	assert.NilError(t, err)
	assert.Equal(t, resp.StatusCode, http.StatusSwitchingProtocols)
	assert.Equal(t, conn.Subprotocol(), "") // the server declined the unknown token

	// The connection is unusable: the handler closed it with a policy violation.
	_, _, readErr := conn.Read(ctx)
	assert.Assert(t, readErr != nil)
	assert.Equal(t, websocket.CloseStatus(readErr), websocket.StatusPolicyViolation)
}

func TestAttachRejectsUnknownSession(t *testing.T) {
	t.Parallel()
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: testOrg}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session=nope", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})

	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusNotFound)
}

func TestAttachForeignOrgIndistinguishableFromMissing(t *testing.T) {
	t.Parallel()
	// A caller in the wrong org must not be able to tell an existing-but-foreign
	// session apart from a nonexistent one: both must return the identical
	// not-found response (same status and body). Otherwise a 403-vs-404
	// difference would leak session existence across orgs (issue #1141).
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	// The caller belongs to a different org than the session's owner.
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: testOrg + 1}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	// "s1" exists but is owned by a foreign org; "nope" was never seeded.
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	attach := func(session string) (int, string) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session="+session, &websocket.DialOptions{
			Subprotocols: []string{shellwire.Subprotocol},
		})
		assert.Assert(t, err != nil)
		body, readErr := io.ReadAll(resp.Body)
		assert.NilError(t, readErr)
		assert.NilError(t, resp.Body.Close())
		return resp.StatusCode, string(body)
	}

	foreignStatus, foreignBody := attach("s1")
	missingStatus, missingBody := attach("nope")

	// The wrong-org attach returns not-found...
	assert.Equal(t, foreignStatus, http.StatusNotFound)
	// ...and is byte-for-byte identical to attaching a nonexistent session.
	assert.Equal(t, foreignStatus, missingStatus)
	assert.Equal(t, foreignBody, missingBody)
}

func TestAttachRejectsMissingCaller(t *testing.T) {
	t.Parallel()
	// The attach handler is mounted without a caller in context, exercising the
	// 401 path.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/attach", reg.AttachHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session=s1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})

	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusUnauthorized)
}

func TestAttachCookieCallerResolvesOrg(t *testing.T) {
	t.Parallel()
	// A browser operator authenticates via its cookie session (no org claim on the
	// caller) and passes org_id in the query; the resolver maps it to the session's
	// owning org, so the attach succeeds.
	reg := shellserver.New(shellserver.Options{
		EstablishTimeout: time.Minute,
		OrgResolver: &shellserver.OrgResolverMock{
			ResolveOrgFunc: func(_ context.Context, _ string, _ int64) (int64, error) {
				return testOrg, nil
			},
		},
	})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{
		ID:   "op",
		Type: apiauth.AuthTypeCookie,
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	driver, _, err := websocket.Dial(ctx, base+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })
	attach, resp, err := websocket.Dial(ctx, base+"/shell/attach?session=s1&org_id=1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.NilError(t, err)
	assert.Equal(t, resp.StatusCode, http.StatusSwitchingProtocols)
	assert.Equal(t, attach.Subprotocol(), shellwire.Subprotocol)
	t.Cleanup(func() { attach.Close(websocket.StatusNormalClosure, "test done") })

	// The relay is live: a byte from the driver reaches the browser operator.
	payload := []byte{0x00, 'h', 'i'}
	send(t, driver, payload)
	typ, got := recv(t, attach)
	assert.Equal(t, typ, websocket.MessageBinary)
	assert.DeepEqual(t, got, payload)
}

func TestAttachCookieCallerRejectsInvalidOrgID(t *testing.T) {
	t.Parallel()
	// A cookie caller with a non-numeric org_id is a bad request, rejected before
	// the resolver is consulted.
	resolver := &shellserver.OrgResolverMock{
		ResolveOrgFunc: func(_ context.Context, _ string, _ int64) (int64, error) {
			return testOrg, nil
		},
	}
	reg := shellserver.New(shellserver.Options{
		EstablishTimeout: time.Minute,
		OrgResolver:      resolver,
	})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{
		ID:   "op",
		Type: apiauth.AuthTypeCookie,
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session=s1&org_id=abc", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusBadRequest)
	// The bad org_id is rejected before the resolver is ever consulted.
	assert.Assert(t, cmp.Len(resolver.ResolveOrgCalls(), 0))
}

func TestAttachCookieCallerRejectsNonMember(t *testing.T) {
	t.Parallel()
	// The resolver errors for a user that isn't a member of the requested org, so
	// the attach is forbidden — the resolver is the sole authorization boundary for
	// a cookie operator.
	reg := shellserver.New(shellserver.Options{
		EstablishTimeout: time.Minute,
		OrgResolver: &shellserver.OrgResolverMock{
			ResolveOrgFunc: func(_ context.Context, _ string, _ int64) (int64, error) {
				return 0, errors.New("not a member")
			},
		},
	})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{
		ID:   "intruder",
		Type: apiauth.AuthTypeCookie,
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session=s1&org_id=1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusForbidden)
}

func TestDriverRejectsUnknownSession(t *testing.T) {
	t.Parallel()
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver?session=nope", nil)

	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusNotFound)
}

func TestDriverRejectsMissingSession(t *testing.T) {
	t.Parallel()
	// A request with no ?session= query param falls through lookup to the same 404
	// as an unknown session (the empty id is never seeded).
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver", nil)

	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusNotFound)
}

func TestDriverBindsToSessionTask(t *testing.T) {
	t.Parallel()
	// A driver whose token is scoped to the session's own task is admitted: the
	// upgrade succeeds and the leg joins the rendezvous. This is the legitimate
	// path — the driver's task.read scope carries {task.id, task.archived:false},
	// which the handler's WithTaskID+WithTaskArchived(false) request satisfies.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	driver, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	assert.Equal(t, resp.StatusCode, http.StatusSwitchingProtocols)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })
}

func TestDriverRejectsForeignTask(t *testing.T) {
	t.Parallel()
	// The hijack scenario: a compromised agent in task A (holding task A's valid
	// token) dials task B's driver leg. The token passes RequireAuth but is scoped
	// to a different task, so the scope predicate on the session's task id fails and
	// the leg is rejected with 403 — before the WebSocket upgrade, so the attacker
	// never seizes the driver slot.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask + 1}),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver?session=s1", nil)

	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusForbidden)
	// The session is untouched: the rejected leg never joined, so the driver slot is
	// still free for the legitimate driver.
	assert.Assert(t, reg.Has("s1"))
}

func TestDriverRejectsMissingCaller(t *testing.T) {
	t.Parallel()
	// The driver handler is mounted without a caller in context (e.g. auth
	// middleware misconfigured), exercising the 401 path. Session existence (404) is
	// checked first, so this is a seeded session with no caller.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", reg.DriverHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver?session=s1", nil)

	assert.Assert(t, err != nil)
	assert.Equal(t, resp.StatusCode, http.StatusUnauthorized)
}

func TestEstablishTimeoutEvictsSession(t *testing.T) {
	t.Parallel()
	// Short, injected establishment timeout: connect only the driver leg.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: 100 * time.Millisecond})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	driver, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })

	// The session is evicted from the map and the lone leg is closed.
	waitFor(t, 3*time.Second, func() bool { return !reg.Has("s1") })
	_, _, err = driver.Read(ctx)
	assert.Assert(t, err != nil, "lone driver leg should be closed after establishment timeout")
}

func TestClosingOneLegEvictsSession(t *testing.T) {
	t.Parallel()
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: testOrg}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	driver, _, err := websocket.Dial(ctx, base+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })
	attach, _, err := websocket.Dial(ctx, base+"/shell/attach?session=s1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.NilError(t, err)
	send(t, driver, []byte{0x00, 'x'})
	recv(t, attach)

	driver.Close(websocket.StatusNormalClosure, "bye")

	_, _, readErr := attach.Read(ctx)
	assert.Assert(t, readErr != nil, "peer leg should be closed when one leg closes")
	waitFor(t, 3*time.Second, func() bool { return !reg.Has("s1") })
}

func TestSeedRejectsDuplicate(t *testing.T) {
	t.Parallel()
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	err := reg.Seed("s1", testOrg, testTask)

	assert.ErrorContains(t, err, "already exists")
}

func TestSeedRejectsEmptyID(t *testing.T) {
	t.Parallel()
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	t.Cleanup(reg.Close)

	err := reg.Seed("", testOrg, testTask)

	assert.ErrorContains(t, err, "empty session id")
}

func TestOnCloseFiresOnLegDrop(t *testing.T) {
	t.Parallel()
	// Establish both legs, then drop one: teardown should fire onClose exactly once
	// with the session id and owning org.
	rec := &closeRecorder{}
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute, OnClose: rec.fn()})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: testOrg}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	driver, _, err := websocket.Dial(ctx, base+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })
	attach, _, err := websocket.Dial(ctx, base+"/shell/attach?session=s1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.NilError(t, err)
	t.Cleanup(func() { attach.Close(websocket.StatusNormalClosure, "test done") })
	send(t, driver, []byte{0x00, 'x'})
	recv(t, attach)

	driver.Close(websocket.StatusNormalClosure, "bye")

	waitFor(t, 3*time.Second, func() bool { return rec.count() == 1 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.DeepEqual(t, rec.calls[0], [2]any{"s1", testOrg})
}

func TestOnCloseFiresOnEstablishTimeout(t *testing.T) {
	t.Parallel()
	// Only the driver leg connects: the establishment timeout tears the session
	// down, which must also fire onClose.
	rec := &closeRecorder{}
	reg := shellserver.New(shellserver.Options{EstablishTimeout: 100 * time.Millisecond, OnClose: rec.fn()})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  testOrg,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: testTask}),
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	driver, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/driver?session=s1", nil)
	assert.NilError(t, err)
	t.Cleanup(func() { driver.Close(websocket.StatusNormalClosure, "test done") })

	waitFor(t, 3*time.Second, func() bool { return rec.count() == 1 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.DeepEqual(t, rec.calls[0], [2]any{"s1", testOrg})
}

func TestOnCloseFiresOnRegistryClose(t *testing.T) {
	t.Parallel()
	// A never-connected session torn down by Close (server shutdown) still fires
	// onClose once.
	rec := &closeRecorder{}
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute, OnClose: rec.fn()})
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	reg.Close()

	waitFor(t, 3*time.Second, func() bool { return rec.count() == 1 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.DeepEqual(t, rec.calls[0], [2]any{"s1", testOrg})
}
