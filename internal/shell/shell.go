// Package shell holds the client-side core of the reverse debug shell (the
// design in proposals/draft/driver-reverse-shell.md): the driver leg (Serve) and
// the operator leg (Attach), plus the shared route/URL helpers both legs and the
// server route registration use. The server-side rendezvous relay lives in the
// shellrelay sub-package, and the wire framing lives in internal/shell/shellwire.
//
// Serve allocates a PTY, spawns a shell, dials the server's shell relay
// WebSocket for a rendezvous session, and pipes the PTY over it using the
// shellwire framing. Attach is its operator-side counterpart: it dials the
// attach leg, drives the local terminal, and returns the shell's exit code. Both
// are plain library functions that take the server URL, the caller's token, and
// the session id, and do not depend on the driver, agent, or server packages.
//
// The wire contract lives entirely in internal/shell/shellwire; the relay passes frames
// through opaquely and never parses them.
package shell

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/gritzapp/gritz/internal/shell/shellwire"
)

// exitReportTimeout bounds the best-effort send of the final exit frame once the
// shell process has exited.
const exitReportTimeout = 5 * time.Second

// shellGracePeriod is how long the shell's process group has to exit after it is
// sent SIGTERM on ctx cancellation before os/exec force-kills the shell process
// and cmd.Wait returns. It bounds how long Serve can take to return on teardown.
const shellGracePeriod = 3 * time.Second

// CreateSessionID returns an unguessable rendezvous id for a debug-shell
// session. It is not a secret — the attach leg is authorized by org membership,
// not by knowing the id — but a random id keeps sessions from colliding and from
// being enumerable.
func CreateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating shell session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ServeOptions configures Serve. A nil Log falls back to slog.Default.
type ServeOptions struct {
	ServerURL string
	Token     string
	Session   string
	Log       *slog.Logger
}

// Serve runs an interactive debug shell for a rendezvous session. It allocates a
// PTY, spawns a shell ($SHELL, else /bin/sh), dials the server's shell
// relay WebSocket at GET {ServerURL}/shell/driver?session={Session} authenticating with
// Token as a Bearer header, negotiates the gritz-shell.v1 subprotocol, and
// pipes the PTY over the WebSocket using the shellwire framing.
//
// Incoming data frames are written to the PTY master, resize frames are applied
// to the PTY, and PTY output is streamed back as data frames. When the shell
// exits, Serve sends an exit frame with the shell's exit code and closes the
// WebSocket cleanly. A dropped operator leg or a canceled ctx both run the same
// robust teardown (see cmd.Cancel below), so the shell is always reaped and
// Serve returns rather than leaking a shell or hanging the driver.
func Serve(ctx context.Context, opts ServeOptions) error {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	session := opts.Session
	log.Info("starting reverse shell", "session", session)

	shell := cmp.Or(os.Getenv("SHELL"), "/bin/sh")
	// cmdCtx drives the shell's lifecycle. It is canceled on ctx cancellation
	// (SIGTERM) via exec.CommandContext's watchdog, and explicitly by the
	// WebSocket->PTY goroutine when the operator leg drops — routing both teardown
	// triggers through the single robust cmd.Cancel path below.
	cmdCtx, cancelCmd := context.WithCancel(ctx)
	defer cancelCmd()
	// exec.CommandContext (not exec.Command) installs the cmdCtx watchdog that
	// auto-invokes cmd.Cancel when cmdCtx is canceled. pty.Start calls cmd.Start
	// under the hood, so the watchdog still gets wired up — they compose.
	// Deliberately not a login shell: /etc/profile resets PATH, which would
	// drop entries the image sets via ENV (e.g. /root/.local/bin for claude).
	// A plain shell inherits the driver's environment as-is.
	cmd := exec.CommandContext(cmdCtx, shell)

	url, err := DriverURL(opts.ServerURL, session)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + opts.Token}},
	})
	if err != nil {
		return fmt.Errorf("failed to dial shell relay: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(shellwire.ReadLimit)

	// Tear the session down promptly when cmdCtx is canceled — either the driver
	// received SIGTERM (ctx canceled) or the operator leg dropped (the WebSocket->PTY
	// goroutine cancels cmdCtx). Relying on the shell noticing the PTY closing is not
	// enough: a shell blocked in a foreground child (an editor, a pager, anything the
	// operator left running) may never notice the resulting EOF, so cmd.Wait would
	// hang and the driver would never exit. Cmd.Cancel runs on cancellation: it
	// closes the connection first — unblocking both WS pumps immediately and giving
	// the operator instant feedback — then sends SIGTERM to the shell's whole
	// process group so it can exit gracefully. pty.Start sets Setsid, so the shell
	// leads its own session and its PGID equals its PID, which is why the negative
	// PID addresses the group.
	cmd.Cancel = func() error {
		conn.CloseNow()
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	// If the group doesn't exit within the grace period, os/exec force-kills and
	// cmd.Wait returns, so the driver can never hang. Caveat: WaitDelay's force
	// step is os.Process.Kill — a single-process SIGKILL of the shell, not the
	// group — so a stubborn child could be orphaned at the backstop. For a debug
	// shell that's acceptable: SIGTERM-to-group handles the normal case and
	// WaitDelay only guarantees no hang.
	cmd.WaitDelay = shellGracePeriod

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// WebSocket -> PTY: apply incoming data/resize frames. On any read error —
	// the operator leg dropped, the session was torn down, or ctx was canceled —
	// cancel cmdCtx so the shared cmd.Cancel teardown runs (close the conn, SIGTERM
	// the shell's process group, force-kill via WaitDelay), guaranteeing the shell
	// dies and cmd.Wait returns even if it is blocked in a foreground child that
	// would never notice the PTY closing.
	go func() {
		for {
			typ, msg, err := conn.Read(ctx)
			if err != nil {
				cancelCmd()
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			frame, err := shellwire.Parse(msg)
			if err != nil {
				continue
			}
			switch frame.Type {
			case shellwire.TypeData:
				if _, err := ptmx.Write(frame.Payload); err != nil {
					return
				}
			case shellwire.TypeResize:
				rows, cols, err := frame.ResizeDims()
				if err != nil {
					continue
				}
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})
			case shellwire.TypePing:
				// keepalive; nothing to apply.
			}
		}
	}()

	// PTY -> WebSocket: stream shell output as data frames. Finishes when the
	// shell closes the PTY (read error) or the connection drops (write error).
	ptyDone := make(chan struct{})
	go func() {
		defer close(ptyDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, shellwire.Data(buf[:n])); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	// Wait for the output pump to stop before sending the exit frame: only one
	// writer may touch the connection at a time.
	<-ptyDone

	// Send the exit frame on a context detached from ctx so it still goes out
	// during a cancellation-driven shutdown.
	exitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exitReportTimeout)
	defer cancel()
	if err := conn.Write(exitCtx, websocket.MessageBinary, shellwire.Exit(exitCode(waitErr))); err != nil {
		log.Debug("failed to send shell exit frame", "session", session, "err", err)
	}
	conn.Close(websocket.StatusNormalClosure, "shell exited")
	log.Info("reverse shell ended", "session", session, "exit_code", exitCode(waitErr))
	return nil
}

// exitCode extracts a shell exit code from the error returned by cmd.Wait: 0 on
// success, the process exit code on a normal non-zero exit, and 1 for any other
// failure (e.g. the shell was killed).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
