package driverserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gritzapp/gritz/internal/auth/agentauth"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/server/shellserver"
	"github.com/gritzapp/gritz/internal/shell/shellwire"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// TestRun_ForksIntoShell verifies that when the task carries a shell_session,
// the run dials the relay and serves a debug shell instead of the agent path —
// wiring the RunSpec's credentials through to shell.Serve.
func TestRun_ForksIntoShell(t *testing.T) {
	t.Parallel()
	// Real server-owned registry with both legs; the attach leg is admitted by a
	// test caller in the session's org (the org policy is exercised in the server
	// package).
	reg := shellserver.New(shellserver.Options{EstablishTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("GET /shell/driver", apiauth.WithTestUser(reg.DriverHandler(), &apiauth.UserInfo{
		ID:     "driver",
		OrgID:  1,
		Scopes: agentauth.Scopes(agentauth.ScopeOptions{TaskID: 1}),
	}))
	mux.Handle("GET /shell/attach", apiauth.WithTestUser(reg.AttachHandler(), &apiauth.UserInfo{ID: "op", OrgID: 1}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(reg.Close)
	assert.NilError(t, reg.Seed("s1", 1, 1))

	// A run whose task carries the shell_session, pointed at the relay.
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	te.gritz.GetTaskFunc = func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
		return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Version: testVersion, ShellSession: "s1"}}, nil
	}
	spec := testSpec()
	spec.ServerUrl = srv.URL
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: spec})

	// Connect the operator leg and exercise the shell the fork spawned.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	attach, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/shell/attach?session=s1", &websocket.DialOptions{
		Subprotocols: []string{shellwire.Subprotocol},
	})
	assert.NilError(t, err)
	t.Cleanup(func() { attach.Close(websocket.StatusNormalClosure, "done") })

	// A command echoes back through the PTY as data frames.
	assert.NilError(t, attach.Write(ctx, websocket.MessageBinary, shellwire.Data([]byte("echo forked\n"))))
	var buf strings.Builder
	for !strings.Contains(buf.String(), "forked") {
		_, msg, err := attach.Read(ctx)
		assert.NilError(t, err)
		frame, err := shellwire.Parse(msg)
		assert.NilError(t, err)
		if frame.Type == shellwire.TypeData {
			buf.Write(frame.Payload)
		}
	}

	// Ending the shell ends the run, reported like an agent run.
	assert.NilError(t, attach.Write(ctx, websocket.MessageBinary, shellwire.Data([]byte("exit\n"))))
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: true}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
}
