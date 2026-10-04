package apiserver

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateSchedule(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Name:      "nightly",
		Workspace: "test-workspace",
		Runner:    "test-runner",
		Namespace: "reviewbot",
		Instructions: []*gritzv1.Instruction{
			{Text: "bump deps", Url: "https://example.com/deps"},
		},
		CronExpr:    "0 9 * * *",
		Timezone:    "America/Toronto",
		Enabled:     true,
		AutoArchive: durationpb.New(time.Hour),
	})
	assert.NilError(t, err)

	// An enabled schedule gets a first fire time computed server-side.
	assert.Assert(t, resp.Schedule.NextRunAt != nil)

	expected := &gritzv1.Schedule{
		Id:        resp.Schedule.Id,
		Name:      "nightly",
		Workspace: "test-workspace",
		Runner:    "test-runner",
		Namespace: "reviewbot",
		Instructions: []*gritzv1.Instruction{
			{Text: "bump deps", Url: "https://example.com/deps"},
		},
		CronExpr:    "0 9 * * *",
		Timezone:    "America/Toronto",
		Enabled:     true,
		AutoArchive: durationpb.New(time.Hour),
		CreatedBy:   org.UserID,
		NextRunAt:   resp.Schedule.NextRunAt, // nondeterministic (computed from now)
		CreatedAt:   resp.Schedule.CreatedAt,
		UpdatedAt:   resp.Schedule.UpdatedAt,
	}
	assert.DeepEqual(t, resp.Schedule, expected, protocmp.Transform())

	// It round-trips through Get. created_at/updated_at are ignored: the create
	// response carries the in-memory time at full precision, while the DB read-back
	// is truncated to Postgres's microsecond resolution.
	getResp, err := srv.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: resp.Schedule.Id})
	assert.NilError(t, err)
	assert.DeepEqual(t, getResp.Schedule, resp.Schedule, protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Schedule{}, "created_at", "updated_at"))
}

func TestCreateSchedule_DisabledHasNoNextRun(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace",
		Runner:    "test-runner",
		CronExpr:  "0 9 * * *",
		Enabled:   false,
	})
	assert.NilError(t, err)

	// A disabled schedule stays out of the claim query: no next fire time.
	assert.Assert(t, resp.Schedule.NextRunAt == nil)
	assert.Assert(t, !resp.Schedule.Enabled)
}

func TestCreateSchedule_DefaultTimezone(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// An empty timezone defaults to UTC.
	resp, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace",
		Runner:    "test-runner",
		CronExpr:  "0 9 * * *",
	})
	assert.NilError(t, err)
	assert.Equal(t, resp.Schedule.Timezone, "UTC")
}

func TestCreateSchedule_InvalidCron(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace",
		Runner:    "test-runner",
		CronExpr:  "not a cron",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)

	// The invalid schedule was never stored.
	list, err := srv.ListSchedules(ctx, &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(list.Schedules, 0))
}

func TestCreateSchedule_InvalidTimezone(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace",
		Runner:    "test-runner",
		CronExpr:  "0 9 * * *",
		Timezone:  "Mars/Phobos",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestCreateSchedule_BadWorkspace(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "fake-workspace",
		Runner:    "test-runner",
		CronExpr:  "0 9 * * *",
	})
	assert.ErrorContains(t, err, "not found")
}

func TestCreateSchedule_RequiresCreateScope(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})

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

func TestListSchedules(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{Name: "a", Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *"})
	assert.NilError(t, err)
	_, err = srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{Name: "b", Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 10 * * *"})
	assert.NilError(t, err)

	list, err := srv.ListSchedules(ctx, &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(list.Schedules, 2))

	// List is org-scoped: another org sees none.
	other := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	otherList, err := srv.ListSchedules(createCtx(t, other), &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(otherList.Schedules, 0))
}

func TestGetSchedule_Permissions(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)

	resp, err := srv.CreateSchedule(ctxA, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.NilError(t, err)

	// Its own org reads it; another org gets not-found (org-scoped).
	_, errA := srv.GetSchedule(ctxA, &gritzv1.GetScheduleRequest{Id: resp.Schedule.Id})
	_, errB := srv.GetSchedule(ctxB, &gritzv1.GetScheduleRequest{Id: resp.Schedule.Id})
	assert.NilError(t, errA)
	assert.ErrorContains(t, errB, "not found")
}

// A member holding only task-read (no write/create scope) may list and get, but
// read scope is not enough to mutate: delete and enable require task-write.
func TestSchedule_ReadIsMembershipOnly(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})

	created, err := srv.CreateSchedule(createCtx(t, org), &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.NilError(t, err)
	reader := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskRead)})

	// Read scope covers get and list.
	_, err = srv.GetSchedule(reader, &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	list, err := srv.ListSchedules(reader, &gritzv1.ListSchedulesRequest{})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(list.Schedules, 1))

	// ...but not the mutations.
	_, err = srv.SetScheduleEnabled(reader, &gritzv1.SetScheduleEnabledRequest{Id: created.Schedule.Id, Enabled: true})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
	_, err = srv.DeleteSchedule(reader, &gritzv1.DeleteScheduleRequest{Id: created.Schedule.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// task-write (the mutation tier) is enough to toggle and delete a schedule
// without create scope — matching ArchiveTask/DeleteEvent.
func TestSchedule_MutationsRequireWrite(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})

	created, err := srv.CreateSchedule(createCtx(t, org), &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.NilError(t, err)
	writer := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskWrite)})

	_, err = srv.SetScheduleEnabled(writer, &gritzv1.SetScheduleEnabledRequest{Id: created.Schedule.Id, Enabled: false})
	assert.NilError(t, err)
	_, err = srv.DeleteSchedule(writer, &gritzv1.DeleteScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)

	_, err = srv.GetSchedule(createCtx(t, org), &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.ErrorContains(t, err, "not found")
}

