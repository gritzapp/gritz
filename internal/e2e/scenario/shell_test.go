package scenario_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestShell_RunEnvironment(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Secrets: map[string]string{"E2E_SECRET": "s3cret"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	resp, err := h.Org.Client.OpenShell(t.Context(), &gritzv1.OpenShellRequest{TaskId: task.ID})
	assert.NilError(t, err)

	h.Org.Shell(resp.SessionId, "env > shellenv")
	env := strings.Split(strings.TrimSpace(h.Backend.ReadFile(task.ID, "shellenv")), "\n")
	assert.Assert(t, cmp.Contains(env, "GRITZ_TASK_ID="+strconv.FormatInt(task.ID, 10)))
	assert.Assert(t, cmp.Contains(env, "GRITZ_SERVER="+h.Server.URL))
	assert.Assert(t, cmp.Contains(env, "E2E_SECRET=s3cret"))
}
