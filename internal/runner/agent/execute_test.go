package agent

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/redact"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// testTask is the task every test executes.
var testTask = &gritzv1.Task{Id: 1, Version: 7}

// setupExecute writes cfg for task 1 in a temporary config dir and returns
// options backed by a mock client serving a first run.
func setupExecute(t *testing.T, cfg *Config) (ExecuteOptions, *gritzclient.ClientMock) {
	t.Helper()
	store := ConfigStore(t.TempDir())
	assert.NilError(t, store.Save(1, cfg))
	mock := &gritzclient.ClientMock{
		// A first (non-wake) run seeds the event cursor from the tail token.
		ListEventsByTaskFunc: func(_ context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return &gritzv1.ListEventsByTaskResponse{NextPageToken: "tail-token"}, nil
		},
		// A first (non-wake) run fetches the task's standing links for the prompt.
		ListLinksFunc: func(_ context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
	}
	// Log is required; tests that don't inspect output use the discard log.
	return ExecuteOptions{Client: mock, Log: DiscardDriverLog, Config: store}, mock
}

// openLog points opts at a real append-only log file, wired as the driver
// server does via OpenDriverLog, and returns its path.
func openLog(t *testing.T, opts *ExecuteOptions, ship io.Writer) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "log")
	opts.Log = OpenDriverLog(logPath, ship)
	t.Cleanup(func() { _ = opts.Log.Close() })
	return logPath
}

func TestExecute(t *testing.T) {
	t.Parallel()
	// Arrange
	opts, _ := setupExecute(t, &Config{Type: TypeDummy})

	// Act
	err := Execute(t.Context(), testTask, opts)

	// Assert - the run is recorded as started, with the event cursor at the tail
	assert.NilError(t, err)
	cfg, err := opts.Config.Load(1)
	assert.NilError(t, err)
	assert.Assert(t, cfg.Started)
	assert.Equal(t, cfg.NextEventToken, "tail-token")
}

func TestExecute_DrainsEventsToTail(t *testing.T) {
	t.Parallel()
	// Arrange - a run whose event stream spans three pages: two with More=true
	// then a final one (More=false) that marks the tail. The drain must follow
	// next_page_token across all three and persist the final one.
	opts, mock := setupExecute(t, &Config{Type: TypeDummy})
	pages := map[string]*gritzv1.ListEventsByTaskResponse{
		"":   {NextPageToken: "p1", More: true},
		"p1": {NextPageToken: "p2", More: true},
		"p2": {NextPageToken: "tail"}, // More defaults to false → tail reached
	}
	// The iterator mutates the request's PageToken in place, so the recorded call
	// requests all alias one pointer — capture the token at call time instead.
	var seenTokens []string
	mock.ListEventsByTaskFunc = func(_ context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
		seenTokens = append(seenTokens, req.GetPageToken())
		return pages[req.GetPageToken()], nil
	}

	// Act
	assert.NilError(t, Execute(t.Context(), testTask, opts))

	// Assert - the three pages were walked in order and the final tail token was
	// persisted as the new cursor.
	assert.DeepEqual(t, seenTokens, []string{"", "p1", "p2"})
	cfg, err := opts.Config.Load(1)
	assert.NilError(t, err)
	assert.Equal(t, cfg.NextEventToken, "tail")
}

func TestDrainEvents_RequestsServerSideTypeFilter(t *testing.T) {
	t.Parallel()
	// Arrange - the drain pushes the instruction + external filter to the server
	// via the request's Types field (the server does the filtering), then returns
	// the events it gets back and the tail token.
	page := &gritzv1.ListEventsByTaskResponse{
		NextPageToken: "tail",
		Events: []*gritzv1.Event{
			{Id: 1, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "do the thing"}}},
			{Id: 3, Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{Description: "PR comment"}}},
		},
	}
	var gotTypes []string
	mock := &gritzclient.ClientMock{
		ListEventsByTaskFunc: func(_ context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			gotTypes = req.GetTypes()
			return page, nil
		},
	}
	e := &execution{ExecuteOptions: ExecuteOptions{Client: mock, Log: DiscardDriverLog}, taskID: 1}

	// Act
	events, token, err := e.drainEvents(t.Context(), &Config{})

	// Assert - the request carried the type filter, and the returned events and
	// tail token passed through.
	assert.NilError(t, err)
	assert.DeepEqual(t, gotTypes, []string{"instruction", "external"})
	assert.Equal(t, token, "tail")
	assert.Assert(t, cmp.Len(events, 2))
	assert.Equal(t, events[0].GetId(), int64(1))
	assert.Equal(t, events[1].GetId(), int64(3))
}

