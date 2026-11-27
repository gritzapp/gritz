package scenario_test

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestFeatures(t *testing.T) {
	t.Parallel()
	jira := agent.McpServer{Type: "http", URL: "https://jira.example.com/mcp"}
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Features: workspace.Features{
			"jira": {
				Secrets:    map[string]string{"JIRA_TOKEN": "j1ra-t0ken"},
				McpServers: map[string]agent.McpServer{"jira": jira},
				Commands:   []string{"echo $JIRA_TOKEN"},
			},
		},
		AgentFeatures: []string{"jira"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	// The feature's command ran, and its secret is masked in the shipped log.
	log := task.WaitLog(func(log string) bool {
		return strings.Contains(log, "[gritz:masked JIRA_TOKEN]")
	})
	assert.Assert(t, !strings.Contains(log, "j1ra-t0ken"), log)
	// The feature's MCP server is in the agent config, beside the injected one.
	var cfg agent.Config
	path := fmt.Sprintf("tmp/gritz/%d.json", task.ID)
	assert.NilError(t, json.Unmarshal([]byte(h.Backend.ReadFile(task.ID, path)), &cfg))
	assert.DeepEqual(t, cfg.McpServers["jira"], jira)
	assert.Assert(t, cmp.Contains(cfg.McpServers, "gritz"))
}

func TestFeatures_Agent(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Features: workspace.Features{
			"dummy": {
				Agent: &workspace.Agent{
					Type:  agent.TypeDummy,
					Dummy: &workspace.DummyConfig{Commands: []string{"echo dummy > ran"}},
				},
			},
		},
		AgentFeatures: []string{"dummy"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})

	// The dummy harness replaces the workspace's puppet: it ran its command
	// and completed the task, and the puppet never saw a run.
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, h.Backend.ReadFile(task.ID, "ran"), "dummy\n")
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))
}
