package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// testStore opens a fresh taskstate store in a temp dir, optionally seeded with
// records.
func testStore(t *testing.T, recs ...taskstate.Record) *taskstate.Store {
	t.Helper()
	s, err := taskstate.Open(t.TempDir())
	assert.NilError(t, err)
	for _, rec := range recs {
		assert.NilError(t, s.Write(rec))
	}
	return s
}

// submitted drives the outbox until it drains the persisted events, then returns
// the events it delivered through the mock client. The outbox's first Run pass
// delivers everything already persisted, so this exercises the same durable path
// production uses.
func submitted(t *testing.T, mock *gritzclient.ClientMock, queue *outbox.Outbox[model.RunnerEvent]) []*gritzv1.RunnerEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go queue.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n, err := queue.Len()
		assert.NilError(t, err)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	return mock.SubmittedRunnerEvents()
}

// waitQueued waits until the outbox holds n events. A TaskRunner's send
// returns once HandleEvents has received the event, which it enqueues only
// after.
func waitQueued(t *testing.T, queue *outbox.Outbox[model.RunnerEvent], n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := queue.Len()
		assert.NilError(t, err)
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox holds %d events, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTaskManagerEventsSurviveRestart is the durability payoff of this layer: events
// persisted by one runner process are redelivered by the next, with no separate
// recovery path — the reopened outbox's first Run pass drains them in FIFO order.
func TestTaskManagerEventsSurviveRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A runner persists two lifecycle events, then "crashes" before delivering
	// them: this outbox is never Run, so nothing reaches the server.
	crashed, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: dir,
		Client:   &gritzclient.ClientMock{},
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}))
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{TaskID: 2, Event: model.RunnerEventFailed, Version: 5, Reason: "boom"}))

	// The new process re-opens the same durable dir and delivers on startup.
	mock := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	restarted, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: dir,
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)

	// The events survived the restart and are redelivered in FIFO order, with
	// their TaskId/Version/Reason intact.
	events := submitted(t, mock, restarted)
	assert.DeepEqual(t, events, []*gritzv1.RunnerEvent{
		{Event: "started", TaskId: 1},
		{Event: "failed", TaskId: 2, Version: 5, Reason: "boom"},
	}, protocmp.Transform())
}

// testSandbox returns a sandbox whose driver is client and which is up until it
// is parked. parked is closed by its first Park.
func testSandbox(client driverv1connect.DriverServiceClient) (sb *backend.SandboxMock, parked <-chan struct{}) {
	var once sync.Once
	ch := make(chan struct{})
	sb = &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{Type: "docker", ID: "c1"} },
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return client, nil
		},
		StatusFunc: func(context.Context) (backend.Status, error) {
			select {
			case <-ch:
				return backend.StatusParked, nil
			default:
				return backend.StatusUp, nil
			}
		},
		ParkFunc: func(context.Context) error {
			once.Do(func() { close(ch) })
			return nil
		},
		DestroyFunc: func(context.Context) error { return nil },
	}
	return sb, ch
}

// testManager returns a manager with the "test" workspace whose client mints
// task tokens, with reattach timings shortened for tests, and its HandleEvents
// running until the test ends. Each tune then adjusts the options of the
// manager's TaskRunners before they are built.
func testManager(t *testing.T, be backend.Backend, store *taskstate.Store, tune ...func(*TaskRunnerOptions)) (*TaskManager, *gritzclient.ClientMock, *outbox.Outbox[model.RunnerEvent]) {
	t.Helper()
	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	newTaskRunner := func(opts TaskRunnerOptions) *TaskRunner {
		opts.RetryInterval = 10 * time.Millisecond
		opts.RetryMaxInterval = 50 * time.Millisecond
		opts.LostTimeout = 10 * time.Second
		opts.ReadyTimeout = 10 * time.Second
		for _, f := range tune {
			f(&opts)
		}
		return NewTaskRunner(opts)
	}
	m, err := NewTaskManager(TaskManagerOptions{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue, Workspaces: testWorkspaces(), NewTaskRunner: newTaskRunner})
	assert.NilError(t, err)
	go m.HandleEvents(t.Context())
	return m, mock, queue
}

