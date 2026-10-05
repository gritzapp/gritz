// Package driverserver implements driver.v1.DriverService, served by
// `gritz driver --serve` inside the sandbox. Runs execute in-process, one at a
// time, each built from its RunSpec: its own gritz client, log shipper, secret
// mask and environment.
//
// This is phase A of proposals/draft/driver-server.md: the driver still
// reports started / stopped / failed to the gritz server itself, exactly as
// the one-shot `gritz driver` does, and Finished says whether that report was
// acknowledged.
package driverserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/logship"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"github.com/gritzapp/gritz/internal/x/envx"
)

// DefaultKeepAlive is how often an open Run stream gets a KeepAlive. A write
// is how the server notices a stream whose runner has gone away, so this bounds
// how long a dead stream blocks the next Run.
const DefaultKeepAlive = 15 * time.Second

// Options configures a Server. Every field takes its default when zero.
type Options struct {
	// RecordPath is the run record. Defaults to runrecord.Path.
	RecordPath string
	// LogPath is the append-only driver log each run appends to. Defaults to
	// agent.DefaultLogPath.
	LogPath string
	// Config is where the task config file lives. Defaults to
	// agent.DefaultConfigStore.
	Config agent.ConfigStore
	// Environ is the base of every run's environment, which RunSpec.env and
	// then RunSpec.secrets are layered on. The command sets it to
	// os.Environ(), so whatever a custom image sets reaches the agent.
	Environ []string
	// NewClient builds a run's gritz client. Defaults to gritzclient.New.
	NewClient func(serverURL, token string) gritzclient.Client
	// KeepAlive is the interval between KeepAlive messages on a Run stream.
	// Defaults to DefaultKeepAlive.
	KeepAlive time.Duration
	// Log is the server's own logger. Defaults to slog.Default().
	Log *slog.Logger
}

// Server implements driverv1connect.DriverServiceHandler.
type Server struct {
	recordPath string
	logPath    string
	config     agent.ConfigStore
	environ    []string
	newClient  func(serverURL, token string) gritzclient.Client
	keepAlive  time.Duration
	log        *slog.Logger

	// runs tracks the goroutine of the active run, so Shutdown can wait for it.
	runs sync.WaitGroup

	mu sync.Mutex
	// rec mirrors the run record on disk; hasRec is false until the sandbox
	// has started its first run.
	rec    runrecord.Record
	hasRec bool
	// active is the latest run started by this process, nil if none. It stays
	// set after the run finishes, so a Run attaching late still gets its
	// Finished.
	active *run
	// streaming is true while a Run stream is open.
	streaming bool
	// closed is set by Shutdown; no run starts after it.
	closed bool
}

var _ driverv1connect.DriverServiceHandler = (*Server)(nil)

// run is a run executing in this process.
type run struct {
	version int64
	cancel  context.CancelCauseFunc
	// done is closed once the run has finished and its record is written.
	done chan struct{}
	// reported is set before done is closed.
	reported bool
}

// New returns a server, reconciling the run record first: a record still
// running means the previous process died mid-run, so it is rewritten as
// finished and unreported.
func New(opts Options) (*Server, error) {
	s := &Server{
		recordPath: cmp.Or(opts.RecordPath, runrecord.Path),
		logPath:    cmp.Or(opts.LogPath, agent.DefaultLogPath),
		config:     cmp.Or(opts.Config, agent.DefaultConfigStore),
		environ:    opts.Environ,
		newClient:  opts.NewClient,
		keepAlive:  cmp.Or(opts.KeepAlive, DefaultKeepAlive),
		log:        cmp.Or(opts.Log, slog.Default()),
	}
	if s.newClient == nil {
		s.newClient = func(serverURL, token string) gritzclient.Client {
			return gritzclient.New(gritzclient.Options{BaseURL: serverURL, Token: token})
		}
	}
	rec, ok, crashed, err := runrecord.Reconcile(s.recordPath, time.Now())
	if err != nil {
		return nil, err
	}
	if crashed {
		s.log.Warn("driver restarted mid-run", "version", rec.Version)
	}
	s.rec, s.hasRec = rec, ok
	return s, nil
}

// Handler returns the path and handler to mount the service on.
func (s *Server) Handler() (string, http.Handler) {
	return driverv1connect.NewDriverServiceHandler(s)
}

// Status reports the current run record.
func (s *Server) Status(ctx context.Context, req *driverv1.StatusRequest) (*driverv1.StatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &driverv1.StatusResponse{}
	if s.hasRec {
		resp.Run = s.rec.Proto()
	}
	return resp, nil
}

// Run attaches to or starts the run with the request's version, then streams
// until that run's Finished has been sent. The run's context is independent of
// the request's: a dropped stream detaches and the run keeps going.
func (s *Server) Run(ctx context.Context, req *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
	r, attached, err := s.open(req)
	if err != nil {
		return err
	}
	defer s.release()
	if err := stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{
		Accepted: &driverv1.Accepted{Attached: attached},
	}}); err != nil {
		return err
	}
	ticker := time.NewTicker(s.keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Finished{
				Finished: &driverv1.Finished{Version: r.version, Reported: r.reported},
			}})
		case <-ticker.C:
			if err := stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_KeepAlive{
				KeepAlive: &driverv1.KeepAlive{},
			}}); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// open claims the stream and resolves the request to a run, starting it if
