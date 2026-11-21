package harness

import (
	"context"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/gritzapp/gritz/internal/shell"
	"github.com/gritzapp/gritz/internal/shell/shellwire"
)

// Shell attaches to the debug-shell session as the org's owner, as gritz shell
// does, types input followed by exit, and returns the shell's output once it
// has exited with status 0.
func (o *Org) Shell(session, input string) string {
	t := o.server.t
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), o.server.timeout)
	defer cancel()
	url, err := shell.AttachURL(o.server.URL, session)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + o.server.userToken(o.Owner.UserID, o.ID)}},
	})
	if err != nil {
		t.Fatalf("attaching to shell %s: %v", session, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(shellwire.ReadLimit)
	if err := conn.Write(ctx, websocket.MessageBinary, shellwire.Data([]byte(input+"\nexit\n"))); err != nil {
		t.Fatalf("writing to shell %s: %v", session, err)
	}
	var out strings.Builder
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("reading shell %s: %v; output:\n%s", session, err, out.String())
		}
		frame, err := shellwire.Parse(msg)
		if err != nil {
			t.Fatal(err)
		}
		switch frame.Type {
		case shellwire.TypeData:
			out.Write(frame.Payload)
		case shellwire.TypeExit:
			code, err := frame.ExitCode()
			if err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				t.Fatalf("shell %s exited with %d; output:\n%s", session, code, out.String())
			}
			return out.String()
		}
	}
}
