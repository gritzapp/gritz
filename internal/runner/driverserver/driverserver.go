// Package driverserver implements driver.v1.DriverService, served by
// `gritz driver` inside the sandbox. Runs execute in-process, one at a time.
// The Server holds what outlives a run — the run record, the Run streams, the
// log shipper, provisioning — and each Run is built from its RunSpec: its own
// gritz client, /gritz/log tee, secret mask and environment.
//
// See proposals/draft/driver-server.md. A run reports started / stopped /
// failed to the gritz server itself, and Finished says whether that report was
// acknowledged. Stop ends a run as stopped; Shutdown ends it as failed.
package driverserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"github.com/gritzapp/gritz/internal/x/envx"
	"github.com/gritzapp/gritz/internal/x/netx"
)

// DefaultKeepAlive is how often an open Run stream gets a KeepAlive. A write
// is how the server notices a stream whose runner has gone away, so this bounds
// how long a dead stream blocks the next Run.
const DefaultKeepAlive = 15 * time.Second

// httpShutdownTimeout bounds the HTTP server's shutdown once the active run
// has finished, which leaves only streams sending their Finished.
const httpShutdownTimeout = 5 * time.Second

// ProvisionedPath is the marker a sandbox gets once a run has written its
// RunSpec files. Like the files, it lives under Options.FileRoot.
const ProvisionedPath = "/gritz/.provisioned"

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
	// FileRoot is the directory the RunSpec file paths and ProvisionedPath
	// are relative to. Defaults to "/".
	FileRoot string
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
	// HookAddr, when set, is an address ListenAndServe also serves the AWS
	// Lambda MicroVM lifecycle hooks on, acknowledging every hook: in a
	// MicroVM, the run arrives over driver.v1 and the runner drives suspend
	// and resume, so the hooks have nothing to do.
	HookAddr string
}

// Server implements driverv1connect.DriverServiceHandler.
type Server struct {
	recordPath string
	logPath    string
	config     agent.ConfigStore
	fileRoot   string
	environ    []string
	newClient  func(serverURL, token string) gritzclient.Client
	keepAlive  time.Duration
	log        *slog.Logger
	hookAddr   string

	// shipper mirrors every run's log to the gritz server; each run points it
	// at itself and puts its own mask in front of it. Its sender runs until
	// Shutdown has seen the active run finish.
	shipper     *logship.Shipper
	stopShipper context.CancelFunc

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
	active *Run
	// streaming is true while a Run stream is open.
	streaming bool
	// closed is set by Shutdown; no run starts after it.
	closed bool
}

var _ driverv1connect.DriverServiceHandler = (*Server)(nil)

