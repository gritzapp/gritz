package apiserver_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateSchedule(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateSchedule(t.Context(), &gritzv1.CreateScheduleRequest{
		Name:      "nightly",
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		Namespace: "reviewbot",
		Instructions: []*gritzv1.Instruction{
			{Text: "bump deps", Url: "https://example.com/deps"},
		},
		CronExpr:    "0 9 * * *",
		Timezone:    "America/Toronto",
		Enabled:     true,
		AutoArchive: durationpb.New(time.Hour),
	})

	// Assert
	assert.NilError(t, err)
	// An enabled schedule gets a first fire time computed server-side.
	assert.Assert(t, resp.Schedule.NextRunAt != nil)
	expected := &gritzv1.Schedule{
		Id:        resp.Schedule.Id,
		Name:      "nightly",
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		Namespace: "reviewbot",
		Instructions: []*gritzv1.Instruction{
			{Text: "bump deps", Url: "https://example.com/deps"},
		},
		CronExpr:    "0 9 * * *",
		Timezone:    "America/Toronto",
		Enabled:     true,
		AutoArchive: durationpb.New(time.Hour),
		CreatedBy:   h.Org.Owner.UserID,
		NextRunAt:   resp.Schedule.NextRunAt, // nondeterministic (computed from now)
		CreatedAt:   resp.Schedule.CreatedAt,
		UpdatedAt:   resp.Schedule.UpdatedAt,
	}
	assert.DeepEqual(t, resp.Schedule, expected, protocmp.Transform())
	// It round-trips through Get. created_at/updated_at are ignored: the create
	// response carries the in-memory time at full precision, while the DB read-back
	// is truncated to Postgres's microsecond resolution.
	got := h.Org.GetSchedule(&gritzv1.GetScheduleRequest{Id: resp.Schedule.Id})
	assert.DeepEqual(t, got, resp.Schedule, protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Schedule{}, "created_at", "updated_at"))
}

func TestCreateSchedule_DisabledHasNoNextRun(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateSchedule(t.Context(), &gritzv1.CreateScheduleRequest{
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		CronExpr:  "0 9 * * *",
		Enabled:   false,
	})

	// Assert
	assert.NilError(t, err)
	// A disabled schedule stays out of the claim query: no next fire time.
	assert.Assert(t, resp.Schedule.NextRunAt == nil)
	assert.Assert(t, !resp.Schedule.Enabled)
}

