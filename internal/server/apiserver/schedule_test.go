package apiserver

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
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
	agent := teststore.CreateAgent(t, srv.store, org, &model.Agent{Workspace: "test-workspace"})
	req := &gritzv1.CreateScheduleRequest{AgentId: agent.ID, CronExpr: "0 9 * * *"}

	// A member holding only task-read (org membership, no create scope) is denied.
	readOnly := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskRead)})
	_, err := srv.CreateSchedule(readOnly, req)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// A caller holding task-create for another target than the agent's is denied.
	elsewhere := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace("other-workspace"),
		authscope.WithTaskRunner("test-runner"),
		authscope.WithTaskArchived(false),
	)})
	_, err = srv.CreateSchedule(elsewhere, req)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// A caller holding task-create for the agent's target (workspace, runner)
	// may create.
	creator := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace("test-workspace"),
		authscope.WithTaskRunner("test-runner"),
		authscope.WithTaskArchived(false),
	)})
	_, err = srv.CreateSchedule(creator, req)
	assert.NilError(t, err)
}
