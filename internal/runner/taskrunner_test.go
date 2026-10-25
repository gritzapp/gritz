package runner

import (
	"context"
	"errors"
	"log/slog"
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
// tracked by a test manager as Load does, along with the manager's
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
	claim(tr)
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

func TestNewTaskRunner(t *testing.T) {
	be := &backend.BackendMock{}
	events := make(chan TaskEvent)
	log := slog.New(slog.DiscardHandler)
	tests := []struct {
		name      string
		opts      func(store *taskstate.Store) TaskRunnerOptions
		wantPanic bool
		want      func(store *taskstate.Store) TaskRunnerOptions
	}{
		{
			name: "no backend",
			opts: func(store *taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{TaskID: 1, Store: store, Events: events}
			},
			wantPanic: true,
		},
		{
			name: "no store",
			opts: func(*taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{TaskID: 1, Backend: be, Events: events}
			},
			wantPanic: true,
		},
		{
			name: "no events",
			opts: func(store *taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{TaskID: 1, Backend: be, Store: store}
			},
			wantPanic: true,
		},
		{
			name: "defaults",
			opts: func(store *taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{TaskID: 1, Backend: be, Store: store, Events: events}
			},
			want: func(store *taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{
					TaskID:           1,
					Backend:          be,
					Store:            store,
					Events:           events,
					Log:              slog.Default(),
					RetryInterval:    250 * time.Millisecond,
					RetryMaxInterval: 10 * time.Second,
					LostTimeout:      2 * time.Minute,
					ReadyTimeout:     time.Minute,
				}
			},
		},
		{
			name: "explicit",
			opts: func(store *taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{
					TaskID:           1,
					Backend:          be,
					Store:            store,
					Events:           events,
					Log:              log,
					RetryInterval:    time.Millisecond,
					RetryMaxInterval: 2 * time.Millisecond,
					LostTimeout:      3 * time.Millisecond,
					ReadyTimeout:     4 * time.Millisecond,
				}
			},
			want: func(store *taskstate.Store) TaskRunnerOptions {
				return TaskRunnerOptions{
					TaskID:           1,
					Backend:          be,
					Store:            store,
					Events:           events,
					Log:              log,
					RetryInterval:    time.Millisecond,
					RetryMaxInterval: 2 * time.Millisecond,
					LostTimeout:      3 * time.Millisecond,
					ReadyTimeout:     4 * time.Millisecond,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := testStore(t)
			opts := tt.opts(store)

			// Act & Assert
			if tt.wantPanic {
				assert.Assert(t, cmp.Panics(func() { NewTaskRunner(opts) }))
				return
			}
			tr := NewTaskRunner(opts)
			assert.Assert(t, tr.opts == tt.want(store), "got %+v", tr.opts)
			assert.Assert(t, tr.sbx == nil)
			assert.Equal(t, tr.Version(), int64(0))
			// No side effects: no record written, no sandbox touched.
			recs, err := store.List()
			assert.NilError(t, err)
			assert.Assert(t, cmp.Len(recs, 0))
			assert.Assert(t, cmp.Len(be.CreateCalls(), 0))
			assert.Assert(t, cmp.Len(be.OpenCalls(), 0))
		})
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
	m, mock, queue := testManager(t, &backend.BackendMock{}, testStore(t), func(opts *TaskRunnerOptions) {
		opts.LostTimeout = 100 * time.Millisecond
	})
	tr := track(t, m, taskstate.Record{TaskID: 2, Version: 4, Type: "docker", ID: "c1"}, sb)

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - Run gave up: the sandbox is parked, and the runner's fallback
	// "failed" is scoped to the run's version.
	assert.NilError(t, err)
	<-parked
	waitQueued(t, queue, 1)
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
	waitQueued(t, queue, 1)
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
	m, mock, queue := testManager(t, &backend.BackendMock{}, testStore(t), func(opts *TaskRunnerOptions) {
		opts.ReadyTimeout = 100 * time.Millisecond
	})
	tr := track(t, m, taskstate.Record{TaskID: 1, Version: 1, Type: "docker", ID: "c1"}, sb)

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert
	assert.NilError(t, err)
	<-parked
	waitQueued(t, queue, 1)
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
	// lost, and the task's record and sandbox are gone.
	assert.NilError(t, waitDone(t, done))
	assert.Assert(t, cmp.Len(sb.DestroyCalls(), 1))
	assertTracked(t, m, 1, nil)
	waitQueued(t, queue, 1)
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
	assert.NilError(t, tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4}))
	assert.NilError(t, tr.Destroy(t.Context()))

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert - Run returned without waiting for the driver: its context was
	// cancelled from the start. Destroy deleted the record, so the lost run
	// has no version.
	assert.NilError(t, err)
	assertTracked(t, m, 1, nil)
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Reason: "sandbox removed before the run finished"},
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
	err := tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})
	again := tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 5})

	// Assert
	assert.NilError(t, err)
	assert.ErrorIs(t, again, errRunning)
	assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
	assertTracked(t, m, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "docker", ID: "c1"})
}

