package shell_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/icholy/gritz/internal/auth/agentauth"
	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/server/shellserver"
	"github.com/icholy/gritz/internal/shell"
	"github.com/icholy/gritz/internal/shell/shellwire"
	"gotest.tools/v3/assert"
)

// testOrg owns the seeded shell session in these tests, and testTask is the task
// whose sandbox serves it — the driver leg is bound to this task.
const (
	testOrg  int64 = 1
	testTask int64 = 7
)

// newRelayServer mounts the real server-owned registry's two legs on an httptest
// server. The attach leg is wrapped with a test caller in testOrg so the org
// check admits it, and the driver leg with a task-scoped caller for testTask so
// the driver binding admits it (both policies are covered on their own in
// internal/server/shellserver).
// These tests exercise the three legs — driver (shell.Serve), relay, and operator
// (shell.Operate) — against each other for real over httptest WebSockets.
// Cleanups run LIFO: the registry is torn down before the server so parked
// handler goroutines are unblocked.
func newRelayServer(t *testing.T, reg *shellserver.Registry) *httptest.Server {
	t.Helper()
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
	return srv
}

// dialAttach connects the operator leg negotiating the shell subprotocol.
func dialAttach(t *testing.T, srv *httptest.Server, session string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url, err := shell.AttachURL(srv.URL, session)
	assert.NilError(t, err)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.NilError(t, err)
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// waitForCond polls cond until it holds or the deadline passes.
func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.Assert(t, cond(), "condition not met within %s", timeout)
}

// syncBuffer is a goroutine-safe buffer collecting the operator's stdout.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// operator drives shell.Operate over conn: stdin writes are sent as data frames,
// stdout is collected, and resize forwards terminal sizes. done carries the exit
// code once the shell exits.
type operator struct {
	stdin  *io.PipeWriter
	stdout *syncBuffer
	resize chan shell.WinSize
	done   chan opResult
}

type opResult struct {
	code int
	err  error
}

// runOperator starts shell.Operate against conn in the background.
func runOperator(t *testing.T, conn *websocket.Conn) *operator {
	t.Helper()
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })
	op := &operator{
		stdin:  inW,
		stdout: &syncBuffer{},
		resize: make(chan shell.WinSize, 1),
		done:   make(chan opResult, 1),
	}
	go func() {
		code, err := shell.Operate(t.Context(), shell.OperateOptions{
			Conn:   conn,
			In:     inR,
			Out:    op.stdout,
			Resize: op.resize,
		})
		op.done <- opResult{code: code, err: err}
	}()
	return op
}

// send writes input to the operator's stdin.
func (op *operator) send(t *testing.T, input string) {
	t.Helper()
	_, err := op.stdin.Write([]byte(input))
	assert.NilError(t, err)
}

// waitFor blocks until the operator's stdout contains substr.
func (op *operator) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(op.stdout.String(), substr) {
			return
		}
		select {
		case r := <-op.done:
			t.Fatalf("operator exited before %q appeared (code=%d err=%v); output: %q", substr, r.code, r.err, op.stdout.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %q; output: %q", substr, op.stdout.String())
}

// waitExit waits for shell.Operate to return and yields the exit code.
func (op *operator) waitExit(t *testing.T) int {
	t.Helper()
	select {
	case r := <-op.done:
		assert.NilError(t, r.err)
		return r.code
	case <-time.After(10 * time.Second):
		t.Fatal("operator did not return after shell exit")
		return 0
	}
}

func TestServeAndOperate(t *testing.T) {
	t.Parallel()
	// Arrange: real relay, a seeded session, the driver leg (Serve), and the
	// operator leg (Operate) both dialed against the httptest server.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("s1", testOrg, testTask))

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- shell.Serve(t.Context(), shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "s1", Log: slog.Default()})
	}()

	op := runOperator(t, dialAttach(t, srv, "s1"))

	// Act + Assert: operator stdin -> shell stdin -> shell stdout -> operator stdout.
	op.send(t, "echo hello\n")
	op.waitFor(t, "hello")

	// Act + Assert: a resize propagates — stty reports the new size back.
	op.resize <- shell.WinSize{Rows: 40, Cols: 100}
	op.send(t, "stty size\n")
	op.waitFor(t, "40 100")

	// Act + Assert: ending the shell propagates the exit code and Serve returns.
	op.send(t, "exit 0\n")
	assert.Equal(t, op.waitExit(t), 0)

	select {
	case err := <-serveErr:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after shell exited")
	}
}