// track tracks rec's task as Load does: rec in the store, and the task's
// TaskRunner holding sb.
func track(t *testing.T, m *TaskManager, rec taskstate.Record, sb backend.Sandbox) *TaskRunner {
	t.Helper()
	assert.NilError(t, m.store.Write(rec))
	tr := m.Runner(rec.TaskID)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.sbx, tr.rec = sb, rec
	return tr
}

// registered returns the task's TaskRunner, or nil if it has none, without
// registering one.
func registered(m *TaskManager, taskID int64) *TaskRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[taskID]
}

// claim claims tr for a Run, as Start or Restore does.
func claim(tr *TaskRunner) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.running = true
}

// seed tracks recs, each with the sandbox the TaskManager's backend opens for its
// handle, as Load does without probing the sandboxes.
func seed(t *testing.T, m *TaskManager, recs ...taskstate.Record) {
	t.Helper()
	for _, rec := range recs {
		sb, err := m.backend.Open(recordHandle(rec))
		assert.NilError(t, err)
		track(t, m, rec, sb)
	}
}

// assertTracked asserts that the task has a record and a TaskRunner holding
// that record, or, with want nil, that it has no record and no TaskRunner with
// a sandbox.
func assertTracked(t *testing.T, m *TaskManager, taskID int64, want *taskstate.Record) {
	t.Helper()
	rec, ok, err := m.store.Read(taskID)
	assert.NilError(t, err)
	tr := registered(m, taskID)
	if want == nil {
		assert.Assert(t, !ok, "task %d has a record", taskID)
		if tr != nil {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			assert.Assert(t, tr.sbx == nil, "task %d has a sandbox", taskID)
		}
		return
	}
	assert.Assert(t, ok, "no record for task %d", taskID)
	assert.DeepEqual(t, rec, *want)
	assert.Assert(t, tr != nil, "no TaskRunner for task %d", taskID)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	assert.DeepEqual(t, tr.rec, *want)
	assert.Assert(t, tr.sbx != nil)
}

// waitIdle waits until no Run follows tr's run.
func waitIdle(t *testing.T, tr *TaskRunner) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tr.mu.Lock()
		running := tr.running
		tr.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not return")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitReleased waits until the manager's Run goroutine has released the