// New returns a server, reconciling the run record first: a record still
// running means the previous process died mid-run, so it is rewritten as
// finished and unreported.
func New(opts Options) (*Server, error) {
	s := &Server{
		recordPath: cmp.Or(opts.RecordPath, runrecord.Path),
		logPath:    cmp.Or(opts.LogPath, agent.DefaultLogPath),
		config:     cmp.Or(opts.Config, agent.DefaultConfigStore),
		fileRoot:   cmp.Or(opts.FileRoot, "/"),
		environ:    opts.Environ,
		newClient:  opts.NewClient,
		keepAlive:  cmp.Or(opts.KeepAlive, DefaultKeepAlive),
		log:        cmp.Or(opts.Log, slog.Default()),
		hookAddr:   opts.HookAddr,
		shipper:    logship.New(nil, logship.Options{}),
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
	ctx, cancel := context.WithCancel(context.Background())
	s.stopShipper = cancel
	go s.shipper.Run(ctx)
	return s, nil
}

// Handler returns the path and handler to mount the service on.
func (s *Server) Handler() (string, http.Handler) {
	return driverv1connect.NewDriverServiceHandler(s)
}

// ListenAndServe serves the service on addr, a TCP address or unix:///path,
// over HTTP/1.1 and unencrypted HTTP/2, and the hooks on Options.HookAddr if
// set, until ctx is cancelled or either listener fails. A socket file left at
// a unix path is removed before listening, and the socket is removed again on
// shutdown. Once ctx is cancelled it shuts down the active run, waits for it
// to report, however long that takes (the platform's stop timeout bounds it),
// and shuts down the HTTP servers.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.Handle(s.Handler())
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	servers := []*http.Server{{
		Addr:      addr,
		Handler:   mux,
		Protocols: protocols,
	}}
	if s.hookAddr != "" {
		servers = append(servers, &http.Server{
			Addr:    s.hookAddr,
			Handler: &awsmicrovm.Handler{},
		})
	}
	// Closing a unix listener removes its socket file, so every listener is
	// closed on the way out, through its server once it is serving.
	listeners := make([]net.Listener, 0, len(servers))
	for _, hs := range servers {
		l, err := netx.Listen(hs.Addr)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return err
		}
		listeners = append(listeners, l)
	}
	errCh := make(chan error, len(servers))
	for i, hs := range servers {
		go func() {
			if i == 0 {
				s.log.Info("driver server listening", "addr", hs.Addr)
			} else {
				s.log.Info("lambda hooks listening", "addr", hs.Addr)
			}
			errCh <- hs.Serve(listeners[i])
		}()
	}
	select {
	case err := <-errCh:
		for _, hs := range servers {
			_ = hs.Close()
		}
		return err
	case <-ctx.Done():
	}
	s.log.Info("shutting down driver server")
	if err := s.Shutdown(context.Background()); err != nil {
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	var errs []error
	for _, hs := range servers {
		errs = append(errs, hs.Shutdown(shutdownCtx))
	}
	return errors.Join(errs...)
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
func (s *Server) open(req *driverv1.RunRequest) (r *Run, attached bool, err error) {
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

// start provisions the spec's files, writes the run record and starts the run
// in the background. It is called with s.mu held.
func (s *Server) start(version int64, spec *driverv1.RunSpec) (*Run, error) {
	if err := s.provision(spec.GetFiles()); err != nil {
		return nil, err
	}
	rec := runrecord.Record{Version: version, State: runrecord.Running}
	if err := runrecord.Write(s.recordPath, rec); err != nil {
		return nil, err
	}
	s.rec, s.hasRec = rec, true
	r := s.newRun(version, spec)
	s.active = r
	s.runs.Go(func() {
		err := r.execute()
		if err != nil {
			s.log.Error("run not reported", "version", r.version, "err", err)
		}
		s.finish(r, err == nil)
	})
	return r, nil
}

// provision writes files, unless the sandbox has been provisioned already,
// then creates the marker. Files are written once per sandbox, so anything the
// agent changes in them survives later runs.
func (s *Server) provision(files []*driverv1.File) error {
	marker := filepath.Join(s.fileRoot, ProvisionedPath)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("provision: %w", err)
	}
	for _, f := range files {
		if err := writeFile(filepath.Join(s.fileRoot, f.GetPath()), f); err != nil {
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

// finish writes r's outcome to the run record, then finishes r.
func (s *Server) finish(r *Run, reported bool) {
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
	defer s.mu.Unlock()
	s.rec = rec
	r.finish(reported)
}

// Stop ends the run with the request's version with ErrStop, if it is
// running. It does not wait: the run flushes its log, reports stopped, and its
// Run stream gets Finished as usual.
func (s *Server) Stop(ctx context.Context, req *driverv1.StopRequest) (*driverv1.StopResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.active
	stopped := r != nil && r.version == req.GetVersion() && r.Stop()
	return &driverv1.StopResponse{Stopped: stopped}, nil
}

// Shutdown ends the active run, if it is running, with ErrShutdown, and waits
// for it to flush its log and report failed, or for ctx to expire. With no run
// active there is nothing to report. No run starts after Shutdown is called.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if s.active != nil {
		s.active.Shutdown()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.stopShipper()
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