func TestServe_LargeBurstSurvivesReadLimit(t *testing.T) {
	t.Parallel()
	// Regression for the 32768-vs-32769 read-limit bug. coder/websocket defaults to
	// a 32768-byte read limit, but both legs frame payloads with a 32*1024 buffer,
	// so a full read yields a 1+32768 = 32769-byte data frame — one byte over the
	// limit. No leg raised it, so the first >=32 KiB burst tripped
	// StatusMessageTooBig and tore the whole three-leg session down.
	//
	// The operator's stdin path is the reliable trigger: a >32 KiB paste is read
	// into a single 32769-byte frame that the relay's attach leg and the driver leg
	// both read at the default limit. (The PTY-output direction can't be used to
	// trip it in a test — the kernel caps a single PTY master read well under
	// 32 KiB — so this drives the burst through stdin.) The test asserts every byte
	// crosses all three legs intact and the session survives.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("burst", testOrg, testTask))

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- shell.Serve(t.Context(), shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "burst", Log: slog.Default()})
	}()

	op := runOperator(t, dialAttach(t, srv, "burst"))

	// Establish liveness before the burst so a teardown is unambiguously the burst.
	op.send(t, "echo ready\n")
	op.waitFor(t, "ready")

	// Put the tty in raw mode with echo off, announce readiness, then have `head -c`
	// consume exactly the paste and `wc -c` report the byte count. Raw mode avoids
	// the canonical line-length limit (which would truncate a newline-less paste)
	// and keeps stdout free of echo noise. Markers are split ('PAS''TE') so the
	// echoed command line doesn't itself contain them.
	const burst = 100000
	op.send(t, "stty raw -echo; printf 'PAS''TE'; head -c 100000 | wc -c; printf ';EN''D\\n'\n")
	op.waitFor(t, "PASTE")

	// Send the whole burst as one write. Without the fix the first 32769-byte frame
	// trips the read limit and the session is torn down, so this write may block on
	// a dead pipe — do it in the background and let waitFor observe the teardown via
	// the operator exiting. t.Cleanup closes the pipe writer, unblocking it.
	go func() { _, _ = op.stdin.Write([]byte(strings.Repeat("x", burst))) }()

	// With the fix, every byte crosses driver+relay+operator and wc reports 100000.
	op.waitFor(t, "END")
	out := op.stdout.String()
	between := out[strings.Index(out, "PASTE"):]
	assert.Assert(t, strings.Contains(between, "100000"),
		"shell did not receive all %d pasted bytes; saw: %q", burst, between)

	// The session survived the burst: the shell still responds and exits cleanly.
	op.send(t, "stty sane; exit 0\n")
	assert.Equal(t, op.waitExit(t), 0)
	select {
	case err := <-serveErr:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after shell exited")
	}
}

func TestServe_ExitCode(t *testing.T) {
	t.Parallel()
	// Arrange
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("s2", testOrg, testTask))

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- shell.Serve(t.Context(), shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "s2", Log: slog.Default()})
	}()

	op := runOperator(t, dialAttach(t, srv, "s2"))

	// Act: exit with a non-zero status.
	op.send(t, "exit 7\n")

	// Assert: the operator observes the shell's exit code.
	assert.Equal(t, op.waitExit(t), 7)
	select {
	case err := <-serveErr:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after shell exited")
	}
}