// run's concurrency slot, which it then holds again.
func waitReleased(t *testing.T, m *TaskManager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !m.sem.TryAcquire(1) {
		if time.Now().After(deadline) {
			t.Fatal("concurrency slot was not released")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitDialed waits until the TaskRunner has dialed sb's driver at least min times.
func waitDialed(t *testing.T, sb *backend.SandboxMock, min int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(sb.DialCalls()) < min {
		if time.Now().After(deadline) {
			t.Fatalf("dialed %d times, want %d", len(sb.DialCalls()), min)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTaskManagerLoad_RestartReattaches covers a runner restart mid-run: the
// first runner process shuts down while it follows the run, and the next one
// reattaches to it from the record in Load.
func TestTaskManagerLoad_RestartReattaches(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, recorded at its version, followed by a
	// runner that then shuts down.
	client, stream := testDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, parked := testSandbox(client)
	be := &backend.BackendMock{
		OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
	}
	store := testStore(t, taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1"})
	first, _, _ := testManager(t, be, store)
	ctx, cancel := context.WithCancel(t.Context())
	assert.NilError(t, first.Load(ctx))
	waitDialed(t, sb, 1)
	cancel()
	waitIdle(t, first.Runner(1))
	// The sandbox stays up for next-boot rehydration, and the slot held.
	assert.Assert(t, cmp.Len(sb.ParkCalls(), 0))
	assert.Assert(t, !first.sem.TryAcquire(1))

	// Act - the next runner process loads the record and reattaches; then the
	// run finishes.
	dials := len(sb.DialCalls())
	m, _, queue := testManager(t, be, store)
	assert.NilError(t, m.Load(t.Context()))
	waitDialed(t, sb, dials+1)
	stopRun(t, client, 3)
	waitReleased(t, m)

	// Assert - the reattached runner saw Finished: the sandbox is parked, the
	// slot Load seeded is released, and no event is owed.
	<-parked
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

// startTask starts task as Poll does, holding the concurrency slot the run's
// Run goroutine releases.
func startTask(t *testing.T, m *TaskManager, task *model.Task) error {
	t.Helper()
	assert.Assert(t, m.sem.TryAcquire(1))
	err := m.Start(t.Context(), task)
	if err != nil {
		m.sem.Release(1)
	}
	return err
}

// stopRun stops the running run version, retrying until the driver has it
// running.
func stopRun(t *testing.T, client driverv1connect.DriverServiceClient, version int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Stop(t.Context(), &driverv1.StopRequest{Version: version})
		assert.NilError(t, err)
		if resp.GetStopped() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("run was never running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// legacyRecord is a record written by an older runner: its Data carries the run
// version.
func legacyRecord(taskID int64, id string) taskstate.Record {
	return taskstate.Record{TaskID: taskID, Version: 3, Type: "docker", ID: id, Data: json.RawMessage(`{"version":3}`)}
}

// testBackend returns a Backend mock whose opened sandboxes report the
// status (or error) given for their ID. Destroy records the IDs it destroyed.
func testBackend(status map[string]backend.Status, statusErr map[string]error) (*backend.BackendMock, *[]string) {
	var mu sync.Mutex
	var destroyed []string
	return &backend.BackendMock{
		OpenFunc: func(h backend.Handle) (backend.Sandbox, error) {
			return &backend.SandboxMock{
				StatusFunc: func(context.Context) (backend.Status, error) {
					if err := statusErr[h.ID]; err != nil {
						return backend.StatusUnknown, err
					}
					return status[h.ID], nil
				},
				DestroyFunc: func(context.Context) error {
					mu.Lock()
					defer mu.Unlock()
					destroyed = append(destroyed, h.ID)
					return nil
				},
			}, nil
		},
	}, &destroyed
}

func TestTaskManagerLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		record     taskstate.Record
		openErr    error
		status     backend.Status
		statusErr  error
		wantEvents []*gritzv1.RunnerEvent
		// wantRecord is whether the record is kept, and the task's TaskRunner
		// holds its sandbox.
		wantRecord bool
		wantAttach bool // run: the TaskRunner dialed the driver to reattach
	}{
		{
			name:       "up",
			record:     legacyRecord(1, "c1"),
			status:     backend.StatusUp,
			wantRecord: true,
			wantAttach: true,
		},
		{
			// A parked husk is a lost-report backstop; its record is kept.
			name:       "parked",
			record:     legacyRecord(1, "c1"),
			status:     backend.StatusParked,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
			wantRecord: true,
		},
		{
			// A gone sandbox is a lost-report backstop; its record is dropped.
			name:       "gone",
			record:     legacyRecord(1, "c1"),
			status:     backend.StatusGone,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
		},
		{
			// A failed status check leaves the record alone.
			name:       "status error",
			record:     legacyRecord(1, "c1"),
			statusErr:  errors.New("boom"),
			wantRecord: true,
		},
		{
			// A record whose handle cannot be opened (a malformed handle) has
			// no sandbox, so it is dropped.
			name:    "open fails",
			record:  legacyRecord(1, "c1"),
			openErr: errors.New("malformed handle"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the record's task is still RUNNING on the server.
			mock := &gritzclient.ClientMock{
				GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
					return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING}}, nil
				},
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			be, destroyed := testBackend(
				map[string]backend.Status{"c1": tt.status},
				map[string]error{"c1": tt.statusErr},
			)
			dialed := make(chan backend.Handle, 1)
			open := be.OpenFunc
			be.OpenFunc = func(h backend.Handle) (backend.Sandbox, error) {
				if tt.openErr != nil {
					return nil, tt.openErr
				}
				sb, err := open(h)
				sb.(*backend.SandboxMock).DialFunc = func(ctx context.Context, _ string) (driverv1connect.DriverServiceClient, error) {
					dialed <- h // the up sandbox got a Run goroutine
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return sb, err
			}
			store := testStore(t, tt.record)
			queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
				StoreDir: t.TempDir(),
				Client:   mock,
				Backoff:  backoff.NewConstantBackOff(0),
				Log:      slog.Default(),
			})
			assert.NilError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m, err := NewTaskManager(TaskManagerOptions{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 5, Queue: queue})
			assert.NilError(t, err)
			go m.HandleEvents(ctx)

			// Act
			err = m.Load(ctx)

			// Assert
			assert.NilError(t, err)
			assert.DeepEqual(t, submitted(t, mock, queue), tt.wantEvents, protocmp.Transform())
			if tt.wantRecord {
				assertTracked(t, m, 1, &tt.record)
			} else {
				assertTracked(t, m, 1, nil)
			}
			assert.Assert(t, cmp.Len(*destroyed, 0)) // Load never destroys
			var running int64
			if tt.wantAttach {
				running = 1
				// Reattached over the sandbox opened from the recorded handle.
				select {
				case h := <-dialed:
					assert.DeepEqual(t, h, backend.Handle{Type: "docker", ID: "c1", Data: tt.record.Data})
				case <-time.After(3 * time.Second):
					t.Fatal("up sandbox was not reattached")
				}
			} else {
				assert.Equal(t, len(dialed), 0)
			}
			// The semaphore was seeded with the up count.
			assert.Assert(t, m.sem.TryAcquire(5-running))
			assert.Assert(t, !m.sem.TryAcquire(1))
		})
	}
}

// TestTaskManagerLoad_VersionScopedBackstop checks that the boot-time backstop stamps
// the exited record's version onto its "failed" so ApplyRunnerEvent drops it for
// a superseded run, applies it for the current run, and bypasses for a legacy
// version-0 record.
func TestTaskManagerLoad_VersionScopedBackstop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		recVersion int64 // version stamped in the seeded exited record
		taskNow    int64 // the task's current version when the event is applied
		wantApply  bool
	}{
		{name: "superseded run dropped", recVersion: 4, taskNow: 5, wantApply: false},
		{name: "current run applies", recVersion: 4, taskNow: 4, wantApply: true},
		{name: "legacy record bypasses", recVersion: 0, taskNow: 5, wantApply: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - one parked husk whose task is still RUNNING on the server,
			// so the boot-time backstop fires a "failed".
			mock := &gritzclient.ClientMock{
				GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
					return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING}}, nil
				},
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			be, _ := testBackend(map[string]backend.Status{"c1": backend.StatusParked}, nil)
			store := testStore(t, taskstate.Record{TaskID: 1, Version: tt.recVersion, Type: "docker", ID: "c1"})
			queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
				StoreDir: t.TempDir(),
				Client:   mock,
				Backoff:  backoff.NewConstantBackOff(0),
				Log:      slog.Default(),
			})
			assert.NilError(t, err)
			m, err := NewTaskManager(TaskManagerOptions{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
			assert.NilError(t, err)

			// Act - Load drives the parked husk through failIfTaskRunning, which
			// emits a "failed" stamped with the record's version.
			assert.NilError(t, m.Load(t.Context()))
			events := submitted(t, mock, queue)
			assert.Equal(t, len(events), 1)
			assert.Equal(t, events[0].Version, tt.recVersion) // stamped from the record

			// The existing guard decides: apply the backstop against a task at its
			// current version.
			task := &model.Task{ID: 1, Status: model.TaskStatusRunning, Version: tt.taskNow}
			ev := model.RunnerEventFromProto(events[0])
			assert.Equal(t, task.ApplyRunnerEvent(&ev), tt.wantApply)
		})
	}
}

