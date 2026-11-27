package scenario_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestEnv(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Secrets:  map[string]string{"E2E_SECRET": "s3cret"},
		Commands: []string{"env > env"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	env := strings.Split(strings.TrimSpace(h.Backend.ReadFile(task.ID, "env")), "\n")
	assert.Assert(t, cmp.Contains(env, "GRITZ_TASK_ID="+strconv.FormatInt(task.ID, 10)))
	assert.Assert(t, cmp.Contains(env, "E2E_SECRET=s3cret"))
	dir := h.Backend.Dir(task.ID)
	home := testx.FindFunc(t, env, func(kv string, _ int) bool { return strings.HasPrefix(kv, "HOME=") })
	assert.Assert(t, strings.HasPrefix(strings.TrimPrefix(home, "HOME="), dir+string(filepath.Separator)), home)
}

func TestEnv_Variables(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Variables: map[string]workspace.VariableConfig{
			"JIRA_ISSUE": {Required: true},
			"BRANCH":     {Default: "main"},
			"LABEL":      {},
		},
		AgentVariables: map[string]string{"JIRA_ISSUE": "APP-142"},
		Commands:       []string{"env > env"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	env := strings.Split(strings.TrimSpace(h.Backend.ReadFile(task.ID, "env")), "\n")
	assert.Assert(t, cmp.Contains(env, "GRITZ_VAR_JIRA_ISSUE=APP-142"))
	assert.Assert(t, cmp.Contains(env, "GRITZ_VAR_BRANCH=main"))
	for _, kv := range env {
		assert.Assert(t, !strings.HasPrefix(kv, "GRITZ_VAR_LABEL="), kv)
	}
}

func TestEnv_VariablesStaleSchedule(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Variables: map[string]workspace.VariableConfig{
			"JIRA_ISSUE": {Required: true},
		},
		AgentVariables: map[string]string{"JIRA_ISSUE": "APP-142"},
	}})
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId:   harness.DefaultRunner,
		Workspaces: []*gritzv1.RegisteredWorkspace{{Name: harness.DefaultWorkspace}},
	})
	// The server's registration declares no variables, so an agent without
	// any saves, while the runner's workspace requires one.
	stale := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "stale",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{
		AgentId:      stale.Id,
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
		CronExpr:     "@daily",
	})

	resp, err := h.Org.Client.RunSchedule(t.Context(), &gritzv1.RunScheduleRequest{Id: sched.Id})
	assert.NilError(t, err)

	id := resp.Task.Id
	h.Org.WaitTask(id, func(task *gritzv1.Task) bool { return task.Status == gritzv1.TaskStatus_FAILED })
	failed := h.Org.WaitEvent(id, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.Assert(t, cmp.Contains(failed.GetLifecycle().Message, `variable "JIRA_ISSUE": required`))
	assert.Assert(t, cmp.Len(h.Agent.Runs(id), 0))
}

func TestSecretsMasked(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Secrets: map[string]string{"E2E_SECRET": "s3cret"},
		// The token is also kept in a file, unshipped, to compare against.
		Commands: []string{"echo $E2E_SECRET; echo $GRITZ_TOKEN", "printenv GRITZ_TOKEN > token"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	log := task.WaitLog(func(log string) bool {
		return strings.Contains(log, "[gritz:masked E2E_SECRET]") && strings.Contains(log, "[gritz:masked token]")
	})
	token := strings.TrimSpace(h.Backend.ReadFile(task.ID, "token"))
	assert.Assert(t, token != "")
	assert.Assert(t, !strings.Contains(log, "s3cret"), log)
	assert.Assert(t, !strings.Contains(log, token), log)
}
