package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/eventfile"
	"github.com/gritzapp/gritz/internal/runner/logship"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// testTaskVersion is the version returned by setupDriver's GetTask mock. The
// driver reads it once at the top of Run and stamps it on every runner event.
const testTaskVersion = 7

// setupDriver writes cfg for task 1 in a temporary config dir and returns a
// driver backed by an empty event file and a mock client whose
// SubmitRunnerEvents always acks.
func setupDriver(t *testing.T, cfg *Config) (*Driver, *gritzclient.ClientMock) {
	t.Helper()
	store := ConfigStore(t.TempDir())
	assert.NilError(t, store.Save(1, cfg))
	events, err := eventfile.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	assert.NilError(t, err)
	mock := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		// run() forks on shell_session; an empty one takes the normal agent path.
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{Id: req.Id, Version: testTaskVersion},
			}, nil
		},
		// A first (non-wake) run fetches the task's standing links for the prompt.
		ListLinksFunc: func(_ context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
	}
	// Log is required; tests that don't inspect output use the discard log.
	// TestDriverRun_LogsToSink overrides it with a file-backed one.
	return &Driver{TaskID: 1, Client: mock, Log: DiscardDriverLog, Config: store, Events: events}, mock
}

func TestDriverRun(t *testing.T) {
	t.Parallel()
	// Arrange
	driver, mock := setupDriver(t, &Config{Type: TypeDummy})

	// Act
	err := driver.Run(t.Context())

	// Assert - started then stopped, both stamped with the fetched task version
	assert.NilError(t, err)
	assert.DeepEqual(t,
		mock.SubmittedRunnerEvents(),
		[]*gritzv1.RunnerEvent{
			{TaskId: 1, Version: testTaskVersion, Event: "started"},
			{TaskId: 1, Version: testTaskVersion, Event: "stopped"},
		},
		protocmp.Transform(),
	)
}

// instruction returns an instruction event with id and text.
func instruction(id int64, text string) *gritzv1.Event {
	return &gritzv1.Event{
		Id:      id,
		Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: text}},
	}
}

// runPrompt runs driver once with a fresh log file and returns the prompt the
// dummy agent logged.
func runPrompt(t *testing.T, driver *Driver) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "log")
	driver.Log = OpenDriverLog(logPath, nil)
	assert.NilError(t, driver.Run(t.Context()))
	assert.NilError(t, driver.Log.Close())
	got, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	_, prompt, ok := strings.Cut(string(got), "dummy agent received prompt")
	assert.Assert(t, ok, "no prompt in log: %s", got)
	return prompt
}

func TestDriverRun_PromptFromEventFile(t *testing.T) {
	t.Parallel()
	// Arrange - the first run's file holds an instruction and a command event
	driver, _ := setupDriver(t, &Config{Type: TypeDummy})
	_, err := driver.Events.Append([]*gritzv1.Event{
		instruction(5, "first-instruction"),
		{Id: 6, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START}}},
	})
	assert.NilError(t, err)

	// Act - the first run
	first := runPrompt(t, driver)

	// Assert - the brief carries the instruction, and the cursor is the last
	// stored event, of any type
	assert.Assert(t, cmp.Contains(first, "first-instruction"))
	cfg, err := driver.Config.Load(1)
	assert.NilError(t, err)
	assert.Equal(t, cfg.PromptedEventID, int64(6))

	// Arrange - an instruction committed late is stored after the prompted
	// events, with an id below them
	_, err = driver.Events.Append([]*gritzv1.Event{instruction(4, "late-instruction")})
	assert.NilError(t, err)

	// Act - the wake
	wake := runPrompt(t, driver)

	// Assert - the wake carries only the late instruction
	assert.Assert(t, cmp.Contains(wake, "late-instruction"))
	assert.Assert(t, !strings.Contains(wake, "first-instruction"), "wake re-prompted an event: %s", wake)
	cfg, err = driver.Config.Load(1)
	assert.NilError(t, err)
	assert.Equal(t, cfg.PromptedEventID, int64(4))
}

func TestDriverRun_PromptedEventNotAdvancedOnError(t *testing.T) {
	t.Parallel()
	// Arrange - a run whose agent errors. The read advances the cursor in
	// memory, but a failed run must leave the saved cursor untouched
	// (at-least-once: the next run reads from the same position).
	driver, _ := setupDriver(t, &Config{
		Type:  TypeDummy,
		Dummy: &DummyOptions{Error: "dummy agent failed on purpose"},
	})
	_, err := driver.Events.Append([]*gritzv1.Event{instruction(1, "do the thing")})
	assert.NilError(t, err)

	// Act - the driver reports the failure and exits 0, but the run did not succeed.
	assert.NilError(t, driver.Run(t.Context()))

	// Assert - the cursor was NOT advanced past its pre-run value.
	cfg, err := driver.Config.Load(1)
	assert.NilError(t, err)
	assert.Equal(t, cfg.PromptedEventID, int64(0))
}