func TestTaskManagerPrune(t *testing.T) {
	t.Parallel()
	// Arrange - every record but the deleted task's (4) belongs to an archived
	// task, except 2's.
	mock := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			if req.Id == 4 {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("task not found"))
			}
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Archived: req.Id != 2}}, nil
		},
	}
	be, destroyed := testBackend(
		map[string]backend.Status{
			"c1": backend.StatusParked, // archived -> removed
			"c2": backend.StatusParked, // not archived -> kept
			"c3": backend.StatusUp,     // up -> kept
			"c4": backend.StatusParked, // deleted -> removed
			"c5": backend.StatusGone,   // archived -> removed
		},
		map[string]error{
			"c7": errors.New("boom"), // status unknown -> kept
		},
	)
	store := testStore(t)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	m, err := NewTaskManager(TaskManagerOptions{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)
	seed(t, m,
		legacyRecord(1, "c1"),
		legacyRecord(2, "c2"),
		legacyRecord(3, "c3"),
		legacyRecord(4, "c4"),
		legacyRecord(5, "c5"),
		legacyRecord(7, "c7"),
	)
	// Task 6 is archived, and its TaskRunner has no sandbox.
	tr6 := m.Runner(6)
	// Task 8 is archived, and its TaskRunner's run is claimed.
	tr8 := track(t, m, legacyRecord(8, "c8"), &backend.SandboxMock{})
	claim(tr8)

	// Act
	err = m.Prune(t.Context())
	assert.NilError(t, err)

	// Assert - every registered task was looked up.
	var looked []int64
	for _, call := range mock.GetTaskCalls() {
		looked = append(looked, call.GetTaskRequest.Id)
	}
	slices.Sort(looked)
	assert.DeepEqual(t, looked, []int64{1, 2, 3, 4, 5, 6, 7, 8})
	// Archived/deleted tasks' sandboxes are destroyed.
	slices.Sort(*destroyed)
	assert.DeepEqual(t, *destroyed, []string{"c1", "c4", "c5"})
	recs, err := store.List()
	assert.NilError(t, err)
	var kept []int64
	for _, rec := range recs {
		kept = append(kept, rec.TaskID)
	}
	slices.Sort(kept)
	assert.DeepEqual(t, kept, []int64{2, 3, 7, 8})
	// The registry dropped the retired tasks' TaskRunners, including the one
	// with no sandbox, and kept the claimed one.
	m.mu.Lock()
	tasks := slices.Sorted(maps.Keys(m.tasks))
	m.mu.Unlock()
	assert.DeepEqual(t, tasks, []int64{2, 3, 7, 8})
	// The next command for a dropped task gets a fresh TaskRunner.
	assert.Assert(t, m.Runner(6) != tr6)
	assert.Equal(t, m.Runner(8), tr8)
}

