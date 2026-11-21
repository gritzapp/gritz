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
	var out strings.Builder
	code, err := shell.Operate(ctx, shell.OperateOptions{
		Conn: conn,
		In:   strings.NewReader(input + "\nexit\n"),
		Out:  &out,
	})
	if err != nil {
		t.Fatalf("shell %s: %v; output:\n%s", session, err, out.String())
	}
	if code != 0 {
		t.Fatalf("shell %s exited with %d; output:\n%s", session, code, out.String())
	}
	return out.String()
}