// needed. On success the caller must release the stream.
func (s *Server) open(req *driverv1.RunRequest) (r *run, attached bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streaming {
		return nil, false, connect.NewError(connect.CodeUnavailable, errors.New("another Run stream is open"))
	}
	version := req.GetVersion()
	switch {
	case s.hasRec && s.rec.Version > version:
		return nil, false, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("version %d is stale, the run record is at %d", version, s.rec.Version))
	case s.hasRec && s.rec.Version == version:
		r = s.active
		if r == nil || r.version != version {
			// Finished before this process started: either it finished in an
			// earlier process, or Reconcile marked it finished after a crash.
			r = finishedRun(version, s.rec.Reported)
		}
		attached = true
	case s.hasRec && s.rec.State == runrecord.Running:
		return nil, false, connect.NewError(connect.CodeAborted,
			fmt.Errorf("version %d is running", s.rec.Version))
	case req.GetSpec() == nil:
		return nil, false, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("no run with version %d and no spec to start one", version))
	case s.closed:
		return nil, false, connect.NewError(connect.CodeUnavailable, errors.New("driver is shutting down"))
	default:
		if r, err = s.start(version, req.GetSpec()); err != nil {
			return nil, false, connect.NewError(connect.CodeInternal, err)
		}
	}
	s.streaming = true
	return r, attached, nil
}

func (s *Server) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = false
}

// finishedRun returns a run that has already finished.
func finishedRun(version int64, reported bool) *run {
	r := &run{version: version, done: make(chan struct{}), reported: reported}
	close(r.done)
	return r
}

// start writes the run record and starts the run in the background. It is
// called with s.mu held.
func (s *Server) start(version int64, spec *driverv1.RunSpec) (*run, error) {
	rec := runrecord.Record{Version: version, State: runrecord.Running}
	if err := runrecord.Write(s.recordPath, rec); err != nil {
		return nil, err
	}
	s.rec, s.hasRec = rec, true
	// Only Stop and Shutdown cancel a run; no request's context reaches it.
	ctx, cancel := context.WithCancelCause(context.Background())
	r := &run{version: version, cancel: cancel, done: make(chan struct{})}
	s.active = r
	s.runs.Go(func() {
		defer cancel(nil)
		s.execute(ctx, r, spec)
	})
	return r, nil
}

// execute runs the driver for spec, then records the outcome and finishes r.
func (s *Server) execute(ctx context.Context, r *run, spec *driverv1.RunSpec) {
	client := s.newClient(spec.GetServerUrl(), spec.GetToken())

	// Built before anything touches the server, as in the one-shot driver, so
	// the mask is in place before the first shipped byte.
	shipper := logship.New(client, logship.Options{
		TaskID:  spec.GetTaskId(),
		Secrets: Secrets(spec),
	})
	shipCtx, stopShipper := context.WithCancel(context.Background())
	go shipper.Run(shipCtx)
	log := agent.OpenDriverLog(s.logPath, shipper)

	driver := &agent.Driver{
		TaskID:    spec.GetTaskId(),
		Client:    client,
		Log:       log,
		Config:    s.config,
		ServerURL: spec.GetServerUrl(),
		Token:     spec.GetToken(),
		Env:       Env(s.environ, spec),
	}
	err := driver.Run(ctx)
	if err != nil {
		s.log.Error("run not reported", "version", r.version, "err", err)
	}
	_ = log.Close()
	stopShipper()

	reported := err == nil
	rec := runrecord.Record{
		Version:    r.version,
		State:      runrecord.Finished,
		FinishedAt: time.Now(),
		Reported:   reported,
	}
	if err := runrecord.Write(s.recordPath, rec); err != nil {
		s.log.Error("failed to write run record", "version", r.version, "err", err)
	}
	s.mu.Lock()
	s.rec = rec
	r.reported = reported
	close(r.done)
	s.mu.Unlock()
}

// Stop cancels the run with the request's version with ErrStop, if it is
// running. It does not wait: the run reports stopped and its Run stream gets
// Finished as usual.
func (s *Server) Stop(ctx context.Context, req *driverv1.StopRequest) (*driverv1.StopResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &driverv1.StopResponse{Stopped: s.stop(req.GetVersion())}, nil
}

// stop cancels the active run if it has the given version and is still
// running. It is called with s.mu held.
func (s *Server) stop(version int64) bool {
	r := s.active
	if r == nil || r.version != version {
		return false
	}
	select {
	case <-r.done:
		return false
	default:
	}
	r.cancel(agent.ErrStop)
	return true
}

// Shutdown stops the active run, if any, and waits for it to finish and
// report, or for ctx to expire. No run starts after Shutdown is called.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if s.active != nil {
		s.stop(s.active.version)
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Env returns a run's environment: environ, then spec's env, then its
// secrets, so the run's values win over the driver process's.
func Env(environ []string, spec *driverv1.RunSpec) []string {
	env := slices.Clone(environ)
	env = append(env, envx.FromMap(spec.GetEnv())...)
	env = append(env, envx.FromMap(spec.GetSecrets())...)
	return env
}

// Secrets returns the values a run masks in the log it ships: the workspace
// secrets, plus the task token under "token". The token goes last, so a
// workspace secret named "token" cannot displace it.
func Secrets(spec *driverv1.RunSpec) map[string]string {
	secrets := maps.Clone(spec.GetSecrets())
	if secrets == nil {
		secrets = map[string]string{}
	}
	secrets["token"] = spec.GetToken()
	return secrets
}