func TestDriverRun_AgentError(t *testing.T) {
	t.Parallel()
	// Arrange
	driver, mock := setupDriver(t, &Config{
		Type:  TypeDummy,
		Dummy: &DummyOptions{Commands: []string{"false"}},
	})

	// Act
	err := driver.Run(t.Context())

	// Assert - the failure was reported and acked, so the driver exits 0; both
	// events carry the fetched task version (the failed reason is the dynamic
	// agent error, ignored here).
	assert.NilError(t, err)
	assert.DeepEqual(t,
		mock.SubmittedRunnerEvents(),
		[]*gritzv1.RunnerEvent{
			{TaskId: 1, Version: testTaskVersion, Event: "started"},
			{TaskId: 1, Version: testTaskVersion, Event: "failed"},
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.RunnerEvent{}, "reason"),
	)
}

func TestDriverRun_AgentConfiguredError(t *testing.T) {
	t.Parallel()
	// Arrange - the dummy agent is configured to return an error string
	driver, mock := setupDriver(t, &Config{
		Type:  TypeDummy,
		Dummy: &DummyOptions{Error: "dummy agent failed on purpose"},
	})

	// Act
	err := driver.Run(t.Context())

	// Assert - the failure was reported and acked, so the driver exits 0
	assert.NilError(t, err)
	assert.DeepEqual(t,
		mock.SubmittedRunnerEvents(),
		[]*gritzv1.RunnerEvent{
			{TaskId: 1, Version: testTaskVersion, Event: "started"},
			{TaskId: 1, Version: testTaskVersion, Event: "failed"},
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.RunnerEvent{}, "reason"),
	)
}

func TestDriverRun_SetupCommandError(t *testing.T) {
	t.Parallel()
	// Arrange
	driver, mock := setupDriver(t, &Config{
		Type:     TypeDummy,
		Commands: []string{"false"},
	})

	// Act
	err := driver.Run(t.Context())

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		mock.SubmittedRunnerEvents(),
		[]*gritzv1.RunnerEvent{
			{TaskId: 1, Version: testTaskVersion, Event: "started"},
			{TaskId: 1, Version: testTaskVersion, Event: "failed"},
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.RunnerEvent{}, "reason"),
	)
}

func TestDriverRun_Stop(t *testing.T) {
	t.Parallel()
	// Arrange - the dummy agent sleeps until the run context is cancelled
	driver, mock := setupDriver(t, &Config{
		Type:  TypeDummy,
		Dummy: &DummyOptions{Sleep: -1},
	})
	ctx, cancel := context.WithCancelCause(t.Context())
	mock.SubmitRunnerEventsFunc = func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		if req.Events[0].Event == "started" {
			cancel(ErrStop)
		}
		return &gritzv1.SubmitRunnerEventsResponse{}, nil
	}

	// Act
	err := driver.Run(ctx)

	// Assert - a graceful stop is reported as stopped, on a context that
	// outlives the cancelled run context
	assert.NilError(t, err)
	assert.DeepEqual(t,
		mock.SubmittedRunnerEvents(),
		[]*gritzv1.RunnerEvent{
			{TaskId: 1, Version: testTaskVersion, Event: "started"},
			{TaskId: 1, Version: testTaskVersion, Event: "stopped"},
		},
		protocmp.Transform(),
	)
}

func TestDriverRun_StartedSubmitError(t *testing.T) {
	t.Parallel()
	// Arrange
	driver, mock := setupDriver(t, &Config{Type: TypeDummy})
	mock.SubmitRunnerEventsFunc = func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		return nil, errors.New("server unreachable")
	}

	// Act
	err := driver.Run(t.Context())

	// Assert
	assert.ErrorContains(t, err, "failed to submit started event")
}

func TestDriverRun_StoppedSubmitError(t *testing.T) {
	t.Parallel()
	// Arrange
	driver, mock := setupDriver(t, &Config{Type: TypeDummy})
	mock.SubmitRunnerEventsFunc = func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		if req.Events[0].Event == "stopped" {
			return nil, errors.New("server unreachable")
		}
		return &gritzv1.SubmitRunnerEventsResponse{}, nil
	}

	// Act
	err := driver.Run(t.Context())

	// Assert - an unacked terminal submit exits non-zero
	assert.ErrorContains(t, err, "failed to submit stopped event")
}

