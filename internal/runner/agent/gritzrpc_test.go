package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/runner/agent/gritzrpc"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// TestMain runs the test binary as a gritzrpc test agent when
// GRITZRPC_TEST_AGENT names a scenario, and runs the tests otherwise.
func TestMain(m *testing.M) {
	if scenario := os.Getenv("GRITZRPC_TEST_AGENT"); scenario != "" {
		runTestAgent(scenario)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runTestAgent speaks gritzrpc on stdio, as the named scenario:
//
//   - ok: writes the run/start params to GRITZRPC_TEST_OUT, sends a log line,
//     sends a request and checks it is answered with method not found, then
//     responds {}.
//   - error: responds with error -32001 "boom".
//   - exit: exits 3 without responding.
//   - noise: prints a non-JSON stdout line, then blocks.
//   - cancel-ok: blocks until run/cancel, logs, then responds {}.
//   - cancel-ignore: ignores run/cancel and blocks until killed.
func runTestAgent(scenario string) {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "test agent: "+format+"\n", args...)
		os.Exit(1)
	}
	var start gritzrpc.Message
	if err := dec.Decode(&start); err != nil || start.Method != gritzrpc.MethodRunStart {
		fail("read run/start: %v %+v", err, start)
	}
	result := gritzrpc.Message{JSONRPC: "2.0", ID: start.ID, Result: json.RawMessage(`{}`)}

	switch scenario {
	case "ok":
		var params gritzrpc.StartParams
		if err := json.Unmarshal(start.Params, &params); err != nil {
			fail("decode run/start params: %v", err)
		}
		data, err := json.Marshal(params)
		if err != nil {
			fail("encode run/start params: %v", err)
		}
		if err := os.WriteFile(os.Getenv("GRITZRPC_TEST_OUT"), data, 0o644); err != nil {
			fail("write params: %v", err)
		}
		logParams, err := json.Marshal(gritzrpc.LogParams{
			Level: "warn",
			Msg:   "step finished",
			Attrs: map[string]any{"step": "plan", "ms": 1532},
		})
		if err != nil {
			fail("encode log params: %v", err)
		}
		_ = enc.Encode(gritzrpc.Message{JSONRPC: "2.0", Method: gritzrpc.MethodLog, Params: logParams})
		id := int64(7)
		_ = enc.Encode(gritzrpc.Message{JSONRPC: "2.0", ID: &id, Method: "tools/list"})
		var reply gritzrpc.Message
		if err := dec.Decode(&reply); err != nil || reply.Error == nil || reply.Error.Code != gritzrpc.CodeMethodNotFound {
			fail("read reply: %v %+v", err, reply)
		}
		_ = enc.Encode(result)
	case "error":
		_ = enc.Encode(gritzrpc.Message{JSONRPC: "2.0", ID: start.ID, Error: &gritzrpc.Error{Code: -32001, Message: "boom"}})
	case "exit":
		os.Exit(3)
	case "noise":
		fmt.Println("this is not json-rpc")
		time.Sleep(time.Hour)
	case "cancel-ok":
		var cancel gritzrpc.Message
		if err := dec.Decode(&cancel); err != nil || cancel.Method != gritzrpc.MethodRunCancel {
			fail("read run/cancel: %v %+v", err, cancel)
		}
		_ = enc.Encode(gritzrpc.Message{JSONRPC: "2.0", Method: gritzrpc.MethodLog, Params: json.RawMessage(`{"msg":"cancelled gracefully"}`)})
		_ = enc.Encode(result)
	case "cancel-ignore":
		time.Sleep(time.Hour)
	default:
		fail("unknown scenario %q", scenario)
	}
}

// newTestGritzRPCAgent returns a gritzrpc agent running the test binary as the
// given scenario, and the buffer it logs to.
func newTestGritzRPCAgent(t *testing.T, scenario string) (*GritzRPCAgent, *bytes.Buffer) {
	t.Helper()
	exe, err := os.Executable()
	assert.NilError(t, err)
	var logBuf bytes.Buffer
	a, err := NewAgent(Options{
		Type:     TypeGritzRPC,
		Cwd:      t.TempDir(),
		Env:      []string{"GRITZRPC_TEST_AGENT=" + scenario},
		Log:      &DriverLog{Logger: *slog.New(slog.NewTextHandler(&logBuf, nil))},
		GritzRPC: &GritzRPCOptions{Command: exe},
	})
	assert.NilError(t, err)
	return a.(*GritzRPCAgent), &logBuf
}

func TestGritzRPCPrompt_Error(t *testing.T) {
	t.Parallel()
	// Arrange
	a, _ := newTestGritzRPCAgent(t, "error")

	// Act
	err := a.Prompt(t.Context(), Turn{})

	// Assert
	assert.Error(t, err, "gritzrpc agent: boom (code -32001)")
}

func TestGritzRPCPrompt_Exit(t *testing.T) {
	t.Parallel()
	// Arrange
	a, _ := newTestGritzRPCAgent(t, "exit")

	// Act
	err := a.Prompt(t.Context(), Turn{})

	// Assert
	assert.Error(t, err, "gritzrpc agent exited without responding: exit status 3")
}

func TestGritzRPCPrompt_Noise(t *testing.T) {
	t.Parallel()
	// Arrange - the agent ignores run/cancel, so the stop needs SIGTERM
	a, _ := newTestGritzRPCAgent(t, "noise")
	a.cancelGrace = 100 * time.Millisecond

	// Act - returns only once the agent is stopped
	err := a.Prompt(t.Context(), Turn{})

	// Assert
	assert.Error(t, err, "gritzrpc agent: invalid stdout line: this is not json-rpc")
}

func TestGritzRPCPrompt_CancelOK(t *testing.T) {
	t.Parallel()
	// Arrange - the default grace, so the agent answers run/cancel itself
	a, logBuf := newTestGritzRPCAgent(t, "cancel-ok")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(ErrStop)

	// Act
	err := a.Prompt(ctx, Turn{})

	// Assert - cancelled despite the agent's successful response
	assert.Assert(t, errors.Is(err, context.Canceled), "err: %v", err)
	assert.Assert(t, cmp.Contains(logBuf.String(), "cancelled gracefully"))
}

func TestGritzRPCPrompt_CancelIgnore(t *testing.T) {
	t.Parallel()
	// Arrange
	a, _ := newTestGritzRPCAgent(t, "cancel-ignore")
	a.cancelGrace = 100 * time.Millisecond
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(ErrStop)

	// Act - returns only once SIGTERM has stopped the agent
	err := a.Prompt(ctx, Turn{})

	// Assert
	assert.Assert(t, errors.Is(err, context.Canceled), "err: %v", err)
}