func TestSessionTornDownWhenOperatorDisconnects(t *testing.T) {
	t.Parallel()
	// Arrange: both legs connected and actively relaying. Serve runs on a context
	// canceled during cleanup so a leaked shell can't outlive the test.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("s3", testOrg, testTask))

	serveCtx, cancelServe := context.WithCancel(t.Context())
	t.Cleanup(cancelServe)
	go func() {
		_ = shell.Serve(serveCtx, shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "s3", Log: slog.Default()})
	}()

	attach := dialAttach(t, srv, "s3")
	op := runOperator(t, attach)
	op.send(t, "echo up\n")
	op.waitFor(t, "up")

	// Act: the operator leg disconnects abruptly.
	attach.CloseNow()

	// Assert: dropping one leg tears the whole session down, and the operator's
	// pump returns an error rather than blocking.
	waitForCond(t, 5*time.Second, func() bool { return !reg.Has("s3") })
	select {
	case r := <-op.done:
		assert.Assert(t, r.err != nil, "Operate should error once its leg is closed")
	case <-time.After(5 * time.Second):
		t.Fatal("operator did not return after its leg was closed")
	}
}

func TestServe_ContextCancelTearsDownBlockedShell(t *testing.T) {
	t.Parallel()
	// Regression for the driver ignoring SIGTERM during a live shell session. The
	// driver cancels Serve's context on SIGTERM; if teardown relied on the shell
	// noticing the PTY closing, a shell blocked in a foreground child would never
	// exit and Serve (and the driver) would hang. On cancellation cmd.Cancel closes
	// the connection first (so the operator gets instant feedback), then SIGTERMs
	// the shell's process group, and the WaitDelay backstop guarantees Serve returns
	// within the grace window regardless — so the driver can never hang.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("s4", testOrg, testTask))

	serveCtx, cancelServe := context.WithCancel(t.Context())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- shell.Serve(serveCtx, shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "s4", Log: slog.Default()})
	}()

	op := runOperator(t, dialAttach(t, srv, "s4"))

	// Run a foreground child that blocks forever, so closing the PTY alone would
	// not make the shell exit — only signaling the process group does.
	op.send(t, "echo up; sleep 100\n")
	op.waitFor(t, "up")

	// Act: the driver received SIGTERM — Serve's context is canceled.
	cancelServe()

	// Assert: the operator's leg is closed promptly — cmd.Cancel closes the
	// connection first, so the operator gets instant feedback rather than hanging.
	select {
	case r := <-op.done:
		assert.Assert(t, r.err != nil, "operator should error once the connection is closed on teardown")
	case <-time.After(5 * time.Second):
		t.Fatal("operator did not return after the connection was closed on teardown")
	}

	// Assert: Serve returns promptly rather than blocking on cmd.Wait, and the
	// relay session is torn down.
	select {
	case err := <-serveErr:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context was canceled")
	}
	waitForCond(t, 5*time.Second, func() bool { return !reg.Has("s4") })
}

func TestServe_OperatorDisconnectTearsDownBlockedShell(t *testing.T) {
	t.Parallel()
	// Regression for the sandbox not being reaped when the operator disconnects
	// (the web UI "Close shell", or the browser closing the attach WebSocket). The
	// relay closes the driver leg, so Serve's WebSocket->PTY read errors. If teardown
	// relied on the shell noticing the PTY closing, a shell blocked in a foreground
	// child would never exit and Serve (and the driver, and the container) would hang.
	// The read error now cancels the shell's context, running the same robust teardown
	// as SIGTERM: cmd.Cancel SIGTERMs the process group and WaitDelay force-kills, so
	// Serve returns promptly and the session is torn down.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("s6", testOrg, testTask))

	// Serve runs on a context canceled during cleanup so a leaked shell can't
	// outlive the test — but the teardown under test is the operator disconnect,
	// not this cancellation.
	serveCtx, cancelServe := context.WithCancel(t.Context())
	t.Cleanup(cancelServe)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- shell.Serve(serveCtx, shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "s6", Log: slog.Default()})
	}()

	attach := dialAttach(t, srv, "s6")
	op := runOperator(t, attach)

	// Run a foreground child that blocks forever, so closing the PTY alone would
	// not make the shell exit — only signaling the process group does.
	op.send(t, "echo up; sleep 100\n")
	op.waitFor(t, "up")

	// Act: the operator leg disconnects abruptly (browser closed the attach socket).
	attach.CloseNow()

	// Assert: Serve returns promptly rather than blocking on cmd.Wait, and the
	// relay session is torn down — so the driver exits and the container stops.
	select {
	case err := <-serveErr:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the operator disconnected")
	}
	waitForCond(t, 5*time.Second, func() bool { return !reg.Has("s6") })
}