func TestDriverRun_FailedSubmitError(t *testing.T) {
	t.Parallel()
	// Arrange
	driver, mock := setupDriver(t, &Config{
		Type:  TypeDummy,
		Dummy: &DummyOptions{Commands: []string{"false"}},
	})
	mock.SubmitRunnerEventsFunc = func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		if req.Events[0].Event == "failed" {
			return nil, errors.New("server unreachable")
		}
		return &gritzv1.SubmitRunnerEventsResponse{}, nil
	}

	// Act
	err := driver.Run(t.Context())

	// Assert - both the agent error and the lost report surface
	assert.ErrorContains(t, err, "dummy command failed")
	assert.ErrorContains(t, err, "failed to submit failed event")
}

func TestDriverRun_GetTaskError(t *testing.T) {
	t.Parallel()
	// Arrange - GetTask (hoisted to the top of Run) fails before any event
	driver, mock := setupDriver(t, &Config{Type: TypeDummy})
	mock.GetTaskFunc = func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
		return nil, errors.New("server unreachable")
	}

	// Act
	err := driver.Run(t.Context())

	// Assert - the error is returned and no events were emitted; the runner's
	// TaskRunner.Run backstop reports the lost run via the non-zero exit code.
	assert.ErrorContains(t, err, "failed to get task")
	assert.Assert(t, cmp.Len(mock.SubmittedRunnerEvents(), 0))
}

func TestDriverRun_LogsToSink(t *testing.T) {
	t.Parallel()
	// Arrange - a driver teeing into a real append-only log file, wired as the
	// command does in production via OpenDriverLog.
	driver, _ := setupDriver(t, &Config{Type: TypeDummy})
	logPath := filepath.Join(t.TempDir(), "log")
	driver.Log = OpenDriverLog(logPath, nil)
	t.Cleanup(func() { _ = driver.Log.Close() })

	// Act - two runs against the same file
	assert.NilError(t, driver.Run(t.Context()))
	assert.NilError(t, driver.Run(t.Context()))

	// Assert - the run delimiter, the driver's slog lines, and the agent's own
	// slog lines (the logger is threaded through to the agent) all reach the
	// log, and the second run appended a second delimiter below the first.
	got, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(got), "==== run version=7 pid="))
	assert.Assert(t, cmp.Contains(string(got), "loaded config"))
	assert.Assert(t, cmp.Contains(string(got), "dummy agent received prompt"))
	assert.Equal(t, strings.Count(string(got), "==== run version="), 2)
}

func TestDriverRun_SetupCommandOutputTeed(t *testing.T) {
	t.Parallel()
	// Arrange - a setup command that writes to stdout and stderr then fails,
	// with the driver teeing into a real append-only log file.
	driver, _ := setupDriver(t, &Config{
		Type:     TypeDummy,
		Commands: []string{"echo out-marker; echo err-marker >&2; false"},
	})
	logPath := filepath.Join(t.TempDir(), "log")
	driver.Log = OpenDriverLog(logPath, nil)
	t.Cleanup(func() { _ = driver.Log.Close() })

	// Act
	assert.NilError(t, driver.Run(t.Context()))

	// Assert - the failing command's stdout and stderr land in the log next to
	// the setup-failure the operator would otherwise see with no output.
	got, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(got), "out-marker"))
	assert.Assert(t, cmp.Contains(string(got), "err-marker"))
}

func TestDriverRun_ShipsLogToServer(t *testing.T) {
	t.Parallel()
	// Arrange - a failing dummy agent teeing into a real append-only log file
	// with a shipper spliced into the sink, wired as the command does in
	// production. The shipper's background sender is deliberately left
	// unstarted, so the only thing that ships anything is Run's own end-of-run
	// flush — which is what is under test.
	driver, mock := setupDriver(t, &Config{
		Type:  TypeDummy,
		Dummy: &DummyOptions{Error: "dummy agent failed on purpose"},
	})
	mock.AppendLogChunkFunc = func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
		return &gritzv1.AppendLogChunkResponse{}, nil
	}
	logPath := filepath.Join(t.TempDir(), "log")
	driver.Log = OpenDriverLog(logPath, logship.New(mock, logship.Options{TaskID: 1}))
	t.Cleanup(func() { _ = driver.Log.Close() })
	// A line emitted before the run, while the version is still unknown.
	_, err := io.WriteString(driver.Log.Sink(), "preamble\n")
	assert.NilError(t, err)

	// Act
	assert.NilError(t, driver.Run(t.Context()))

	// Assert - the server received /gritz/log byte for byte, including the
	// terminal failure line the flush deliberately runs after.
	file, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(file), "task failed"))
	assert.Equal(t, mock.ShippedLog(), string(file))

	// Assert - chunks are stamped with the version current when the drain cut
	// them. Nothing drained before StartRun here, so the pre-run preamble
	// shares the run's first chunk and is stamped with it.
	chunks := mock.AppendedLogChunks()
	assert.Assert(t, strings.HasPrefix(string(chunks[0].GetData()), "preamble\n"))
	assert.Assert(t, cmp.Contains(string(chunks[0].GetData()), "run version="))
	for _, chunk := range chunks {
		assert.Equal(t, chunk.GetVersion(), int64(testTaskVersion))
	}
}

