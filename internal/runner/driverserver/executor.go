package driverserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/logship"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
)

// ErrShutdown is returned by Executor.Start once Shutdown has been called.
var ErrShutdown = errors.New("driver is shutting down")

// ExecutorOptions configures an Executor. Every field takes its default when
// zero.
type ExecutorOptions struct {
	// RecordPath is the run record. Defaults to runrecord.Path.
	RecordPath string
	// LogPath is the append-only driver log each run appends to. Defaults to
	// agent.DefaultLogPath.
	LogPath string
	// Config is where the task config file lives. Defaults to
	// agent.DefaultConfigStore.
	Config agent.ConfigStore
	// FileRoot is the directory the RunSpec file paths and ProvisionedPath
	// are relative to. Defaults to "/".
	FileRoot string
	// Environ is the base of every run's environment, which RunSpec.env and
	// then RunSpec.secrets are layered on.
	Environ []string
	// NewClient builds a run's gritz client. Defaults to gritzclient.New.
	NewClient func(serverURL, token string) gritzclient.Client
	// Log is the executor's own logger. Defaults to slog.Default().
	Log *slog.Logger
}

// Executor owns the agent process: at most one run at a time, and the run
// record. It lives as long as the driver process, across Run streams.
type Executor struct {
	recordPath string
	logPath    string
	config     agent.ConfigStore
	fileRoot   string
	environ    []string
	newClient  func(serverURL, token string) gritzclient.Client
	log        *slog.Logger

	// runs tracks the goroutine of the current run, so Shutdown can wait for
	// it.
	runs sync.WaitGroup

	mu sync.Mutex
	// rec mirrors the run record on disk; hasRec is false until the sandbox
	// has started its first run. rec.State is Running exactly while a run
	// started by this process is in progress.
	rec    runrecord.Record
	hasRec bool
	// cancel cancels the current run; nil before the first run.
	cancel context.CancelCauseFunc
	// done is closed once the current run has finished and its record is
	// written. It starts closed.
	done chan struct{}
	// closed is set by Shutdown; no run starts after it.
	closed bool
}

// NewExecutor returns an executor, reconciling the run record first: a record
// still running means the previous process died mid-run, so it is rewritten
// as finished and unreported.
func NewExecutor(opts ExecutorOptions) (*Executor, error) {
	e := &Executor{
		recordPath: cmp.Or(opts.RecordPath, runrecord.Path),
		logPath:    cmp.Or(opts.LogPath, agent.DefaultLogPath),
		config:     cmp.Or(opts.Config, agent.DefaultConfigStore),
		fileRoot:   cmp.Or(opts.FileRoot, "/"),
		environ:    opts.Environ,
		newClient:  opts.NewClient,
		log:        cmp.Or(opts.Log, slog.Default()),
		done:       make(chan struct{}),
	}
	close(e.done)
	if e.newClient == nil {
		e.newClient = func(serverURL, token string) gritzclient.Client {
			return gritzclient.New(gritzclient.Options{BaseURL: serverURL, Token: token})
		}
	}
	rec, ok, crashed, err := runrecord.Reconcile(e.recordPath, time.Now())
	if err != nil {
		return nil, err
	}
	if crashed {
		e.log.Warn("driver restarted mid-run", "version", rec.Version)
	}
	e.rec, e.hasRec = rec, ok
	return e, nil
}

// State returns the run record, and false if the sandbox has not started a
// run yet. The record is Running while a run is in progress.
func (e *Executor) State() (rec runrecord.Record, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rec, e.hasRec
}

// Done returns a channel closed once the current run has finished and its
// record is written; it is closed already if no run is in progress.
func (e *Executor) Done() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.done
}

