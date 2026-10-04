package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent/agentprompt"
	"github.com/gritzapp/gritz/internal/shell"
)

type Driver struct {
	TaskID int64
	Client gritzclient.Client
	// Log bundles the driver's structured logger with the append-only
	// /gritz/log sink it tees setup command and Claude CLI output into (see
	// proposals/implemented/driver-logs-to-sandbox.md). It is required: the
	// command builds it via agent.OpenDriverLog, and tests use
	// agent.DiscardDriverLog. os.Stdout/os.Stderr stay in every tee, so docker
	// logs output is unchanged.
	Log    *DriverLog
	Config ConfigStore // where the task config file lives

	// ServerURL and Token are the driver's own server credentials, reused to dial
	// the shell relay WebSocket when the task is a debug-shell run. They mirror
	// the values passed to gritzclient.New for Client above.
	ServerURL string
	Token     string

	// Env is the run's environment: the complete environment of the agent CLI
	// and the setup commands, and what $VARs in the config's cwd expand against.
	// Nothing is inherited from the driver's own environment, so a nil Env is an
	// empty one. `gritz driver` sets it to os.Environ().
	Env []string
}

// Run executes the task and reports its outcome to the server. The driver
// owns the started / stopped / failed runner events (see the
// driver-owned-events proposal); it returns an error only when it could not
// report the outcome itself, so the runner's monitor reads the exit code as
// a single bit meaning "did the driver report?".
func (d *Driver) Run(ctx context.Context) error {
	// Events are submitted on the parent context, which survives the SIGTERM
	// cancellation below — the terminal events go out after the run context
	// is torn down, bounded by the client's HTTP timeout.
	eventCtx := ctx

	// Set up SIGTERM handler to cancel with ErrStop
	ctx, cancel := context.WithCancelCause(ctx)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	go func() {
		<-sigCh
		d.Log.Info("received SIGTERM, stopping agent")
		cancel(ErrStop)
	}()
	defer signal.Stop(sigCh)

	// Fetch the task once at the top of the run, before any event is emitted.
	// task.Version is this run's version — stamped on every runner event below
	// so the server's stale guard drops events from a superseded run instead of
	// clobbering the live one. The same response is reused for the shell-session
	// fork in run, so this is a single fetch.
	//
	// A GetTask failure returns here before started is emitted: the driver exits
	// non-zero and the runner's supervise backstop reports the lost run. A failed
	// GetTask means the driver has no working server connection to report through
	// anyway (see the proposal's resolved open question).
	resp, err := d.Client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: d.TaskID})
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}
	task := resp.GetTask()
	version := task.GetVersion()

	// Write a run delimiter before the first event so an operator can find run
	// boundaries in the single append-only log (runs are not split per file).
	// It goes to os.Stderr (docker logs) and the sink (the /gritz/log file)
	// alike; with DiscardDriverLog it writes only to stderr.
	d.Log.StartRun(version)

	// Report started: replaces the startup ping — an acked submit proves the
	// connection, token, server, and DB are all healthy.
	if err := d.submit(eventCtx, model.RunnerEvent{TaskID: d.TaskID, Version: version, Event: model.RunnerEventStarted}); err != nil {
		return err
	}

	err = d.run(ctx, task)
	if err != nil && context.Cause(ctx) == ErrStop {
		d.Log.Info("agent stopped gracefully")
		err = nil
	}
	event := model.RunnerEvent{TaskID: d.TaskID, Version: version, Event: model.RunnerEventStopped}
	if err != nil {
		d.Log.Error("task failed", "err", err)
		// The error already distinguishes setup failures from agent failures
		// (different wrapped errors), so it carries that context into the
		// timeline as the failure reason.
		event.Event = model.RunnerEventFailed
		event.Reason = err.Error()
	}
	// The terminal ack decides the exit code: acked means the outcome is
	// durably recorded and the driver exits 0 even on agent failure; a lost
	// report exits non-zero so the monitor's "failed" fires.
	serr := d.submit(eventCtx, event)
	// Ship the tail of the log last, after the terminal event, so the "task
	// failed" / "agent stopped" lines written above are in the buffer and go
	// out with it. eventCtx is deliberate for the same reason the events use
	// it: it survives the SIGTERM cancellation.
	d.flushLog(eventCtx)
	if serr != nil {
		return errors.Join(err, serr)
	}
	return nil
}