func TestExecute_EventTokenNotAdvancedOnError(t *testing.T) {
	t.Parallel()
	// Arrange - a run whose agent errors. The drain computes a fresh tail token,
	// but a failed run must leave the saved cursor untouched (at-least-once: the
	// next run re-fetches from the same position).
	opts, mock := setupExecute(t, &Config{
		Type:           TypeDummy,
		NextEventToken: "old-token",
		Dummy:          &DummyOptions{Error: "dummy agent failed on purpose"},
	})
	mock.ListEventsByTaskFunc = func(_ context.Context, _ *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
		return &gritzv1.ListEventsByTaskResponse{NextPageToken: "new-token"}, nil
	}

	// Act
	err := Execute(t.Context(), testTask, opts)

	// Assert - the run failed, and the cursor was NOT advanced past its
	// pre-run value.
	assert.ErrorContains(t, err, "dummy agent failed on purpose")
	cfg, err := opts.Config.Load(1)
	assert.NilError(t, err)
	assert.Equal(t, cfg.NextEventToken, "old-token")
}

func TestExecute_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"agent command", &Config{Type: TypeDummy, Dummy: &DummyOptions{Commands: []string{"false"}}}, "dummy command failed"},
		{"agent error", &Config{Type: TypeDummy, Dummy: &DummyOptions{Error: "dummy agent failed on purpose"}}, "dummy agent failed on purpose"},
		{"setup command", &Config{Type: TypeDummy, Commands: []string{"false"}}, "setup command 0 failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts, _ := setupExecute(t, tt.cfg)

			err := Execute(t.Context(), testTask, opts)

			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestExecute_LogsToSink(t *testing.T) {
	t.Parallel()
	// Arrange
	opts, _ := setupExecute(t, &Config{Type: TypeDummy})
	logPath := openLog(t, &opts, nil)

	// Act
	assert.NilError(t, Execute(t.Context(), testTask, opts))

	// Assert - the execution's slog lines and the agent's own (the logger is
	// threaded through to the agent) both reach the log.
	got, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(got), "loaded config"))
	assert.Assert(t, cmp.Contains(string(got), "dummy agent received prompt"))
}

func TestExecute_SetupCommandOutputTeed(t *testing.T) {
	t.Parallel()
	// Arrange - a setup command that writes to stdout and stderr then fails
	opts, _ := setupExecute(t, &Config{
		Type:     TypeDummy,
		Commands: []string{"echo out-marker; echo err-marker >&2; false"},
	})
	logPath := openLog(t, &opts, nil)

	// Act
	assert.ErrorContains(t, Execute(t.Context(), testTask, opts), "setup command 0 failed")

	// Assert - the failing command's stdout and stderr land in the log next to
	// the setup-failure the operator would otherwise see with no output.
	got, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(got), "out-marker"))
	assert.Assert(t, cmp.Contains(string(got), "err-marker"))
}

// TestExecute_MasksTokenInShippedLog asserts the Copilot MCP config dump —
// which discloses the task token as the injected gritz server's --token
// argument — still reaches the shipped branch, with the token masked. It is
// the mask, not a deleted log line, that protects the shipped branch.
func TestExecute_MasksTokenInShippedLog(t *testing.T) {
	t.Parallel()
	// Arrange - a copilot agent whose binary exits non-zero, so the run reaches
	// the mcp config dump without needing a real copilot install.
	const token = "eyJhbGciOiJIUzI1NiJ9.dGFzay10b2tlbg.c2lnbmF0dXJl"
	opts, _ := setupExecute(t, &Config{
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
	var shipped bytes.Buffer
	mask := redact.NewWriter(&shipped, map[string]string{"token": token})
	logPath := openLog(t, &opts, mask)

	// Act
	assert.Assert(t, Execute(t.Context(), testTask, opts) != nil)
	assert.NilError(t, mask.Flush())

	// Assert - the in-sandbox log holds the live token, as it already did
	file, err := os.ReadFile(logPath)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(file), token))

	// Assert - the shipped copy keeps the dump, minus the credential
	assert.Assert(t, cmp.Contains(shipped.String(), "mcp config"))
	assert.Assert(t, cmp.Contains(shipped.String(), "[gritz:masked token]"))
	assert.Assert(t, !strings.Contains(shipped.String(), token), "shipped log leaked the task token")
}

func TestExecuteSetup_Cancel(t *testing.T) {
	t.Parallel()
	// Arrange - the trailing "true" keeps sh from exec'ing sleep directly, so
	// the sleep is a child of sh holding the stdout pipe open.
	opts, _ := setupExecute(t, &Config{
		Type:     TypeDummy,
		Commands: []string{"sleep 30; true"},
	})
	e := &execution{ExecuteOptions: opts, taskID: 1}
	cfg, err := opts.Config.Load(1)
	assert.NilError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	// Act
	start := time.Now()
	err = e.setup(ctx, cfg)

	// Assert - the setup command was interrupted rather than run to completion
	assert.ErrorContains(t, err, "setup command 0 failed")
	assert.Assert(t, time.Since(start) < 10*time.Second, "setup took %s", time.Since(start))
}
