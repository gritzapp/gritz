package harness

import (
	"context"
	"strings"

	"github.com/gritzapp/gritz/internal/shell"
)

// Shell attaches to the debug-shell session as the org's owner, as gritz shell
// does, types input followed by exit, and returns the shell's output once it
// has exited with status 0.
func (o *Org) Shell(session, input string) string {
	t := o.server.t
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), o.server.timeout)
	defer cancel()
	conn, err := shell.Dial(ctx, shell.DialOptions{
		ServerURL: o.server.URL,
		Session:   session,
		Token:     o.server.userToken(o.Owner.UserID, o.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
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