// flushLog drains the log shipper before the driver exits, under a short
// deadline. It is best-effort and never affects the run's outcome: on deadline
// the unshipped tail is given up on and the complete log is still in
// /gritz/log for as long as the sandbox lives.
func (d *Driver) flushLog(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, logFlushTimeout)
	defer cancel()
	if err := d.Log.Flush(ctx); err != nil {
		// Not through d.Log: its handler writes into the sink the shipper is
		// teed into, so reporting a failed flush there would buffer a line that
		// can no longer be shipped. os.Stderr is outside the tee.
		fmt.Fprintf(os.Stderr, "gritz: %v\n", err)
	}
}

// submit reports a runner event for the driver's task and waits for the ack.
// The SubmitRunnerEvents handler commits the transaction before returning,
// so a nil error means the state transition is durable.
func (d *Driver) submit(ctx context.Context, event model.RunnerEvent) error {
	// Version 0 bypasses the version check (spontaneous events).
	_, err := d.Client.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{event.Proto()},
	})
	if err != nil {
		return fmt.Errorf("failed to submit %s event: %w", event.Event, err)
	}
	return nil
}

// run forks into one of two mutually exclusive modes: a debug-shell run when
// the task carries a shell_session, or the normal agent path otherwise. A
// sandbox run is one mode, chosen once at startup (see the design in
// proposals/draft/driver-reverse-shell.md). The fork lives inside run so a
// shell run emits the same started/stopped/failed lifecycle events as an agent
// run. The task is fetched once by Run and passed in, so this reuses that
// single read rather than fetching again.
func (d *Driver) run(ctx context.Context, task *gritzv1.Task) error {
	if session := task.GetShellSession(); session != "" {
		return shell.Serve(ctx, shell.ServeOptions{
			ServerURL: d.ServerURL,
			Token:     d.Token,
			Session:   session,
			Log:       &d.Log.Logger,
		})
	}
	return d.runAgent(ctx, task)
}

// runAgent executes the setup commands and the agent prompt. This is the normal
// (non-shell) sandbox run. task is the one Run fetched at the top of the run; it
// carries the id/name rendered into the wake header line.
func (d *Driver) runAgent(ctx context.Context, task *gritzv1.Task) error {
	// Load config
	cfg, err := d.Config.Load(d.TaskID)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	d.Log.Info("loaded config",
		"cwd", cfg.Cwd,
		"commands", cfg.Commands,
		"mcp_servers", len(cfg.McpServers),
		"setup_completed", cfg.SetupCommandsCompleted,
		"started", cfg.Started,
	)

	if err := d.setup(ctx, cfg); err != nil {
		return err
	}

	// Fetch new events and drain to the tail before running the agent. This runs
	// on every run (first run and wake) uniformly: it pages ListEventsByTask
	// forward from cfg.NextEventToken (empty on the first run) to the tail,
	// returning the instruction + external events accumulated across the walk
	// (report/lifecycle/link events are filtered out for injection, but the token
	// still advances over the full stream). The advanced cursor is held on cfg but
	// persisted only by the post-run Save below, so a run that fails before that
	// Save leaves the stored cursor untouched and the next run re-fetches from the
	// same position (at-least-once). See
	// proposals/draft/wake-prompt-event-injection.md.
	events, token, err := d.drainEvents(ctx, cfg)
	if err != nil {
		return err
	}
	cfg.NextEventToken = token

	// Fetch the task's standing links for the first-run prompt. This runs only on
	// the first run (!cfg.Started): the first-run branch of the template renders
	// the brief in place of the get_my_task bootstrap instruction, so the agent
	// learns its task without a tool round-trip. The brief's events are the
	// instruction+external slice already drained above (on the first run
	// cfg.NextEventToken is empty, so the drain starts at the head of the stream
	// and returns exactly that slice); only the links need a fresh read. A wake
	// run leaves promptLinks nil and is completely unchanged (the wake branch
	// renders Events instead). See proposals/draft/first-run-brief-injection.md.
	var promptLinks []*gritzv1.TaskLink
	if !cfg.Started {
		resp, err := d.Client.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: d.TaskID})
		if err != nil {
			return fmt.Errorf("failed to fetch task links: %w", err)
		}
		promptLinks = resp.GetLinks()
	}

	// Start agent
	a, err := NewAgent(Options{
		Type:       cfg.Type,
		Cwd:        os.Expand(cfg.Cwd, func(key string) string { return envLookup(d.Env, key) }),
		Env:        d.Env,
		Verbose:    cfg.Verbose,
		McpServers: cfg.McpServers,
		Claude:     cfg.Claude,
		Codex:      cfg.Codex,
		Copilot:    cfg.Copilot,
		Cursor:     cfg.Cursor,
		Sloppy:     cfg.Sloppy,
		Dummy:      cfg.Dummy,
		Log:        d.Log,
	})
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}
	defer a.Close()

	// Compose the prompt's event stream so the template loops it once, the same way
	// on both paths. The drained events are the same slice both times: on a wake
	// they are the events pending since the last cursor; on the first run (empty
	// cursor) they are the instruction+external brief from the head of the stream.
	// The links are attached only on the first run. A wake with nothing pending
	// renders without either.
	prompt, err := agentprompt.Render(agentprompt.Options{
		Started: cfg.Started,
		Prompt:  cfg.Prompt,
		Task:    task, // header line, and gates the first-run brief
		Events:  events,
		Links:   promptLinks,
	})
	if err != nil {
		return fmt.Errorf("failed to build prompt: %w", err)
	}

	if err := a.Prompt(ctx, prompt, cfg.Started); err != nil {
		return err
	}

	// Save config. The event cursor advanced in memory during the drain above but
	// is persisted only here, after a.Prompt returned without error, so a run that
	// failed mid-prompt leaves the stored cursor untouched (at-least-once).
	cfg.Started = true
	if err := d.Config.Save(d.TaskID, cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	d.Log.Info("Task completed successfully.")
	return nil
}

