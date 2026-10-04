// Package shellrelay implements the leg-agnostic rendezvous primitive for the
// driver reverse shell (the design in proposals/draft/driver-reverse-shell.md).
//
// A Session bridges two WebSocket legs: once both are connected the relay copies
// WebSocket frames verbatim in both directions. The relay is a mode-agnostic byte
// pump: it never parses or interprets the frame payload. The end-to-end
// [1-byte type][payload] framing is a contract between the two endpoints, opaque
// to the server.
//
// This package holds only the transport mechanics of a single rendezvous: the
// two-leg join, the establishment timeout, the verbatim pump, and the idempotent
// teardown. It knows nothing about session ids, orgs, authorization, or HTTP —
// the registry and the leg policy (which caller may attach, the reject-before-
// upgrade check, subprotocol negotiation) live one layer up in
// internal/server/shellserver.
package shellrelay

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gritzapp/gritz/internal/shell/shellwire"
)

// DefaultEstablishTimeout is the default connection-establishment timeout: if
// both legs are not connected within it, the session is torn down and any
// already-connected leg is disconnected. This is distinct from the idle timeout
// below, which governs an already-established session.
const DefaultEstablishTimeout = 30 * time.Second

// DefaultIdleTimeout is the default idle timeout for an established session: once
// both legs are connected, if no frame flows across the relay in either direction
// for this long, the session is torn down and both legs are disconnected. The
// timer is armed when the second leg connects and reset on every relayed frame.
const DefaultIdleTimeout = 5 * time.Minute

var (
	errLegTaken = errors.New("shellrelay: both legs already connected")
	errTornDown = errors.New("shellrelay: session torn down")
)

// Session is one two-leg rendezvous. The first Join is leg A, the second is
// leg B, and a third is rejected. Once both legs are connected each Join pumps
// frames from its peer verbatim until either side errors, at which point the
// whole session is torn down and both legs are closed.
//
// A Session is leg-agnostic: it does not distinguish the driver leg from the
// attach leg. That distinction — and everything that depends on the caller's
// identity — belongs to the server layer.
type Session struct {
	log *slog.Logger

	idleTimeout time.Duration // idle timeout for the established session; <= 0 disables it

	mu        sync.Mutex
	legs      [2]*websocket.Conn // filled in Join order; legs[0] is A, legs[1] is B
	count     int                // number of legs connected so far
	ready     chan struct{}      // closed once both legs are connected
	done      chan struct{}      // closed on teardown
	timer     *time.Timer        // establishment timeout, stopped when both legs connect
	idleTimer *time.Timer        // idle timeout, armed when both legs connect, reset on relayed frames
	closeOnce sync.Once
}

// SessionOptions configures NewSession.
//
// EstablishTimeout <= 0 falls back to DefaultEstablishTimeout; tests inject a
// small timeout to exercise the establishment-timeout path without sleeping the
// real default. IdleTimeout is the idle timeout applied once both legs connect;
// IdleTimeout <= 0 disables the idle timeout entirely. A nil Log falls back to
// slog.Default.
type SessionOptions struct {
	EstablishTimeout time.Duration
	IdleTimeout      time.Duration
	Log              *slog.Logger
}

// NewSession creates a rendezvous session and starts its establishment timeout.
func NewSession(opts SessionOptions) *Session {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	establishTimeout := opts.EstablishTimeout
	if establishTimeout <= 0 {
		establishTimeout = DefaultEstablishTimeout
	}
	s := &Session{
		log:         log,
		idleTimeout: opts.IdleTimeout,
		ready:       make(chan struct{}),
		done:        make(chan struct{}),
	}
	// Assign the timer under s.mu so the AfterFunc callback (which stops it via
	// teardown) has a happens-before edge to this write.
	s.mu.Lock()
	s.timer = time.AfterFunc(establishTimeout, func() {
		s.teardown("establishment timeout")
	})
	s.mu.Unlock()
	return s
}