// Start provisions the spec's files, writes the run record and starts run
// version in the background. It returns ErrShutdown once Shutdown has been
// called. It must not be called while a run is in progress.
func (e *Executor) Start(version int64, spec *driverv1.RunSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hasRec && e.rec.State == runrecord.Running {
		panic("driverserver: Executor.Start called while a run is in progress")
	}
	if e.closed {
		return ErrShutdown
	}
	if err := e.provision(spec.GetFiles()); err != nil {
		return err
	}
	rec := runrecord.Record{Version: version, State: runrecord.Running}
	if err := runrecord.Write(e.recordPath, rec); err != nil {
		return err
	}
	e.rec, e.hasRec = rec, true
	// Only Stop and Shutdown cancel a run; no request's context reaches it.
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	e.cancel, e.done = cancel, done
	e.runs.Go(func() {
		defer cancel(nil)
		e.execute(ctx, version, spec, done)
	})
	return nil
}

// provision writes files, unless the sandbox has been provisioned already,
// then creates the marker. Files are written once per sandbox, so anything the
// agent changes in them survives later runs.
func (e *Executor) provision(files []*driverv1.File) error {
	marker := filepath.Join(e.fileRoot, ProvisionedPath)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("provision: %w", err)
	}
	for _, f := range files {
		if err := writeFile(filepath.Join(e.fileRoot, f.GetPath()), f); err != nil {
			return fmt.Errorf("provision %s: %w", f.GetPath(), err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	return nil
}

// writeFile writes f at path with f's mode, creating missing parents. The mode
// is applied with chmod so the umask does not narrow it, but only when it
// differs: a directory that exists already may belong to another user.
func writeFile(path string, f *driverv1.File) error {
	mode := fs.FileMode(f.GetMode()) & fs.ModePerm
	if f.GetDir() {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, f.GetData(), mode); err != nil {
			return err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() == mode {
		return nil
	}
	return os.Chmod(path, mode)
}

// execute runs the driver for spec, then records the outcome and closes done.
func (e *Executor) execute(ctx context.Context, version int64, spec *driverv1.RunSpec, done chan struct{}) {
	client := e.newClient(spec.GetServerUrl(), spec.GetToken())

	// Built before anything touches the server, so the mask is in place before
	// the first shipped byte.
	shipper := logship.New(client, logship.Options{
		TaskID:  spec.GetTaskId(),
		Secrets: Secrets(spec),
	})
	shipCtx, stopShipper := context.WithCancel(context.Background())
	go shipper.Run(shipCtx)
	log := agent.OpenDriverLog(e.logPath, shipper)

	driver := &agent.Driver{
		TaskID:    spec.GetTaskId(),
		Client:    client,
		Log:       log,
		Config:    e.config,
		ServerURL: spec.GetServerUrl(),
		Token:     spec.GetToken(),
		Env:       Env(e.environ, spec),
	}
	err := driver.Run(ctx)
	if err != nil {
		e.log.Error("run not reported", "version", version, "err", err)
	}
	_ = log.Close()
	stopShipper()

	rec := runrecord.Record{
		Version:    version,
		State:      runrecord.Finished,
		FinishedAt: time.Now(),
		Reported:   err == nil,
	}
	if err := runrecord.Write(e.recordPath, rec); err != nil {
		e.log.Error("failed to write run record", "version", version, "err", err)
	}
	e.mu.Lock()
	e.rec = rec
	close(done)
	e.mu.Unlock()
}

// Stop cancels run version with agent.ErrStop, if it is in progress, and
// reports whether it did. It does not wait for the run to finish.
func (e *Executor) Stop(version int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stop(version)
}

// stop is Stop with e.mu held.
func (e *Executor) stop(version int64) bool {
	if !e.hasRec || e.rec.State != runrecord.Running || e.rec.Version != version {
		return false
	}
	e.cancel(agent.ErrStop)
	return true
}

// Shutdown stops the current run, if any, and waits for it to finish and
// report, or for ctx to expire. No run starts after Shutdown is called.
func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.stop(e.rec.Version)
	e.mu.Unlock()
	done := make(chan struct{})
	go func() {
		e.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