func TestSetScheduleEnabled(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Start disabled — no next fire time.
	created, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *", Enabled: false,
	})
	assert.NilError(t, err)
	assert.Assert(t, created.Schedule.NextRunAt == nil)

	// Enabling recomputes next_run_at from now.
	enabled, err := srv.SetScheduleEnabled(ctx, &gritzv1.SetScheduleEnabledRequest{Id: created.Schedule.Id, Enabled: true})
	assert.NilError(t, err)
	assert.Assert(t, enabled.Schedule.Enabled)
	assert.Assert(t, enabled.Schedule.NextRunAt != nil)

	// Disabling clears it so the claim query skips the row.
	disabled, err := srv.SetScheduleEnabled(ctx, &gritzv1.SetScheduleEnabledRequest{Id: created.Schedule.Id, Enabled: false})
	assert.NilError(t, err)
	assert.Assert(t, !disabled.Schedule.Enabled)
	assert.Assert(t, disabled.Schedule.NextRunAt == nil)
}

func TestDeleteSchedule(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	created, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.NilError(t, err)

	_, err = srv.DeleteSchedule(ctx, &gritzv1.DeleteScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)

	_, err = srv.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.ErrorContains(t, err, "not found")
}

func TestUpdateSchedule(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	created, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Name: "original", Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true,
	})
	assert.NilError(t, err)
	firstNext := created.Schedule.NextRunAt.AsTime()

	// Rename and move the spec to a new time; next_run_at recomputes.
	updated, err := srv.UpdateSchedule(ctx, &gritzv1.UpdateScheduleRequest{
		Id: created.Schedule.Id, Name: "renamed", Workspace: "test-workspace", Runner: "test-runner", CronExpr: "30 2 * * *", Timezone: "UTC",
	})
	assert.NilError(t, err)
	assert.Equal(t, updated.Schedule.Name, "renamed")
	assert.Equal(t, updated.Schedule.CronExpr, "30 2 * * *")
	assert.Assert(t, updated.Schedule.NextRunAt != nil)
	assert.Assert(t, !updated.Schedule.NextRunAt.AsTime().Equal(firstNext))

	// Persisted.
	getResp, err := srv.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Schedule.Name, "renamed")
	assert.Equal(t, getResp.Schedule.CronExpr, "30 2 * * *")
}

func TestUpdateSchedule_InvalidCron(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	created, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *", Timezone: "UTC",
	})
	assert.NilError(t, err)

	_, err = srv.UpdateSchedule(ctx, &gritzv1.UpdateScheduleRequest{
		Id: created.Schedule.Id, Workspace: "test-workspace", Runner: "test-runner", CronExpr: "nonsense",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)

	// The bad update never landed: the stored spec is unchanged.
	getResp, err := srv.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Schedule.CronExpr, "0 9 * * *")
}