// setup runs the setup commands listed in cfg.Commands, resuming from
// cfg.SetupCommandsCompleted. After each successful command, the updated
// count is persisted via d.Config.Save so a restart can pick up where the
// previous run left off.
func (d *Driver) setup(ctx context.Context, cfg *Config) error {
	// Defensive clamp: if the saved count exceeds the current command
	// list, reset to 0 and re-run from the beginning.
	if cfg.SetupCommandsCompleted > len(cfg.Commands) {
		cfg.SetupCommandsCompleted = 0
	}
	for i := cfg.SetupCommandsCompleted; i < len(cfg.Commands); i++ {
		command := cfg.Commands[i]
		d.Log.Info("Running setup command", "index", i, "command", command)
		c := exec.CommandContext(ctx, "sh", "-c", command)
		c.Env = cmdEnv(d.Env)
		// Tee the command's output into the log sink so an opaque setup failure
		// ("setup command N failed") has the command's actual stdout/stderr
		// sitting next to it in /gritz/log. os.Stdout/os.Stderr stay wired so
		// docker logs is unchanged.
		c.Stdout = d.Log.Stdout()
		c.Stderr = d.Log.Stderr()
		// Run the command in its own process group so cancellation reaches the
		// processes sh spawns, not just sh itself. Otherwise the children keep
		// the output pipes open and Run blocks until they finish on their own.
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.Cancel = func() error {
			return syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
		}
		c.WaitDelay = 5 * time.Second
		if err := c.Run(); err != nil {
			return fmt.Errorf("setup command %d failed: %w", i, err)
		}
		cfg.SetupCommandsCompleted = i + 1
		if err := d.Config.Save(d.TaskID, cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	}
	return nil
}

// eventPageSize bounds each ListEventsByTask page the drain walks. A non-zero
// size selects the paged live-follow path (an empty page_token with size 0
// takes the legacy unpaged path instead).
const eventPageSize = 50

// drainEvents walks the task's event stream forward from cfg.NextEventToken to
// the tail via gritzclient.ListEventsByTask, filtered server-side (via the types
// filter) to the injectable arms — to-agent instructions and self-contained
// external triggers. It returns the events accumulated across the walk along with
// the final next_page_token — the cursor position after the current filtered tail.
//
// The cursor tracks the filtered stream: because event ids are monotonic, every
// future injectable event has a higher id than the saved token and is delivered
// on a later wake, so pushing the filter server-side never drops one.
func (d *Driver) drainEvents(ctx context.Context, cfg *Config) ([]*gritzv1.Event, string, error) {
	token := cfg.NextEventToken
	var events []*gritzv1.Event
	req := &gritzv1.ListEventsByTaskRequest{
		TaskId:    d.TaskID,
		PageSize:  eventPageSize,
		PageToken: cfg.NextEventToken,
		Types:     []string{model.EventTypeInstruction, model.EventTypeExternal},
	}
	for resp, err := range gritzclient.ListEventsByTask(ctx, d.Client, req) {
		if err != nil {
			return nil, "", fmt.Errorf("failed to fetch events: %w", err)
		}
		token = resp.GetNextPageToken()
		events = append(events, resp.GetEvents()...)
	}
	d.Log.Info("drained events", "count", len(events))
	return events, token, nil
}