// TestTaskManagerRun covers the poll loop: between Poll passes it prunes an
// archived task's parked sandbox, and once die fires it returns the FatalError
// cause.
func TestTaskManagerRun(t *testing.T) {
	t.Parallel()
	// Arrange - task 1 is archived and its sandbox is parked.
	be, _ := testBackend(map[string]backend.Status{"c1": backend.StatusParked}, nil)
	m, mock, _ := testManager(t, be, testStore(t))
	mock.ListRunnerTasksFunc = func(context.Context, *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{}, nil
	}
	mock.GetTaskFunc = func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
		return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Archived: true}}, nil
	}
	m.pollInterval = 10 * time.Millisecond
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m.fatal = cancelCause
	seed(t, m, legacyRecord(1, "c1"))

	// Act
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Assert - the archived task's sandbox is pruned while Run keeps polling.
	deadline := time.Now().Add(3 * time.Second)
	for registered(m, 1) != nil {
		if time.Now().After(deadline) {
			t.Fatal("sandbox was not pruned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertTracked(t, m, 1, nil)
	assert.Assert(t, len(mock.ListRunnerTasksCalls()) > 0)

	// Assert - Run returns the FatalError cause once die fires.
	m.die(errors.New("boom"))
	select {
	case err := <-done:
		var fatal FatalError
		assert.Assert(t, errors.As(err, &fatal), "got %v", err)
		assert.ErrorContains(t, err, "boom")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestTaskManagerRunning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		records []taskstate.Record
		status  backend.Status
		err     error
		want    bool
	}{
		{name: "up", records: []taskstate.Record{legacyRecord(1, "c1")}, status: backend.StatusUp, want: true},
		{name: "parked", records: []taskstate.Record{legacyRecord(1, "c1")}, status: backend.StatusParked},
		{name: "gone", records: []taskstate.Record{legacyRecord(1, "c1")}, status: backend.StatusGone},
		{name: "no record"},
		{name: "status error", records: []taskstate.Record{legacyRecord(1, "c1")}, err: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be, _ := testBackend(
				map[string]backend.Status{"c1": tt.status},
				map[string]error{"c1": tt.err},
			)
			m, err := NewTaskManager(TaskManagerOptions{Backend: be, Store: testStore(t), RunnerID: "test-runner"})
			assert.NilError(t, err)
			seed(t, m, tt.records...)

			// Act
			running, err := m.Runner(1).Running(t.Context())

			// Assert
			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
			} else {
				assert.NilError(t, err)
			}
			assert.Equal(t, running, tt.want)
		})
	}
}