func TestCreateSchedule_DefaultTimezone(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act - an empty timezone defaults to UTC.
	resp, err := h.Org.Client.CreateSchedule(t.Context(), &gritzv1.CreateScheduleRequest{
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		CronExpr:  "0 9 * * *",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Schedule.Timezone, "UTC")
}

func TestCreateSchedule_InvalidCron(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.CreateSchedule(t.Context(), &gritzv1.CreateScheduleRequest{
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		CronExpr:  "not a cron",
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	// The invalid schedule was never stored.
	assert.Assert(t, cmp.Len(h.Org.ListSchedules(&gritzv1.ListSchedulesRequest{}), 0))
}

func TestCreateSchedule_InvalidTimezone(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.CreateSchedule(t.Context(), &gritzv1.CreateScheduleRequest{
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		CronExpr:  "0 9 * * *",
		Timezone:  "Mars/Phobos",
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestCreateSchedule_BadWorkspace(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.CreateSchedule(t.Context(), &gritzv1.CreateScheduleRequest{
		Workspace: "fake-workspace",
		Runner:    e2e.DefaultRunner,
		CronExpr:  "0 9 * * *",
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListSchedules(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{Name: "a", CronExpr: "0 9 * * *"})
	h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{Name: "b", CronExpr: "0 10 * * *"})

	// Act & Assert
	resp, err := h.Org.Client.ListSchedules(t.Context(), &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Schedules, 2))

	// Act & Assert - list is org-scoped: another org sees none.
	resp, err = other.Client.ListSchedules(t.Context(), &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Schedules, 0))
}

func TestGetSchedule_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *"})

	// Act & Assert - its own org reads it.
	_, err := h.Org.Client.GetSchedule(t.Context(), &gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.NilError(t, err)

	// Act & Assert - another org gets not-found (org-scoped).
	_, err = other.Client.GetSchedule(t.Context(), &gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.ErrorContains(t, err, "not found")
}

// A member holding only task-read (no write/create scope) may list and get, but
// read scope is not enough to mutate: delete and enable require task-write.
func TestSchedule_ReadIsMembershipOnly(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *"})
	reader := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(authscope.OpTaskRead),
	})

	// Act & Assert - read scope covers get and list.
	_, err := reader.Client.GetSchedule(t.Context(), &gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.NilError(t, err)
	list, err := reader.Client.ListSchedules(t.Context(), &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(list.Schedules, 1))

	// Act & Assert - ...but not the mutations.
	_, err = reader.Client.SetScheduleEnabled(t.Context(), &gritzv1.SetScheduleEnabledRequest{
		Id:      sched.Id,
		Enabled: true,
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
	_, err = reader.Client.DeleteSchedule(t.Context(), &gritzv1.DeleteScheduleRequest{Id: sched.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// task-write (the mutation tier) is enough to toggle and delete a schedule
// without create scope — matching ArchiveTask/DeleteEvent.
func TestSchedule_MutationsRequireWrite(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *"})
	writer := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(authscope.OpTaskWrite),
	})

	// Act
	_, errEnable := writer.Client.SetScheduleEnabled(t.Context(), &gritzv1.SetScheduleEnabledRequest{
		Id:      sched.Id,
		Enabled: false,
	})
	_, errDelete := writer.Client.DeleteSchedule(t.Context(), &gritzv1.DeleteScheduleRequest{Id: sched.Id})

	// Assert
	assert.NilError(t, errEnable)
	assert.NilError(t, errDelete)
	_, err := h.Org.Client.GetSchedule(t.Context(), &gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.ErrorContains(t, err, "not found")
}

func TestSetScheduleEnabled(t *testing.T) {
	t.Parallel()
	// Arrange - start disabled: no next fire time.
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *", Enabled: false})
	assert.Assert(t, sched.NextRunAt == nil)

	// Act & Assert - enabling recomputes next_run_at from now.
	enabled, err := h.Org.Client.SetScheduleEnabled(t.Context(), &gritzv1.SetScheduleEnabledRequest{
		Id:      sched.Id,
		Enabled: true,
	})
	assert.NilError(t, err)
	assert.Assert(t, enabled.Schedule.Enabled)
	assert.Assert(t, enabled.Schedule.NextRunAt != nil)

	// Act & Assert - disabling clears it so the claim query skips the row.
	disabled, err := h.Org.Client.SetScheduleEnabled(t.Context(), &gritzv1.SetScheduleEnabledRequest{
		Id:      sched.Id,
		Enabled: false,
	})
	assert.NilError(t, err)
	assert.Assert(t, !disabled.Schedule.Enabled)
	assert.Assert(t, disabled.Schedule.NextRunAt == nil)
}

func TestDeleteSchedule(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *"})

	// Act
	_, err := h.Org.Client.DeleteSchedule(t.Context(), &gritzv1.DeleteScheduleRequest{Id: sched.Id})

	// Assert
	assert.NilError(t, err)
	_, err = h.Org.Client.GetSchedule(t.Context(), &gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.ErrorContains(t, err, "not found")
}

func TestUpdateSchedule(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{
		Name:     "original",
		CronExpr: "0 9 * * *",
		Timezone: "UTC",
		Enabled:  true,
	})
	firstNext := sched.NextRunAt.AsTime()

	// Act - rename and move the spec to a new time; next_run_at recomputes.
	updated, err := h.Org.Client.UpdateSchedule(t.Context(), &gritzv1.UpdateScheduleRequest{
		Id:        sched.Id,
		Name:      "renamed",
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		CronExpr:  "30 2 * * *",
		Timezone:  "UTC",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, updated.Schedule.Name, "renamed")
	assert.Equal(t, updated.Schedule.CronExpr, "30 2 * * *")
	assert.Assert(t, updated.Schedule.NextRunAt != nil)
	assert.Assert(t, !updated.Schedule.NextRunAt.AsTime().Equal(firstNext))
	// Persisted.
	got := h.Org.GetSchedule(&gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.Equal(t, got.Name, "renamed")
	assert.Equal(t, got.CronExpr, "30 2 * * *")
}

func TestUpdateSchedule_InvalidCron(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *", Timezone: "UTC"})

	// Act
	_, err := h.Org.Client.UpdateSchedule(t.Context(), &gritzv1.UpdateScheduleRequest{
		Id:        sched.Id,
		Workspace: e2e.DefaultWorkspace,
		Runner:    e2e.DefaultRunner,
		CronExpr:  "nonsense",
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	// The bad update never landed: the stored spec is unchanged.
	got := h.Org.GetSchedule(&gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.Equal(t, got.CronExpr, "0 9 * * *")
}

// A manual run fires the schedule as a one-off: it creates exactly one task
// carrying the template, seeds the ScheduleActor created-event + one instruction
// event per instruction (byte-for-byte what the scheduler worker fires), and
// records nothing on the schedule row — next_run_at/last_run_at/last_task_id all
// stay untouched so the cron cadence never advances.
func TestRunSchedule(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	before := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{
		Name:      "nightly",
		Namespace: "reviewbot",
		Instructions: []*gritzv1.Instruction{
			{Text: "bump deps", Url: "https://example.com/deps"},
			{Text: "groom changelog"},
		},
		CronExpr: "0 9 * * *",
		Timezone: "UTC",
		Enabled:  true,
	})
	// An enabled schedule has a next_run_at; it never ran, so
	// last_run_at/last_task_id are unset.
	assert.Assert(t, before.NextRunAt != nil)
	assert.Assert(t, before.LastRunAt == nil)
	assert.Equal(t, before.LastTaskId, int64(0))

	// Act
	resp, err := h.Org.Client.RunSchedule(t.Context(), &gritzv1.RunScheduleRequest{Id: before.Id})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, resp.Task != nil)
	assert.Assert(t, resp.Task.Id != 0)
	// Exactly one task was created for the org, and it is the one returned.
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Assert(t, cmp.Len(tasks, 1))
	assert.Equal(t, tasks[0].Id, resp.Task.Id)
	assert.Equal(t, tasks[0].Name, "nightly")
	assert.Equal(t, tasks[0].Workspace, e2e.DefaultWorkspace)
	assert.Equal(t, tasks[0].Runner, e2e.DefaultRunner)
	// The created event first (ScheduleActor) and the start command, then one
	// wake instruction event per template instruction — identical to a scheduled
	// fire. The server-assigned id, task_id and created_at are ignored.
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: resp.Task.Id})
	want := []*gritzv1.Event{
		{Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{
			Kind:     gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED,
			Actor:    &gritzv1.Actor{Kind: model.ActorKindSchedule},
			ToStatus: model.TaskStatusPending.Label(),
		}}},
		{Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		}}},
		{
			Wake: true,
			Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "bump deps",
				Url:  "https://example.com/deps",
			}},
		},
		{
			Wake: true,
			Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "groom changelog",
			}},
		},
	}
	assert.DeepEqual(t, events, want, protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "task_id", "created_at"))
	// The schedule row is untouched: the cron cadence never advanced and no run was
	// recorded on it. created_at/updated_at are ignored — the create response carries
	// the in-memory time at full precision, while the read-back is truncated to
	// Postgres's microsecond resolution (same as TestCreateSchedule).
	after := h.Org.GetSchedule(&gritzv1.GetScheduleRequest{Id: before.Id})
	assert.DeepEqual(t, after, before, protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Schedule{}, "created_at", "updated_at"))
}

// A manual run works on a disabled schedule — disabled only means "don't fire
// automatically," and testing a not-yet-enabled schedule is the primary use case.
// The row stays disabled with next_run_at unset afterward.
func TestRunSchedule_DisabledSchedule(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{
		Name:         "inert",
		Instructions: []*gritzv1.Instruction{{Text: "smoke test"}},
		CronExpr:     "0 9 * * *",
		Enabled:      false,
	})
	assert.Assert(t, sched.NextRunAt == nil)

	// Act
	resp, err := h.Org.Client.RunSchedule(t.Context(), &gritzv1.RunScheduleRequest{Id: sched.Id})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, resp.Task != nil)
	// Running it never re-enabled the row or gave it a next fire time.
	after := h.Org.GetSchedule(&gritzv1.GetScheduleRequest{Id: sched.Id})
	assert.Assert(t, !after.Enabled)
	assert.Assert(t, after.NextRunAt == nil)
	assert.Assert(t, after.LastRunAt == nil)
	assert.Equal(t, after.LastTaskId, int64(0))
}