// TestDriverRun_MasksSecretsInShippedLog asserts the declared secret's value
// reaches /gritz/log raw but never leaves the sandbox: the shipped copy carries
// only the marker. The setup command echoes the value the way a failed
// authenticated clone or a `set -x` step would.
func TestDriverRun_MasksSecretsInShippedLog(t *testing.T) {
	t.Parallel()
	// Arrange
	const secret = "ghp_S3CR3TV4LU3"
	driver, mock := setupDriver(t, &Config{
		Type:     TypeDummy,
		Commands: []string{"echo cloning with " + secret},
	})
	mock.AppendLogChunkFunc = func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
		return &gritzv1.AppendLogChunkResponse{}, nil
	}
	logPath := filepath.Join(t.TempDir(), "log")
	driver.Log = OpenDriverLog(logPath, logship.New(mock, logship.Options{
		TaskID:  1,
		Secrets: map[string]string{"GH_TOKEN": secret},
	}))

	// Act - Close as the driver command defers it, draining the shipper
	assert.NilError(t, driver.Run(t.Context()))
	assert.NilError(t, driver.Log.Close())

	// Assert - the in-sandbox log keeps full fidelity
	file, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(file), secret))

	// Assert - the server got the same lines with the value masked
	shipped := mock.ShippedLog()
	assert.Assert(t, cmp.Contains(shipped, "cloning with [gritz:masked GH_TOKEN]"))
	assert.Assert(t, !strings.Contains(shipped, secret), "shipped log leaked the secret")
}

// TestDriverRun_MasksTokenInShippedLog asserts the Copilot MCP config dump —
// which discloses the driver's own task token as the injected gritz server's
// --token argument — still ships, with the token masked. It is the mask, not a
// deleted log line, that protects the shipped branch.
func TestDriverRun_MasksTokenInShippedLog(t *testing.T) {
	t.Parallel()
	// Arrange - a copilot agent whose binary exits non-zero, so the run reaches
	// the mcp config dump without needing a real copilot install.
	const token = "eyJhbGciOiJIUzI1NiJ9.dGFzay10b2tlbg.c2lnbmF0dXJl"
	driver, mock := setupDriver(t, &Config{
		Type:    TypeCopilot,
		Copilot: &CopilotOptions{Bin: "false"},
		McpServers: map[string]McpServer{
			"gritz": {
				Type:    "stdio",
				Command: "gritz",
				Args:    []string{"tool", "agent-mcp", "--task", "1", "--token", token},
			},
		},
	})
	mock.AppendLogChunkFunc = func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
		return &gritzv1.AppendLogChunkResponse{}, nil
	}
	logPath := filepath.Join(t.TempDir(), "log")
	driver.Log = OpenDriverLog(logPath, logship.New(mock, logship.Options{
		TaskID:  1,
		Secrets: map[string]string{"token": token},
	}))

	// Act
	assert.NilError(t, driver.Run(t.Context()))
	assert.NilError(t, driver.Log.Close())

	// Assert - the in-sandbox log holds the live token, as it already did
	file, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(file), token))

	// Assert - the shipped copy keeps the dump, minus the credential
	shipped := mock.ShippedLog()
	assert.Assert(t, cmp.Contains(shipped, "mcp config"))
	assert.Assert(t, cmp.Contains(shipped, "[gritz:masked token]"))
	assert.Assert(t, !strings.Contains(shipped, token), "shipped log leaked the task token")
}

func TestDriverSetup_Cancel(t *testing.T) {
	t.Parallel()
	// Arrange - the trailing "true" keeps sh from exec'ing sleep directly, so
	// the sleep is a child of sh holding the stdout pipe open.
	driver, _ := setupDriver(t, &Config{
		Type:     TypeDummy,
		Commands: []string{"sleep 30; true"},
	})
	cfg, err := driver.Config.Load(1)
	assert.NilError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	// Act
	start := time.Now()
	err = driver.setup(ctx, cfg)

	// Assert - the setup command was interrupted rather than run to completion
	assert.ErrorContains(t, err, "setup command 0 failed")
	assert.Assert(t, time.Since(start) < 10*time.Second, "setup took %s", time.Since(start))
}
