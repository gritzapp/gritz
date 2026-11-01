// Package driverserver implements driver.v1.DriverService, served by
// `gritz driver` inside the sandbox. Runs execute in-process, one at a
// time, each built from its RunSpec: its own gritz client, log shipper, secret
// mask and environment.
//
// See proposals/draft/driver-server.md. The driver reports started / stopped /
// failed to the gritz server itself, and Idle says whether that report was
// acknowledged. Every event the runner forwards on a Run stream is stored in
// the driver's event file (internal/runner/eventfile); the driver doesn't act
// on them yet.
package driverserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
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
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/eventfile"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"github.com/gritzapp/gritz/internal/x/envx"
	"github.com/gritzapp/gritz/internal/x/netx"
)

// DefaultKeepAlive is how often an open Run stream gets a KeepAlive. A write
// is how the server notices a stream whose runner has gone away, so this bounds
// how long a dead stream blocks the next Run.
const DefaultKeepAlive = 15 * time.Second

// httpShutdownTimeout bounds the HTTP server's shutdown once the active run
// has finished, which leaves only streams sending their Idle.
const httpShutdownTimeout = 5 * time.Second

// ProvisionedPath is the marker a sandbox gets once a run has written its
// RunSpec files. Like the files, it lives under Options.FileRoot.
const ProvisionedPath = "/gritz/.provisioned"

// Options configures a Server. Every field takes its default when zero.
type Options struct {
	// RecordPath is the run record. Defaults to runrecord.Path.
	RecordPath string
	// EventsPath is the event file. Defaults to eventfile.Path.
	EventsPath string
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
}

// Server implements driverv1connect.DriverServiceHandler. It owns the
// transport; runs are executed by its Executor.
type Server struct {
	exec      *Executor
	events    *eventfile.File
	keepAlive time.Duration
	log       *slog.Logger

	mu sync.Mutex
	// streaming is true while a Run stream is open.
	streaming bool
}

var _ driverv1connect.DriverServiceHandler = (*Server)(nil)

// New returns a server, reconciling the run record first (see NewExecutor).
// It reads the event file.
func New(opts Options) (*Server, error) {
	s := &Server{
		keepAlive: cmp.Or(opts.KeepAlive, DefaultKeepAlive),
		log:       cmp.Or(opts.Log, slog.Default()),
	}
	events, err := eventfile.Open(cmp.Or(opts.EventsPath, eventfile.Path))
	if err != nil {
		return nil, err
	}
	s.events = events
	s.exec, err = NewExecutor(ExecutorOptions{
		RecordPath: opts.RecordPath,
		LogPath:    opts.LogPath,
		Config:     opts.Config,
		FileRoot:   opts.FileRoot,
		Environ:    opts.Environ,
		NewClient:  opts.NewClient,
		Log:        s.log,
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the path and handler to mount the service on.
func (s *Server) Handler() (string, http.Handler) {
	return driverv1connect.NewDriverServiceHandler(s)
}

// ListenAndServe serves the service on addr, a TCP address or unix:///path,
// over HTTP/1.1 and unencrypted HTTP/2, until ctx is cancelled or the listener
// fails. A socket file left at a unix path is removed before listening, and the
// socket is removed again on shutdown. Once ctx is cancelled it stops the
// active run, waits for it to stop and report, however long that takes (the
// platform's stop timeout bounds it), and shuts down the HTTP server.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.Handle(s.Handler())
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	hs := &http.Server{
		Addr:      addr,
		Handler:   mux,
		Protocols: protocols,
	}
	// Closing a unix listener removes its socket file, so the listener is
	// closed on the way out, through the server.
	l, err := netx.Listen(addr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("driver server listening", "addr", addr)
		errCh <- hs.Serve(l)
	}()
	select {
	case err := <-errCh:
		_ = hs.Close()
		return err
	case <-ctx.Done():
	}
	s.log.Info("shutting down driver server")
	if err := s.Shutdown(context.Background()); err != nil {
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	return hs.Shutdown(shutdownCtx)
}

// Run serves the runner's stream for one run. The first message names the
// run to attach to or start, with the spec to start it with; the server
// answers Hello with the id of the last event in its event file, then stores
// every batch of events the runner sends, until the run has ended and its
// Idle has been sent. The run's context is independent of the request's: a
// dropped stream detaches and the run keeps going.
func (s *Server) Run(ctx context.Context, stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]) error {
	req, err := stream.Receive()
	if err != nil {
		return err
	}
	if req.GetEvents() != nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the first message must not carry events"))
	}
	runDone, err := s.open(req)
	if err != nil {
		return err
	}
	defer s.release()
	if err := stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Hello{
		Hello: &driverv1.Hello{LastEventId: s.events.LastID()},
	}}); err != nil {
		return err
	}
	// The batches are stored here rather than by receive, so nothing is
	// stored once Run has returned and released the stream.
	batches := make(chan batch)
	done := make(chan struct{})
	defer close(done)
	go receive(stream, batches, done)
	ticker := time.NewTicker(s.keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-runDone:
			// No run starts while this stream is open, so the record is
			// the one of the run it resolved to.
			rec, _ := s.exec.State()
			return stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Idle{
				Idle: &driverv1.Idle{Version: rec.Version, Reported: rec.Reported},
			}})
		case b := <-batches:
			if errors.Is(b.err, io.EOF) {
				// The runner has nothing more to send; the run goes on.
				batches = nil
				continue
			}
			if b.err != nil {
				return b.err
			}
			if err := s.events.Append(b.events); err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
		case <-ticker.C:
			if err := stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_KeepAlive{
				KeepAlive: &driverv1.KeepAlive{},
			}}); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// batch is what receive read from a Run stream: a batch of events, or why
