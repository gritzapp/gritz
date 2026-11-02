package apiserver

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateSchedule_RequiresCreateScope(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{
			{RunnerID: "test-runner", Name: "test-workspace"},
		},
	})

	// A member holding only task-read (org membership, no create scope) is denied.
	readOnly := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskRead)})
	_, err := srv.CreateSchedule(readOnly, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// A caller holding task-create for the target (workspace, runner) may create.
	creator := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace("test-workspace"),
		authscope.WithTaskRunner("test-runner"),
		authscope.WithTaskArchived(false),
	)})
	_, err = srv.CreateSchedule(creator, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.NilError(t, err)
}
