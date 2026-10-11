package agent

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent/agentprompt"
	"github.com/gritzapp/gritz/internal/x/envx"
)

// ExecuteOptions is what Execute needs from the run it executes for.
type ExecuteOptions struct {
	// Client is the run's gritz client, authenticated as the task.
	Client gritzclient.Client
	// Log bundles the run's structured logger with the append-only /gritz/log
	// sink it tees setup command and agent CLI output into (see
	// proposals/implemented/driver-logs-to-sandbox.md). It is required: the
	// driver server builds it via OpenDriverLog, and tests use
	// DiscardDriverLog. os.Stdout/os.Stderr stay in every tee, so docker logs
	// output is unchanged.
	Log *DriverLog
	// Config is where the task config file lives.
	Config ConfigStore
	// Env is the run's environment: the complete environment of the agent CLI
	// and the setup commands, and what $VARs in the config's cwd expand against.
	Env []string
}

// execution is one Execute call: the options plus the task they are for.
type execution struct {
	ExecuteOptions
	taskID int64
}

// Execute runs task's setup commands, resuming after the ones a previous run
// completed, then prompts its agent with the instruction and the events pending
// since the last run. It returns the first error; cancelling ctx interrupts the
// setup commands and the agent (SIGTERM, then SIGKILL after a wait delay).
// Reporting the outcome is the caller's.
func Execute(ctx context.Context, task *gritzv1.Task, opts ExecuteOptions) error {
	e := &execution{ExecuteOptions: opts, taskID: task.GetId()}
	return e.run(ctx, task)
}

// run executes the setup commands and the agent prompt. task carries the
// id/name rendered into the wake header line.
func (e *execution) run(ctx context.Context, task *gritzv1.Task) error {
	// Load config
	cfg, err := e.Config.Load(e.taskID)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	e.Log.Info("loaded config",
		"cwd", cfg.Cwd,
		"commands", cfg.Commands,
		"mcp_servers", len(cfg.McpServers),
		"setup_completed", cfg.SetupCommandsCompleted,
		"started", cfg.Started,
	)

	if err := e.setup(ctx, cfg); err != nil {
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
	events, token, err := e.drainEvents(ctx, cfg)
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
		resp, err := e.Client.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: e.taskID})
		if err != nil {
			return fmt.Errorf("failed to fetch task links: %w", err)
		}
		promptLinks = resp.GetLinks()
	}

	// Start agent
	a, err := NewAgent(Options{
		Type:       cfg.Type,
		Cwd:        envx.Expand(cfg.Cwd, e.Env),
		Env:        e.Env,
		Verbose:    cfg.Verbose,
		McpServers: cfg.McpServers,
		Claude:     cfg.Claude,
		Codex:      cfg.Codex,
		Copilot:    cfg.Copilot,
		Cursor:     cfg.Cursor,
		Sloppy:     cfg.Sloppy,
		Dummy:      cfg.Dummy,
		Log:        e.Log,
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
	if err := e.Config.Save(e.taskID, cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	e.Log.Info("Task completed successfully.")
	return nil
}

// setup runs the setup commands listed in cfg.Commands, resuming from
// cfg.SetupCommandsCompleted. After each successful command, the updated
// count is persisted via e.Config.Save so a restart can pick up where the
// previous run left off.
func (e *execution) setup(ctx context.Context, cfg *Config) error {
	// Defensive clamp: if the saved count exceeds the current command
	// list, reset to 0 and re-run from the beginning.
	if cfg.SetupCommandsCompleted > len(cfg.Commands) {
		cfg.SetupCommandsCompleted = 0
	}
	for i := cfg.SetupCommandsCompleted; i < len(cfg.Commands); i++ {
		command := cfg.Commands[i]
		e.Log.Info("Running setup command", "index", i, "command", command)
		c := exec.CommandContext(ctx, "sh", "-c", command)
		c.Env = e.Env
		// Tee the command's output into the log sink so an opaque setup failure
		// ("setup command N failed") has the command's actual stdout/stderr
		// sitting next to it in /gritz/log. os.Stdout/os.Stderr stay wired so
		// docker logs is unchanged.
		c.Stdout = e.Log.Stdout()
		c.Stderr = e.Log.Stderr()
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
		if err := e.Config.Save(e.taskID, cfg); err != nil {
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
func (e *execution) drainEvents(ctx context.Context, cfg *Config) ([]*gritzv1.Event, string, error) {
	token := cfg.NextEventToken
	var events []*gritzv1.Event
	req := &gritzv1.ListEventsByTaskRequest{
		TaskId:    e.taskID,
		PageSize:  eventPageSize,
		PageToken: cfg.NextEventToken,
		Types:     []string{model.EventTypeInstruction, model.EventTypeExternal},
	}
	for resp, err := range gritzclient.ListEventsByTask(ctx, e.Client, req) {
		if err != nil {
			return nil, "", fmt.Errorf("failed to fetch events: %w", err)
		}
		token = resp.GetNextPageToken()
		events = append(events, resp.GetEvents()...)
	}
	e.Log.Info("drained events", "count", len(events))
	return events, token, nil
}
