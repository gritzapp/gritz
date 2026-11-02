package e2e

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/poll"
)

// RunnerOptions configures the runner and its workspace; zero values take
// defaults.
type RunnerOptions struct {
	// Concurrency is the runner's slots; default 2.
	Concurrency int
	// Commands are the workspace's setup commands.
	Commands []string
	// Secrets are the workspace's secrets.
	Secrets map[string]string
	// PollInterval is the runner's; default 1s, a backstop to notifications.
	PollInterval time.Duration
}

// Runner is the harness's runner: runner.Serve on a goroutine.
type Runner struct {
	t    *testing.T
	opts runner.ServeOptions

	// cancel stops Serve; nil while the runner is stopped.
	cancel context.CancelFunc
	// done is closed when Serve returns.
	done chan struct{}
}

// newRunner starts a runner for org on b, with one workspace whose agent is
// the puppet pointed at a, and waits until it has registered that workspace.
// Cleanup stops it.
func newRunner(t *testing.T, org *Org, b *Backend, a *Agent, opts *RunnerOptions, log *slog.Logger) *Runner {
	t.Helper()
	r := &Runner{t: t, opts: runner.ServeOptions{
		Client:    org.runner,
		ServerURL: org.server.URL,
		Token:     org.runnerKey,
		Backend:   b,
		StateDB:   filepath.Join(t.TempDir(), "runner.db"),
		Workspaces: &workspace.Config{Workspaces: map[string]workspace.Workspace{
			DefaultWorkspace: {
				Agent: workspace.Agent{
					Type:   agent.TypePuppet,
					Puppet: &workspace.PuppetConfig{URL: a.url},
				},
				Commands: opts.Commands,
				Secrets:  opts.Secrets,
			},
		}},
		RunnerID:     DefaultRunner,
		Concurrency:  cmp.Or(opts.Concurrency, 2),
		PollInterval: cmp.Or(opts.PollInterval, time.Second),
		LostTimeout:  2 * time.Second,
		ReadyTimeout: 2 * time.Second,
		Log:          log,
	}}
	r.Start()
	t.Cleanup(r.Stop)
	// CreateTask needs the workspace the runner registers on startup. The
	// check runs on poll's goroutine, where org.ListWorkspaces's t.Fatal would
	// leave WaitOn waiting out its timeout: it returns the error instead.
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		resp, err := org.Client.ListWorkspaces(t.Context(), &gritzv1.ListWorkspacesRequest{})
		if err != nil {
			return poll.Error(fmt.Errorf("ListWorkspaces: %w", err))
		}
		if !slices.ContainsFunc(resp.Workspaces, func(ws *gritzv1.RegisteredWorkspace) bool {
			return ws.RunnerId == DefaultRunner && ws.Name == DefaultWorkspace
		}) {
			return poll.Continue("runner %s has not registered workspace %s", DefaultRunner, DefaultWorkspace)
		}
		return poll.Success()
	}, org.pollOps()...)
	return r
}

// Start runs Serve on a goroutine, on the runner's runner.db and backend. Its
// context isn't the test's, which ends before cleanup starts: the runner is
// stopped in cleanup's order. The runner must be stopped.
func (r *Runner) Start() {
	if r.cancel != nil {
		panic("e2e: Start on a running runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Stopping the runner while it starts up fails the startup; only a
		// fatal error, or one the harness didn't cause, fails the test.
		err := runner.Serve(ctx, r.opts)
		if err != nil && (ctx.Err() == nil || errors.As(err, new(runner.FatalError))) {
			r.t.Errorf("runner: %v", err)
		}
	}()
	r.cancel, r.done = cancel, done
}

// Stop stops the runner and waits for Serve to return. Stopping a stopped
// runner does nothing, so cleanup can stop a runner the test stopped.
func (r *Runner) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel, r.done = nil, nil
}

// Restart stops the runner and starts a new one on the same runner.db and
// backend.
func (r *Runner) Restart() {
	r.Stop()
	r.Start()
}