// A manual run fires the schedule as a one-off: it creates exactly one task
// carrying the template, seeds the ScheduleActor created-event + one instruction
// event per instruction (byte-for-byte what the scheduler worker fires), and
// records nothing on the schedule row — next_run_at/last_run_at/last_task_id all
// stay untouched so the cron cadence never advances.
func TestRunSchedule(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	created, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Name:      "nightly",
		Workspace: "test-workspace",
		Runner:    "test-runner",
		Namespace: "reviewbot",
		Instructions: []*gritzv1.Instruction{
			{Text: "bump deps", Url: "https://example.com/deps"},
			{Text: "groom changelog"},
		},
		CronExpr: "0 9 * * *",
		Timezone: "UTC",
		Enabled:  true,
	})
	assert.NilError(t, err)

	// Snapshot the cadence columns before the manual run so we can prove they are
	// left untouched. An enabled schedule has a next_run_at; it never ran, so
	// last_run_at/last_task_id are unset.
	before := created.Schedule
	assert.Assert(t, before.NextRunAt != nil)
	assert.Assert(t, before.LastRunAt == nil)
	assert.Equal(t, before.LastTaskId, int64(0))

	resp, err := srv.RunSchedule(ctx, &gritzv1.RunScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	assert.Assert(t, resp.Task != nil)
	assert.Assert(t, resp.Task.Id != 0)

	// Exactly one task was created for the org, and it is the one returned.
	tasks, err := srv.store.ListTasks(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tasks, 1))
	assert.Equal(t, tasks[0].ID, resp.Task.Id)
	assert.Equal(t, tasks[0].Name, "nightly")
	assert.Equal(t, tasks[0].Workspace, "test-workspace")
	assert.Equal(t, tasks[0].Runner, "test-runner")

	// The created event first (ScheduleActor), then one wake instruction event per
	// template instruction — identical to a scheduled fire. Compare only Wake +
	// Payload; the DB-assigned id/task_id/org_id and timestamp vary per run.
	events, err := srv.store.ListEventsByTask(ctx, nil, resp.Task.Id, org.OrgID, nil)
	assert.NilError(t, err)
	want := []*model.Event{
		{Payload: &model.LifecyclePayload{
			Kind:     model.LifecycleKindCreated,
			Actor:    model.ScheduleActor,
			ToStatus: model.TaskStatusPending.Label(),
		}},
		{Wake: true, Payload: &model.InstructionPayload{Text: "bump deps", URL: "https://example.com/deps"}},
		{Wake: true, Payload: &model.InstructionPayload{Text: "groom changelog"}},
	}
	assert.Assert(t, cmp.Len(events, len(want)))
	for i := range want {
		assert.DeepEqual(t, events[i], want[i], cmpx.OnlyFields("Wake", "Payload"))
	}

	// The schedule row is untouched: the cron cadence never advanced and no run was
	// recorded on it. created_at/updated_at are ignored — the create response carries
	// the in-memory time at full precision, while the read-back is truncated to
	// Postgres's microsecond resolution (same as TestCreateSchedule).
	after, err := srv.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	assert.DeepEqual(t, after.Schedule, before, protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Schedule{}, "created_at", "updated_at"))
}

// A manual run works on a disabled schedule — disabled only means "don't fire
// automatically," and testing a not-yet-enabled schedule is the primary use case.
// The row stays disabled with next_run_at unset afterward.
func TestRunSchedule_DisabledSchedule(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	created, err := srv.CreateSchedule(ctx, &gritzv1.CreateScheduleRequest{
		Name:         "inert",
		Workspace:    "test-workspace",
		Runner:       "test-runner",
		Instructions: []*gritzv1.Instruction{{Text: "smoke test"}},
		CronExpr:     "0 9 * * *",
		Enabled:      false,
	})
	assert.NilError(t, err)
	assert.Assert(t, created.Schedule.NextRunAt == nil)

	resp, err := srv.RunSchedule(ctx, &gritzv1.RunScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	assert.Assert(t, resp.Task != nil)

	// Running it never re-enabled the row or gave it a next fire time.
	after, err := srv.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
	assert.Assert(t, !after.Schedule.Enabled)
	assert.Assert(t, after.Schedule.NextRunAt == nil)
	assert.Assert(t, after.Schedule.LastRunAt == nil)
	assert.Equal(t, after.Schedule.LastTaskId, int64(0))
}

// A manual run materializes a real task on the schedule's target, so it demands
// the same task-create scope CreateSchedule/UpdateSchedule require — the
// task-write mutation tier (enough to enable/disable) is not sufficient. The
// target is read from the schedule row, so it can't be spoofed by the request.
func TestRunSchedule_RequiresCreateScope(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})

	created, err := srv.CreateSchedule(createCtx(t, org), &gritzv1.CreateScheduleRequest{
		Workspace: "test-workspace", Runner: "test-runner", CronExpr: "0 9 * * *",
	})
	assert.NilError(t, err)

	// task-write (the tier that toggles/deletes a schedule) is not enough to fire it.
	writer := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskWrite)})
	_, err = srv.RunSchedule(writer, &gritzv1.RunScheduleRequest{Id: created.Schedule.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// Create scope for a different target does not grant a run on this one.
	wrongTarget := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace("other-workspace"),
		authscope.WithTaskRunner("test-runner"),
		authscope.WithTaskArchived(false),
	)})
	_, err = srv.RunSchedule(wrongTarget, &gritzv1.RunScheduleRequest{Id: created.Schedule.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// Create scope on the schedule's own (workspace, runner) may run it.
	creator := scopedCtx(t, org, authscope.Scopes{authscope.New(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace("test-workspace"),
		authscope.WithTaskRunner("test-runner"),
		authscope.WithTaskArchived(false),
	)})
	_, err = srv.RunSchedule(creator, &gritzv1.RunScheduleRequest{Id: created.Schedule.Id})
	assert.NilError(t, err)
}

func TestRunSchedule_NotFound(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.RunSchedule(ctx, &gritzv1.RunScheduleRequest{Id: 999})
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}
