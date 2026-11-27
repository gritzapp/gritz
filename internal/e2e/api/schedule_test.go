package api_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestAgentSchedule_InvalidTimezone(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "nightly",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{{Name: "nightly", CronExpr: "0 9 * * *"}},
	}})

	// Act
	edited := h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id})
	edited.Schedules[0].Timezone = "Mars/Phobos"
	_, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: edited})

	// Assert - rejected, and the stored schedule is unchanged.
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, "invalid timezone")
	assert.DeepEqual(t, h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id}), agent, protocmp.Transform())
}

func TestAgentSchedule_Enable(t *testing.T) {
	t.Parallel()
	// Arrange - a disabled schedule has no next run.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "nightly",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{{Name: "nightly", CronExpr: "0 9 * * *"}},
	}})
	assert.Assert(t, agent.Schedules[0].NextRunAt == nil)

	// Act - enable it.
	agent.Schedules[0].Enabled = true
	resp, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert - its next run is computed from now.
	assert.NilError(t, err)
	assert.Assert(t, resp.Agent.Schedules[0].NextRunAt.AsTime().After(time.Now()))

	// Act - disable it.
	agent = resp.Agent
	agent.Schedules[0].Enabled = false
	resp, err = h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert - its next run is cleared.
	assert.NilError(t, err)
	assert.Assert(t, resp.Agent.Schedules[0].NextRunAt == nil)
}

// A fire creates one task of the schedule's agent, named after the schedule,
// with the created event attributed to the schedule and no instruction when
// neither the agent nor the schedule has a prompt, and records the run on the
// schedule.
func TestAgentSchedule_Fire(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "deps",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{{Name: "nightly", CronExpr: "0 9 * * *", Enabled: true}},
	}})
	before := time.Now()

	// Act
	h.Org.FireSchedule(agent.Id, agent.Schedules[0].Id)

	// Assert - exactly one task of the agent.
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Assert(t, cmp.Len(tasks, 1))
	task := tasks[0]
	assert.DeepEqual(t,
		task,
		&gritzv1.Task{
			Id:        task.Id,
			Name:      "nightly",
			Workspace: harness.DefaultWorkspace,
			Runner:    harness.DefaultRunner,
			AgentId:   agent.Id,
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "name", "workspace", "runner", "agent_id"),
	)
	// The created event first (the schedule), then the start command. The
	// server-assigned id, task_id and created_at are ignored.
	assert.DeepEqual(t,
		h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.Id}),
		[]*gritzv1.Event{
			{Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{
				Kind:     gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED,
				Actor:    &gritzv1.Actor{Kind: model.ActorKindSchedule},
				ToStatus: model.TaskStatusPending.Label(),
			}}},
			{Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{
				Command:   gritzv1.TaskCommand_START,
				Version:   1,
				Runner:    harness.DefaultRunner,
				Workspace: harness.DefaultWorkspace,
			}}},
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "task_id", "created_at"),
	)
	// The run is recorded on the schedule, and its next run moves on.
	sched := h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id}).Schedules[0]
	assert.Equal(t, sched.LastTaskId, task.Id)
	assert.Assert(t, !sched.LastRunAt.AsTime().Before(before.Truncate(time.Microsecond)))
	assert.Assert(t, sched.NextRunAt.AsTime().After(before))
}

// A fire creates a task of the schedule's agent as it is when it fires: its
// workspace, runner, auto-archive, variables and prompt, followed by the
// schedule's prompt.
func TestAgentSchedule_FireAgentTemplate(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{{
			Name: "story",
			Variables: []*gritzv1.VariableDecl{
				{Name: "BRANCH", Default: "main"},
				{Name: "JIRA_ISSUE", Required: true},
			},
		}},
	})
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:        "story",
		Workspace:   "story",
		Runner:      "runner-1",
		Prompt:      "Groom the backlog.",
		AutoArchive: durationpb.New(time.Hour),
		Variables:   map[string]string{"JIRA_ISSUE": "APP-142"},
		Schedules:   []*gritzv1.AgentSchedule{{Name: "nightly", CronExpr: "@daily", Enabled: true, Prompt: "Start with the oldest epic."}},
	}})
	agent.Variables = map[string]string{"JIRA_ISSUE": "APP-143"}
	agent.Prompt = "Groom the backlog, oldest first."
	_, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})
	assert.NilError(t, err)

	// Act
	h.Org.FireSchedule(agent.Id, agent.Schedules[0].Id)

	// Assert
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Assert(t, cmp.Len(tasks, 1))
	task := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: tasks[0].Id})
	assert.DeepEqual(t,
		task,
		&gritzv1.Task{
			Id:          task.Id,
			Name:        "nightly",
			Workspace:   "story",
			Runner:      "runner-1",
			AgentId:     agent.Id,
			AutoArchive: durationpb.New(time.Hour),
			Variables:   map[string]string{"JIRA_ISSUE": "APP-143"},
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "name", "workspace", "runner", "agent_id", "auto_archive", "variables"),
	)
	// The created and start command events carry no instruction; the agent's
	// prompt and then the schedule's are separate instructions.
	assert.DeepEqual(t,
		h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.Id}),
		[]*gritzv1.Event{
			{},
			{},
			{Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "Groom the backlog, oldest first.",
			}}},
			{Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "Start with the oldest epic.",
			}}},
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Event{}, "instruction"),
	)
}