// sleepForever is an agent config whose dummy agent runs until it is stopped.
var sleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// testDriver serves a driver server over an in-memory Connect client. With cfg
// set, it starts run version of taskID with that agent config and returns the
// run's stream; with cfg nil the driver has never run.
func testDriver(t *testing.T, taskID, version int64, cfg *agent.Config) (driverv1connect.DriverServiceClient, *connect.ServerStreamForClient[driverv1.RunResponse]) {
	t.Helper()
	_, client, stream := serveDriver(t, taskID, version, cfg)
	return client, stream
}

// serveDriver is testDriver, also returning the server the driver is served
// by.
func serveDriver(t *testing.T, taskID, version int64, cfg *agent.Config) (*httptest.Server, driverv1connect.DriverServiceClient, *connect.ServerStreamForClient[driverv1.RunResponse]) {
	t.Helper()
	hs, client, configs := newDriver(t, version, nil)
	if cfg == nil {
		return hs, client, nil
	}
	assert.NilError(t, configs.Save(taskID, cfg))
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{
		Version: version,
		Spec:    &driverv1.RunSpec{TaskId: taskID, ServerUrl: "http://gritz.test", Token: "token"},
	})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	assert.Assert(t, stream.Receive(), "Run: %v", stream.Err())
	assert.Assert(t, stream.Msg().GetAccepted() != nil, "first message is %v, want accepted", stream.Msg())
	return hs, client, stream
}

// newDriver serves a driver server that has never run, for tasks on version.
// It reads the agent config from where the TaskManager's spec provisions it, so a
// Run with that spec runs the workspace's agent; configs is that store. Until
// ready is closed every call fails Unavailable, as from a driver that is not
// up yet; a nil ready is open.
func newDriver(t *testing.T, version int64, ready <-chan struct{}) (*httptest.Server, driverv1connect.DriverServiceClient, agent.ConfigStore) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	configs := agent.ConfigStore(filepath.Join(root, string(agent.DefaultConfigStore)))
	gritz := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Version: version}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		ListEventsByTaskFunc: func(_ context.Context, _ *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return &gritzv1.ListEventsByTaskResponse{}, nil
		},
		ListLinksFunc: func(_ context.Context, _ *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
		AppendLogChunkFunc: func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	srv, err := driverserver.New(driverserver.Options{
		RecordPath: filepath.Join(dir, "run.json"),
		LogPath:    filepath.Join(dir, "log"),
		Config:     configs,
		FileRoot:   root,
		NewClient:  func(_, _ string) gritzclient.Client { return gritz },
		KeepAlive:  20 * time.Millisecond,
	})
	assert.NilError(t, err)
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	var handler http.Handler = mux
	if ready != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-ready:
				mux.ServeHTTP(w, req)
			default:
				http.Error(w, "driver is starting", http.StatusServiceUnavailable)
			}
		})
	}
	hs := httptest.NewServer(handler)
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})
	return hs, driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL), configs
}