// Join registers conn as a leg, waits for the peer leg (or teardown), then pumps
// frames from the peer to conn verbatim until either side errors. When the pump
// ends — for any reason — the whole session is torn down, closing both legs. It
// returns the terminating error, or errLegTaken if conn is a rejected third leg
// and errTornDown if the session is already gone.
func (s *Session) Join(ctx context.Context, conn *websocket.Conn) error {
	// The single place the relay's read limit is applied: both legs get it here as
	// they enter the pump. coder/websocket's default is one byte under a full
	// PTY-output frame (see shellwire.ReadLimit).
	conn.SetReadLimit(shellwire.ReadLimit)

	ready, err := s.register(conn)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "leg unavailable")
		return err
	}
	select {
	case <-ready:
	case <-s.done:
		conn.Close(websocket.StatusNormalClosure, "session closed")
		return errTornDown
	}
	// Pump until either side errors/closes; the read error is the normal
	// end-of-session signal. Each relayed frame resets the idle timer, so activity
	// in either direction keeps the session alive.
	pumpErr := s.pump(ctx, s.peer(conn), conn)
	s.teardown(closeReason(pumpErr))
	return pumpErr
}

// Close tears the session down, disconnecting any connected legs. It is safe to
// call more than once and from any goroutine.
func (s *Session) Close() {
	s.teardown("session closed")
}

// Done is closed when the session is torn down (by a dropped leg, the
// establishment timeout, or Close). The server uses it to evict the session from
// its registry regardless of which path tore it down.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// register records conn as the next leg and returns the ready channel, which is
// closed once both legs are present. It errors if both legs are already taken or
// the session is being torn down.
func (s *Session) register(conn *websocket.Conn) (<-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return nil, errTornDown
	default:
	}
	if s.count == len(s.legs) {
		return nil, errLegTaken
	}
	s.legs[s.count] = conn
	s.count++
	if s.count == len(s.legs) {
		s.timer.Stop()
		// The session is now established: swap the establishment deadline for the
		// idle deadline. Armed under s.mu so teardown (and resetIdle) observe the
		// write; idleTimeout <= 0 leaves idleTimer nil, disabling the idle timeout.
		if s.idleTimeout > 0 {
			s.idleTimer = time.AfterFunc(s.idleTimeout, func() {
				s.teardown("idle timeout")
			})
		}
		s.log.Info("shell session established")
		close(s.ready)
	}
	return s.ready, nil
}

// peer returns the opposite leg from conn. It is only called after both legs are
// connected, so the peer is always non-nil.
func (s *Session) peer(conn *websocket.Conn) *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.legs[0] == conn {
		return s.legs[1]
	}
	return s.legs[0]
}

// resetIdle restarts the idle timer, called on every frame relayed in either
// direction. It is a no-op when the idle timeout is disabled (idleTimer nil) or
// the session is already tearing down — the done check avoids re-arming a timer
// teardown just stopped (a re-arm would be harmless anyway: closeOnce collapses
// the resulting second teardown to nothing).
func (s *Session) resetIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return
	default:
	}
	if s.idleTimer != nil {
		s.idleTimer.Reset(s.idleTimeout)
	}
}

// teardown closes both legs and marks the session done. It is idempotent:
// repeated calls (from the timer, either pump goroutine, or Close) collapse to
// one via closeOnce.
func (s *Session) teardown(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Stop()
		}
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		legs := s.legs
		s.mu.Unlock()

		close(s.done)

		// CloseNow rather than a graceful Close: the pump goroutine is parked in
		// Read holding the conn's read lock, so the close handshake's
		// waitCloseHandshake could never acquire it and would burn its full timeout
		// (2 legs × ~5s, serialized, on every teardown and on Registry.Close at
		// shutdown). CloseNow skips the handshake — the pump's Read just errors,
		// which is already the teardown signal — and close(s.done) above has already
		// fired, so Done semantics are unaffected.
		for _, leg := range legs {
			if leg != nil {
				_ = leg.CloseNow()
			}
		}
		s.log.Info("shell session torn down", "reason", reason)
	})
}

// pump copies whole WebSocket messages from src to dst verbatim, preserving the
// message type, until a read or write error occurs. It never inspects the
// payload — the server is a mode-agnostic byte pump. Each received message resets
// the idle timer, so traffic in either direction counts as liveness.
func (s *Session) pump(ctx context.Context, dst, src *websocket.Conn) error {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return err
		}
		s.resetIdle()
		if err := dst.Write(ctx, typ, data); err != nil {
			return err
		}
	}
}

// closeReason renders a short teardown reason from the pump's terminating error.
func closeReason(err error) string {
	if err == nil {
		return "closed"
	}
	if status := websocket.CloseStatus(err); status != -1 {
		return "peer closed"
	}
	return "relay ended"
}
