package shell

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
	"github.com/gritzapp/gritz/internal/shell/shellwire"
	"golang.org/x/term"
)

// WinSize is a terminal size in rows and cols, forwarded to the shell as a
// resize frame.
type WinSize struct {
	Rows, Cols uint16
}

// AttachOptions configures Attach.
type AttachOptions struct {
	ServerURL string
	Token     string
	Session   string
}

// Attach runs the operator side of a debug-shell session. It dials the server's
// shell relay WebSocket at GET {ServerURL}/shell/attach?session={Session} authenticating
// with Token as a Bearer header, negotiates the gritz-shell.v1 subprotocol, puts
// the local terminal into raw mode, tracks its size (initial size plus SIGWINCH),
// and pipes stdin/stdout through the WebSocket using the shellwire framing until
// the shell exits. It returns the shell's exit code.
//
// It is the operator-side counterpart to Serve. internal/command/shell.go is a
// thin wrapper: it asks the server to open a session via the OpenShell RPC and
// then hands the session off to Attach.
func Attach(ctx context.Context, opts AttachOptions) (int, error) {
	url, err := AttachURL(opts.ServerURL, opts.Session)
	if err != nil {
		return 1, err
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + opts.Token}},
	})
	if err != nil {
		return 1, fmt.Errorf("failed to attach to shell: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(shellwire.ReadLimit)

	// Put the terminal into raw mode so keystrokes reach the remote shell
	// unbuffered and unechoed; always restore it on the way out.
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return 1, fmt.Errorf("failed to set raw terminal mode: %w", err)
		}
		defer func() { _ = term.Restore(fd, state) }()
	}

	// Track terminal size: seed the initial size, then follow SIGWINCH.
	resize := make(chan WinSize, 1)
	if cols, rows, err := term.GetSize(fd); err == nil {
		resize <- WinSize{Rows: uint16(rows), Cols: uint16(cols)}
	}
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	defer signal.Stop(sigwinch)
	go func() {
		for range sigwinch {
			cols, rows, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			select {
			case resize <- WinSize{Rows: uint16(rows), Cols: uint16(cols)}:
			case <-ctx.Done():
				return
			}
		}
	}()

	code, err := Operate(ctx, OperateOptions{
		Conn:   conn,
		In:     os.Stdin,
		Out:    os.Stdout,
		Resize: resize,
	})
	if err != nil {
		return code, fmt.Errorf("shell session ended: %w", err)
	}
	// CloseNow, not a graceful Close: the relay only ever tears a leg down with
	// CloseNow, so a graceful close here blocks for a close-frame reply that never
	// comes and burns the handshake timeout — the shell-exit hang.
	return code, conn.CloseNow()
}

// OperateOptions configures Operate.
type OperateOptions struct {
	Conn   *websocket.Conn
	In     io.Reader
	Out    io.Writer
	Resize <-chan WinSize
}

// Operate runs the operator side of the shellwire protocol over Conn until an
// Exit frame arrives — returning its code — or the connection errors. It reads
// local input from In and sends it as Data frames, writes incoming Data frames to
// Out, and forwards terminal sizes from Resize as Resize frames. Ping frames are
// keepalives and ignored.
//
// Attach wraps Operate with the dial and terminal setup; it is exported so the
// operator leg can be exercised end-to-end against a real relay over a WebSocket
// without a controlling terminal.
func Operate(ctx context.Context, opts OperateOptions) (int, error) {
	conn, in, out, resize := opts.Conn, opts.In, opts.Out, opts.Resize
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Serialize outgoing frames through a single writer goroutine: coder/websocket
	// permits only one concurrent writer, and both input and resize produce frames.
	frames := make(chan []byte)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				select {
				case frames <- shellwire.Data(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ws, ok := <-resize:
				if !ok {
					return
				}
				select {
				case frames <- shellwire.Resize(ws.Rows, ws.Cols):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			return 1, err
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
			if _, err := out.Write(frame.Payload); err != nil {
				return 1, err
			}
		case shellwire.TypeExit:
			code, err := frame.ExitCode()
			if err != nil {
				return 1, err
			}
			return code, nil
		case shellwire.TypePing:
			// keepalive; nothing to do.
		}
	}
}
