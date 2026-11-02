package scenario_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestEnv(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{
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

func TestSecretsMasked(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{
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
