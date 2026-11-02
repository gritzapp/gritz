package scenario_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestUnreportedRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)
	// The driver's client doesn't retry a CodeInternal error; the runner's
	// outbox does.
	m := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(context.Context, *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return nil, connect.NewError(connect.CodeInternal, errors.New("e2e: rejected"))
		},
	}
	h.Server.Mock(m)

	run1.Complete()
	events := m.RunnerEventWaiter(t)
	events.Wait(task.ID, model.RunnerEventStopped) // the driver's report, rejected
	events.Wait(task.ID, model.RunnerEventFailed)  // then the runner's backstop
	h.Server.Mock(nil)

	// The driver went idle with its run unreported, the runner reported it
	// failed, and the outbox delivered that once the real method answered.
	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}

func TestSandboxFails_Create(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{Concurrency: 1}})
	m := &backend.BackendMock{
		CreateFunc: func(context.Context, *backend.Spec) (backend.Sandbox, error) {
			return nil, errors.New("e2e: create failed")
		},
	}
	h.Backend.Mock(m)

	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})

	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	// The mock fails every Create: remove it so the next tasks get sandboxes.
	h.Backend.Mock(nil)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := testx.FindFunc(t, lifecycle, func(l *gritzv1.LifecyclePayload, _ int) bool {
		return l.Kind == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.Assert(t, cmp.Contains(failed.Message, "e2e: create failed"))
	assert.Assert(t, cmp.Len(m.CreateCalls(), 1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))

	// The slot was released: a second task runs.
	other := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the other thing"}},
	})
	h.Agent.WaitRun(other.ID).Complete()
	other.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	// Started again, the first task runs on a real sandbox.
	task.Update(&gritzv1.UpdateTaskRequest{Start: true})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
}

func TestSandboxFails_Boot(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{Concurrency: 1}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
		Draft:        true,
	})
	m := &backend.SandboxMock{
		BootFunc: func(context.Context) error {
			return errors.New("e2e: boot failed")
		},
	}
	h.Backend.MockSandbox(task.ID, m)

	task.Update(&gritzv1.UpdateTaskRequest{Start: true})

	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	// The mock fails every Boot of the task's sandbox: remove it so the task
	// can run when started again.
	h.Backend.MockSandbox(task.ID, nil)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := testx.FindFunc(t, lifecycle, func(l *gritzv1.LifecyclePayload, _ int) bool {
		return l.Kind == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.Assert(t, cmp.Contains(failed.Message, "e2e: boot failed"))
	assert.Assert(t, cmp.Len(m.BootCalls(), 1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))

	// The slot was released: a second task runs.
	other := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the other thing"}},
	})
	h.Agent.WaitRun(other.ID).Complete()
	other.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	// Its sandbox was recorded before the boot that failed, so a restarted
	// runner still has it: started again, the first task boots that sandbox
	// for real.
	h.Runner.Restart()
	task.Update(&gritzv1.UpdateTaskRequest{Start: true})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
}

func TestDriverUnreachable(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
		Draft:        true,
	})
	m := &backend.SandboxMock{
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return nil, errors.New("e2e: dial failed")
		},
	}
	h.Backend.MockSandbox(task.ID, m)

	task.Update(&gritzv1.UpdateTaskRequest{Start: true})

	// Failed once the runner's ReadyTimeout passed.
	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := testx.FindFunc(t, lifecycle, func(l *gritzv1.LifecyclePayload, _ int) bool {
		return l.Kind == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.Assert(t, cmp.Contains(failed.Message, "e2e: dial failed"))
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)
	assert.Assert(t, len(m.DialCalls()) > 0)
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))
}

func TestDriverDies_MidRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	h.Backend.KillDriver(task.ID)

	run1.WaitCancelled()
	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)

	// A wake boots the sandbox again, and its new driver acts on the start.
	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "more"}},
	})
	run2 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run2.Prompt, "more"))
	run2.Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 2))
}

func TestSandboxGone_MidRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	h.Backend.DestroySandbox(task.ID)

	// The runner found the sandbox gone under its driver, rather than waiting
	// out the driver, and reported the run failed without starting it again.
	run1.WaitCancelled()
	got := task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := slices.DeleteFunc(lifecycle, func(lc *gritzv1.LifecyclePayload) bool {
		return lc.GetKind() != gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.DeepEqual(t, testx.ExtractField(failed, "Message"), []string{"sandbox gone"})
}

func TestSandboxGone_Suspended(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)
	h.Backend.DestroySandbox(task.ID)

	// A wake finds the sandbox gone when booting it, and fails the run
	// without creating a new one.
	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "more"}},
	})
	got := task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := slices.DeleteFunc(lifecycle, func(lc *gritzv1.LifecyclePayload) bool {
		return lc.GetKind() != gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.DeepEqual(t, testx.ExtractField(failed, "Message"), []string{backend.ErrGone.Error()})

	// The next wake gets a fresh sandbox.
	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "again"}},
	})
	run3 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run3.Prompt, "again"))
	run3.Complete()
	got = task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(3))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 2))
}

func TestRunDropped(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	// The first drop cuts the stream the run was started on, the second the
	// one the runner reattached with; it reopens both.
	h.Backend.CloseRunStream(task.ID)
	h.Backend.CloseRunStream(task.ID)
	task.Cancel()

	// The driver kept going, got the stop on the reopened stream, and stopped
	// run 1.
	run1.WaitCancelled()
	got := task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}
