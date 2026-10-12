package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// testTaskRunner returns the TaskRunner of run version of taskID on sb,
// registered with a test manager as Load does, along with the manager's
// client and outbox.
func testTaskRunner(t *testing.T, taskID, version int64, sb backend.Sandbox) (*TaskRunner, *TaskManager, *gritzclient.ClientMock, *outbox.Outbox[model.RunnerEvent]) {
	t.Helper()
	m, mock, queue := testManager(t, &backend.BackendMock{}, testStore(t))
	tr := track(t, m, taskstate.Record{TaskID: taskID, Version: version, Type: "docker", ID: "c1"}, sb)
	return tr, m, mock, queue
}

// runAsync calls tr's Run on its own goroutine, and returns the channel its
// error is sent on.
func runAsync(ctx context.Context, tr *TaskRunner, spec *driverv1.RunSpec) <-chan error {
	done := make(chan error, 1)
	tr.claim()
	go func() { done <- tr.Run(ctx, spec) }()
	return done
}

// waitDone waits for the error of a Run started with runAsync.
func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestTaskRunnerRun_Finished(t *testing.T) {
	t.Parallel()
	// Arrange - a run that finishes on its own; the test's own stream is
	// closed, which detaches it and leaves the run to Run.
	client, stream := testDriver(t, 1, 3, &agent.Config{Type: agent.TypeDummy})
	assert.NilError(t, stream.Close())
	sb, parked := testSandbox(client)
	tr, _, _, queue := testTaskRunner(t, 1, 3, sb)

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - the driver reported, so no event is owed; the sandbox is parked
	// after Finished.
	assert.NilError(t, err)
	select {
	case <-parked:
	default:
		t.Fatal("sandbox was not parked after Finished")
	}
	assert.Assert(t, cmp.Len(sb.ParkCalls(), 1))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	waitIdle(t, tr)
}

func TestTaskRunnerRun_Reattach(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going until it is stopped.
	hs, client, stream := serveDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, parked := testSandbox(client)
	tr, _, _, queue := testTaskRunner(t, 1, 3, sb)
	done := runAsync(t.Context(), tr, nil)
	waitDialed(t, sb, 1)

	// Act - drop every connection to the driver, the attached stream with
	// them, until Run has dialed again; then stop the run.
	dials := len(sb.DialCalls())
	deadline := time.Now().Add(10 * time.Second)
	for len(sb.DialCalls()) == dials {
		if time.Now().After(deadline) {
			t.Fatal("Run did not reattach")
		}
		hs.CloseClientConnections()
		time.Sleep(5 * time.Millisecond)
	}
	stopRun(t, client, 3)

	// Assert - the reattached stream saw Finished: the driver reported, the
	// sandbox is parked, and no event is owed.
	assert.NilError(t, waitDone(t, done))
	<-parked
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestTaskRunnerRun_DriverUnreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	hs := httptest.NewServer(http.NotFoundHandler())
	hs.Close()
	sb, parked := testSandbox(driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL))
	tr, m, mock, queue := testTaskRunner(t, 2, 4, sb)
	m.env.lostTimeout = 100 * time.Millisecond

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - Run gave up: the sandbox is parked, and the runner's fallback
	// "failed" is scoped to the run's version.
	assert.NilError(t, err)
	<-parked
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 2, Version: 4, Reason: "driver unreachable"},
	}, protocmp.Transform())
}

func TestTaskRunnerRun_NoSuchRun(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, so attaching to version 3 is
	// NotFound.
	client, _ := testDriver(t, 1, 3, nil)
	sb, parked := testSandbox(client)
	tr, _, mock, queue := testTaskRunner(t, 1, 3, sb)

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - nothing runs on the sandbox, so it is parked and the run is
	// reported failed.
	assert.NilError(t, err)
	<-parked
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "driver has no such run"},
	}, protocmp.Transform())
}

// TestTaskRunnerRun_NotReady covers a run started on a sandbox whose driver
// never comes up: the run is reported failed and the sandbox parked.
func TestTaskRunnerRun_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	_, client, _ := newDriver(t, 1, make(chan struct{}))
	sb, parked := testSandbox(client)
	tr, m, mock, queue := testTaskRunner(t, 1, 1, sb)
	m.env.readyTimeout = 100 * time.Millisecond

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert
	assert.NilError(t, err)
	<-parked
	events := submitted(t, mock, queue)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
}

func TestTaskRunnerRun_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client, stream := testDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, _ := testSandbox(client)
	tr, _, _, queue := testTaskRunner(t, 1, 3, sb)
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, tr, nil)
	waitDialed(t, sb, 1)

	// Act - the runner shuts down.
	cancel()

	// Assert - Run returns ctx's error, so the manager keeps the slot, and the
	// sandbox stays up for next-boot rehydration: no park, no event.
	assert.ErrorIs(t, waitDone(t, done), context.Canceled)
	assert.Assert(t, cmp.Len(sb.ParkCalls(), 0))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	waitIdle(t, tr)
}

func TestTaskRunnerRun_Destroyed(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, on a recorded sandbox.
	client, stream := testDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, _ := testSandbox(client)
	tr, m, mock, queue := testTaskRunner(t, 1, 3, sb)
	done := runAsync(t.Context(), tr, nil)
	waitDialed(t, sb, 1)

	// Act - the sandbox is destroyed under the run.
	assert.NilError(t, tr.Destroy(t.Context()))

	// Assert - Run is not shutting down, so it returns no error; the run is
	// lost, and the task's record and TaskRunner are gone.
	assert.NilError(t, waitDone(t, done))
	assert.Assert(t, cmp.Len(sb.DestroyCalls(), 1))
	assertTracked(t, m, 1, nil)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
}

// TestTaskRunnerRun_DestroyedBeforeRun covers a TaskRunner destroyed after
// Start claimed it but before its Run began: the run is lost without being
// started.
func TestTaskRunnerRun_DestroyedBeforeRun(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox whose driver can only be dialed while the run's
	// context is live.
	sb := &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{Type: "docker", ID: "c1"} },
		StartFunc:  func(context.Context) error { return nil },
		DialFunc: func(ctx context.Context, _ string) (driverv1connect.DriverServiceClient, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		DestroyFunc: func(context.Context) error { return nil },
	}
	tr, m, mock, queue := testTaskRunner(t, 1, 3, sb)
	assert.NilError(t, tr.Start(t.Context(), 4))
	assert.NilError(t, tr.Destroy(t.Context()))

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert - Run returned without waiting for the driver: its context was
	// cancelled from the start.
	assert.NilError(t, err)
	assertTracked(t, m, 1, nil)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 4, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
}

// TestTaskRunnerStart covers starting runs on a TaskRunner: Start stamps the
// record with the run's version, and refuses a second run while the first is
// claimed.
func TestTaskRunnerStart(t *testing.T) {
	t.Parallel()
	// Arrange
	sb := &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{Type: "docker", ID: "c1"} },
		StartFunc:  func(context.Context) error { return nil },
	}
	tr, m, _, _ := testTaskRunner(t, 1, 3, sb)

	// Act
	err := tr.Start(t.Context(), 4)
	again := tr.Start(t.Context(), 5)

	// Assert
	assert.NilError(t, err)
	assert.ErrorIs(t, again, errRunning)
	assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
	assertTracked(t, m, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "docker", ID: "c1"})
}