// TestTaskManagerDie_StopWithoutSandbox is the sharpest unrecoverable case from #1209:
// a stop command with no running sandbox has no reconciliation path on restart
// (Load has nothing to probe and cannot re-derive "stopped"), so a dropped persist
// would leave the task stuck in cancelling forever. When the durable outbox write
// fails there, the runner must crash — die cancels the root ctx with a
// FatalError cause — instead of logging and continuing.
func TestTaskManagerDie_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
	}
	// No record, so there is no run to stop → the TaskManager emits "stopped".
	// Back the outbox with a store whose durable Append fails. The die path is
	// reached through Enqueue → Append alone, so Peek/Drop/Len are never called
	// (an unstubbed moq method would panic if they were).
	store := &outbox.StoreMock{
		AppendFunc: func(json.RawMessage) error { return errors.New("no space left on device") },
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		Store:   store,
		Client:  mock,
		Backoff: backoff.NewConstantBackOff(0),
		Log:     slog.Default(),
	})
	assert.NilError(t, err)

	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m, err := NewTaskManager(TaskManagerOptions{Client: mock, Backend: &backend.BackendMock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
	assert.NilError(t, err)

	// Act - the "stopped" enqueue fails durably, so die fires.
	assert.NilError(t, m.Poll(ctx))

	// Assert - the root ctx is cancelled with a FatalError cause, which the
	// command layer maps to a non-zero exit.
	var fatal FatalError
	assert.Assert(t, errors.As(context.Cause(ctx), &fatal))
}

// TestTaskManagerGracefulShutdown_NoFatalCause is the complement: on the happy path the
// durable write succeeds, die never fires, and a plain ctx cancel (the signal
// shutdown) leaves no FatalError cause — so the command layer's exit mapping
// returns nil (exit 0) rather than crashing.
func TestTaskManagerGracefulShutdown_NoFatalCause(t *testing.T) {
	t.Parallel()
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)

	ctx, cancelCause := context.WithCancelCause(context.Background())
	m, err := NewTaskManager(TaskManagerOptions{Client: mock, Backend: &backend.BackendMock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
	assert.NilError(t, err)

	// The durable enqueue succeeds, so die never fires.
	assert.NilError(t, m.Poll(ctx))

	// A graceful signal shutdown cancels ctx with no FatalError cause.
	cancelCause(nil)
	var fatal FatalError
	assert.Assert(t, !errors.As(context.Cause(ctx), &fatal))
}

// TestTaskManagerHandleEvents_Lost covers a lost run reported through the
// events channel: HandleEvents turns it into the run's "failed" in the outbox.
func TestTaskManagerHandleEvents_Lost(t *testing.T) {
	t.Parallel()
	// Arrange
	m, mock, queue := testManager(t, &backend.BackendMock{}, testStore(t))
	tr := track(t, m, taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1"}, &backend.SandboxMock{})

	// Act
	tr.send(t.Context(), TaskLostEvent{TaskID: 1, Version: 3, Reason: "boom"})

	// Assert
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "boom"},
	}, protocmp.Transform())
}

// TestTaskManagerHandleEvents_Fatal covers a failed record write: the
// TaskRunner sends a TaskFatalEvent, and HandleEvents terminates the runner
// with a FatalError cause.
func TestTaskManagerHandleEvents_Fatal(t *testing.T) {
	t.Parallel()
	// Arrange - a store whose directory is gone, so every write fails.
	dir := t.TempDir()
	store, err := taskstate.Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, os.RemoveAll(dir))
	sb, _ := testSandbox(nil)
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		CreateFunc:            func(context.Context, *backend.Spec) (backend.Sandbox, error) { return sb, nil },
	}
	m, _, _ := testManager(t, be, store)
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m.fatal = cancelCause

	// Act
	err = startTask(t, m, &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1})

	// Assert - Start failed, the unrecorded sandbox was destroyed, and the
	// runner's root ctx is cancelled with a FatalError cause.
	assert.ErrorContains(t, err, "failed to record task handle")
	assert.Assert(t, cmp.Len(sb.DestroyCalls(), 1))
	assert.Assert(t, cmp.Len(sb.StartCalls(), 0))
	assert.Assert(t, m.Runner(1).sbx == nil)
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("runner was not terminated")
	}
	var fatal FatalError
	assert.Assert(t, errors.As(context.Cause(ctx), &fatal))
}

// TestTaskRunnerSend_Shutdown covers a send with nothing handling events: it
// returns once ctx is done, dropping the event.
func TestTaskRunnerSend_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a manager whose HandleEvents is not running.
	m, err := NewTaskManager(TaskManagerOptions{Backend: &backend.BackendMock{}, Store: testStore(t), RunnerID: "test-runner"})
	assert.NilError(t, err)
	tr := track(t, m, taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1"}, &backend.SandboxMock{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Act
	done := make(chan struct{})
	go func() {
		tr.send(ctx, TaskLostEvent{TaskID: 1, Version: 3, Reason: "boom"})
		close(done)
	}()

	// Assert
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("send did not return")
	}
}

// testWorkspaces returns a minimal workspace config with a "test" workspace.
func testWorkspaces() *workspace.Config {
	return &workspace.Config{
		Workspaces: map[string]workspace.Workspace{
			"test": {
				Container: workspace.Container{Image: "alpine:latest"},
				Agent:     workspace.Agent{Type: "dummy", Dummy: &workspace.DummyConfig{}},
			},
		},
	}
}