// the stream ended.
type batch struct {
	events []*gritzv1.Event
	err    error
}

// receive sends every batch of events the runner sends on stream to batches,
// until the stream fails or ends, which it sends as the last batch, or done
// is closed.
func receive(stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse], batches chan<- batch, done <-chan struct{}) {
	for {
		var b batch
		req, err := stream.Receive()
		switch {
		case err != nil:
			b.err = err
		case req.GetEvents() == nil:
			b.err = connect.NewError(connect.CodeInvalidArgument, errors.New("only the first message may omit events"))
		default:
			b.events = req.GetEvents().GetEvents()
		}
		select {
		case batches <- b:
		case <-done:
			return
		}
		if b.err != nil {
			return
		}
	}
}

// open claims the stream and resolves the request to a run, starting it if
// needed, and returns a channel closed once that run has finished. On success
// the caller must release the stream.
func (s *Server) open(req *driverv1.RunRequest) (<-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streaming {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("another Run stream is open"))
	}
	version := req.GetVersion()
	// Only open starts runs, under s.mu, so until the stream is claimed the
	// record can only move from running to finished.
	rec, hasRec := s.exec.State()
	switch {
	case hasRec && rec.Version > version:
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("version %d is stale, the run record is at %d", version, rec.Version))
	case hasRec && rec.Version == version:
		// Either in progress, or ended: in this process, in an earlier one,
		// or marked finished by Reconcile after a crash.
	case hasRec && rec.State == runrecord.Running:
		return nil, connect.NewError(connect.CodeAborted,
			fmt.Errorf("version %d is running", rec.Version))
	case req.GetSpec() == nil:
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("no run with version %d and no spec to start one", version))
	default:
		if err := s.exec.Start(version, req.GetSpec()); errors.Is(err, ErrShutdown) {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		} else if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	s.streaming = true
	return s.exec.Done(), nil
}

func (s *Server) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = false
}

// Stop cancels the run with the request's version with ErrStop, if it is
// running. It does not wait: the run reports stopped and its Run stream gets
// Idle as usual.
func (s *Server) Stop(ctx context.Context, req *driverv1.StopRequest) (*driverv1.StopResponse, error) {
	return &driverv1.StopResponse{Stopped: s.exec.Stop(req.GetVersion())}, nil
}

// Shutdown stops the current run, if any, and waits for it to finish and
// report, or for ctx to expire. No run starts after Shutdown is called.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.exec.Shutdown(ctx)
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