func TestServe_ContextCancelForceKillsUnresponsiveShell(t *testing.T) {
	t.Parallel()
	// The WaitDelay backstop: even a shell that ignores SIGTERM (and SIGHUP, so
	// the PTY closing can't take it down either) must not hang the driver. After
	// the grace period os/exec force-kills the shell process with SIGKILL and
	// cmd.Wait returns. This exercises the SIGTERM -> WaitDelay -> SIGKILL path.
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	srv := newRelayServer(t, reg)
	assert.NilError(t, reg.Seed("s5", testOrg, testTask))

	serveCtx, cancelServe := context.WithCancel(t.Context())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- shell.Serve(serveCtx, shell.ServeOptions{ServerURL: srv.URL, Token: "driver-token", Session: "s5", Log: slog.Default()})
	}()

	op := runOperator(t, dialAttach(t, srv, "s5"))

	// Make the shell itself impervious to SIGTERM and SIGHUP, then keep it
	// busy in the foreground so it never reads EOF. SIGTERM to the group can't stop
	// it and closing the PTY can't either — only the WaitDelay SIGKILL will. The
	// short inner sleep keeps the lone orphaned child (the caveat) short-lived.
	//
	// The traps must be installed before the cancel below, or the SIGHUP from the
	// PTY closing kills the still-unprotected shell in milliseconds instead of
	// exercising the backstop. Install them first, then print a readiness marker —
	// but the PTY echoes the typed command line, so a plain marker would match the
	// echo of the input before the shell has run anything. Split it ('ARM''ED') so
	// the echoed command shows ARM''ED while only the executed printf emits ARMED;
	// waiting for ARMED then guarantees the traps are in place.
	op.send(t, "trap '' TERM HUP; printf 'ARM''ED\\n'; while :; do sleep 1; done\n")
	op.waitFor(t, "ARMED")

	// Act: the driver received SIGTERM — Serve's context is canceled.
	start := time.Now()
	cancelServe()

	// Assert: Serve still returns — via the force-kill backstop — and only after
	// the grace period elapsed (proving SIGTERM alone did not stop this shell).
	select {
	case err := <-serveErr:
		assert.NilError(t, err)
		elapsed := time.Since(start)
		assert.Assert(t, elapsed >= 1500*time.Millisecond,
			"Serve returned in %s — too fast for the WaitDelay backstop; SIGTERM must not have stopped this shell", elapsed)
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after its context was canceled — WaitDelay backstop did not fire")
	}
	waitForCond(t, 5*time.Second, func() bool { return !reg.Has("s5") })
}

func TestDriverURL(t *testing.T) {
	t.Parallel()
	url, err := shell.DriverURL("https://example.com", "abc")
	assert.NilError(t, err)
	assert.Equal(t, url, "wss://example.com/shell/driver?session=abc")

	url, err = shell.DriverURL("http://example.com/", "abc")
	assert.NilError(t, err)
	assert.Equal(t, url, "ws://example.com/shell/driver?session=abc")

	// The session id is escaped into the query, not concatenated raw.
	url, err = shell.DriverURL("https://example.com", "a b&c")
	assert.NilError(t, err)
	assert.Equal(t, url, "wss://example.com/shell/driver?session=a+b%26c")

	_, err = shell.DriverURL("", "abc")
	assert.ErrorContains(t, err, "empty server URL")
}

func TestAttachURL(t *testing.T) {
	t.Parallel()
	url, err := shell.AttachURL("https://example.com", "abc")
	assert.NilError(t, err)
	assert.Equal(t, url, "wss://example.com/shell/attach?session=abc")

	url, err = shell.AttachURL("http://example.com/", "abc")
	assert.NilError(t, err)
	assert.Equal(t, url, "ws://example.com/shell/attach?session=abc")

	_, err = shell.AttachURL("", "abc")
	assert.ErrorContains(t, err, "empty server URL")
}