// TestTaskRunnerStart_Create covers starting a TaskRunner with no sandbox:
// Start creates one from the spec and records its handle before starting it.
func TestTaskRunnerStart_Create(t *testing.T) {
	t.Parallel()
	// Arrange
	store := testStore(t)
	sb := &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{Type: "docker", ID: "c1"} },
		StartFunc: func(context.Context) error {
			// The handle is recorded before the sandbox is started.
			assertRecord(t, store, taskstate.Record{TaskID: 1, Version: 4, Type: "docker", ID: "c1"})
			return nil
		},
	}
	be := &backend.BackendMock{
		CreateFunc: func(context.Context, *backend.Spec) (backend.Sandbox, error) { return sb, nil },
	}
	m, _, _ := testManager(t, be, store)
	tr := m.Runner(1)

	// Act
	err := tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(be.CreateCalls(), 1))
	assert.DeepEqual(t, *be.CreateCalls()[0].Spec, backend.Spec{TaskID: 1, Version: 4})
	assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
	assertTracked(t, m, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "docker", ID: "c1"})
}

// TestTaskRunnerStart_Concurrent covers two Starts racing on a task with no
// sandbox: one sandbox is created and started, and the other Start finds it
// claimed.
func TestTaskRunnerStart_Concurrent(t *testing.T) {
	t.Parallel()
	// Arrange - a Create slow enough for both Starts to be underway.
	sb := &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{Type: "docker", ID: "c1"} },
		StartFunc:  func(context.Context) error { return nil },
	}
	be := &backend.BackendMock{
		CreateFunc: func(context.Context, *backend.Spec) (backend.Sandbox, error) {
			time.Sleep(10 * time.Millisecond)
			return sb, nil
		},
	}
	m, _, _ := testManager(t, be, testStore(t))

	// Act
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- m.Runner(1).Start(t.Context(), backend.Spec{TaskID: 1, Version: 4}) }()
	}
	first, second := <-errs, <-errs

	// Assert - one Start created and started the sandbox, the other started
	// nothing.
	assert.Assert(t, (first == nil) != (second == nil), "first: %v, second: %v", first, second)
	assert.ErrorIs(t, errors.Join(first, second), errRunning)
	assert.Assert(t, cmp.Len(be.CreateCalls(), 1))
	assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
	assertTracked(t, m, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "docker", ID: "c1"})
}

// TestTaskRunnerRestore covers adopting a record at Load: an up sandbox is
// claimed for its Run, a parked one is kept unclaimed, a gone or unopenable one
// leaves no sandbox and no record, and a failed status check keeps both.
func TestTaskRunnerRestore(t *testing.T) {
	t.Parallel()
	rec := taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1"}
	tests := []struct {
		name        string
		openErr     error
		status      backend.Status
		statusErr   error
		wantStatus  backend.Status
		wantErr     bool
		wantSandbox bool // the sandbox and record are kept
		wantClaimed bool
	}{
		{name: "up", status: backend.StatusUp, wantStatus: backend.StatusUp, wantSandbox: true, wantClaimed: true},
		{name: "parked", status: backend.StatusParked, wantStatus: backend.StatusParked, wantSandbox: true},
		{name: "gone", status: backend.StatusGone, wantStatus: backend.StatusGone},
		{name: "status error", statusErr: errors.New("boom"), wantErr: true, wantSandbox: true},
		{name: "open fails", openErr: errors.New("malformed handle"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a TaskRunner with no sandbox, and a previous process's
			// record.
			sb := &backend.SandboxMock{
				StatusFunc: func(context.Context) (backend.Status, error) { return tt.status, tt.statusErr },
			}
			be := &backend.BackendMock{
				OpenFunc: func(h backend.Handle) (backend.Sandbox, error) {
					assert.DeepEqual(t, h, recordHandle(rec))
					if tt.openErr != nil {
						return nil, tt.openErr
					}
					return sb, nil
				},
			}
			m, _, _ := testManager(t, be, testStore(t, rec))
			tr := NewTaskRunner(TaskRunnerOptions{
				TaskID:  rec.TaskID,
				Backend: be,
				Store:   m.store,
				Events:  m.events,
			})

			// Act
			st, err := tr.Restore(t.Context(), rec)

			// Assert
			if tt.wantErr {
				assert.Assert(t, err != nil)
			} else {
				assert.NilError(t, err)
				assert.Equal(t, st, tt.wantStatus)
			}
			got, ok, err := m.store.Read(rec.TaskID)
			assert.NilError(t, err)
			tr.mu.Lock()
			defer tr.mu.Unlock()
			if tt.wantSandbox {
				assert.Assert(t, ok, "record was deleted")
				assert.DeepEqual(t, got, rec)
				assert.Equal(t, tr.sbx, backend.Sandbox(sb))
				assert.DeepEqual(t, tr.rec, rec)
			} else {
				assert.Assert(t, !ok, "record was kept")
				assert.Assert(t, tr.sbx == nil)
				assert.DeepEqual(t, tr.rec, taskstate.Record{})
			}
			assert.Equal(t, tr.running, tt.wantClaimed)
		})
	}
}

// TestDoubling checks the backoff matches the schedule the hand-rolled loops
// used: start at initial, double without jitter, cap at maxInterval, and go
// back to initial on Reset.
func TestDoubling(t *testing.T) {
	bo := doubling(250*time.Millisecond, 3*time.Second)
	var got []time.Duration
	for range 6 {
		got = append(got, bo.NextBackOff())
	}
	assert.DeepEqual(t, got, []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second,
	})
	bo.Reset()
	assert.Equal(t, bo.NextBackOff(), 250*time.Millisecond)
}