// A manual run materializes a real task on the schedule's target, so it demands
// the same task-create scope CreateSchedule/UpdateSchedule require — the
// task-write mutation tier (enough to enable/disable) is not sufficient. The
// target is read from the schedule row, so it can't be spoofed by the request.
func TestRunSchedule_RequiresCreateScope(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	sched := h.Org.CreateSchedule(&gritzv1.CreateScheduleRequest{CronExpr: "0 9 * * *"})
	writer := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(authscope.OpTaskWrite),
	})
	wrongTarget := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(
			authscope.OpTaskCreate,
			authscope.WithTaskWorkspace("other-workspace"),
			authscope.WithTaskRunner(e2e.DefaultRunner),
			authscope.WithTaskArchived(false),
		),
	})
	creator := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(
			authscope.OpTaskCreate,
			authscope.WithTaskWorkspace(e2e.DefaultWorkspace),
			authscope.WithTaskRunner(e2e.DefaultRunner),
			authscope.WithTaskArchived(false),
		),
	})
	req := &gritzv1.RunScheduleRequest{Id: sched.Id}

	// Act & Assert - task-write (the tier that toggles/deletes a schedule) is
	// not enough to fire it.
	_, err := writer.Client.RunSchedule(t.Context(), req)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// Act & Assert - create scope for a different target does not grant a run
	// on this one.
	_, err = wrongTarget.Client.RunSchedule(t.Context(), req)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// Act & Assert - create scope on the schedule's own (workspace, runner) may
	// run it.
	_, err = creator.Client.RunSchedule(t.Context(), req)
	assert.NilError(t, err)
}

func TestRunSchedule_NotFound(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.RunSchedule(t.Context(), &gritzv1.RunScheduleRequest{Id: 999})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}
