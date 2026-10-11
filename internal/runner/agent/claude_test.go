package agent

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// TestClaudePrompt_StderrTeed runs a fake `claude` binary that writes a marker
// to stderr and a single stream-json line to stdout, and asserts the stderr
// reaches the log sink while stdout is parsed into a tool summary (via the slog
// logger) and NOT duplicated raw into the sink.
func TestClaudePrompt_StderrTeed(t *testing.T) {
	t.Parallel()
	// Arrange - a fake claude that ignores its args
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"echo claude-stderr-marker >&2\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"hi-from-claude"}]}}'` + "\n"
	assert.NilError(t, os.WriteFile(bin, []byte(script), 0o755))

	// Separate the logger's output (logBuf) from the raw stderr sink so the
	// assertions can tell parsed summaries apart from teed stderr.
	var sink bytes.Buffer
	var logBuf bytes.Buffer
	a := &ClaudeAgent{
		log:     &DriverLog{Logger: *slog.New(slog.NewTextHandler(&logBuf, nil)), sink: &sink},
		cwd:     dir,
		options: &ClaudeOptions{Bin: bin},
	}

	// Act
	assert.NilError(t, a.Prompt(t.Context(), "prompt", false))

	// Assert - stderr is teed into the sink
	assert.Assert(t, cmp.Contains(sink.String(), "claude-stderr-marker"))
	// stdout JSON is parsed into a tool summary on the slog logger...
	assert.Assert(t, cmp.Contains(logBuf.String(), "hi-from-claude"))
	// ...and is NOT duplicated raw into the sink.
	assert.Assert(t, !bytes.Contains(sink.Bytes(), []byte("hi-from-claude")))
}

// TestClaudePrompt_LongLine runs a fake `claude` whose stream has a line far
// over bufio.Scanner's 64 KiB default, followed by more events and then more
// output than a pipe buffers. The events after the long line must still be
// parsed and logged, and Prompt can only return once that output is drained,
// since until then the fake blocks writing it.
func TestClaudePrompt_LongLine(t *testing.T) {
	t.Parallel()
	// Arrange - a fake claude that ignores its args
	dir := t.TempDir()
	long := `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + strings.Repeat("x", 200<<10) + `"}]}}`
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "long.json"), []byte(long+"\n"), 0o644))
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"cat " + filepath.Join(dir, "long.json") + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"after-long-line"}]}}'` + "\n" +
		`i=0; while [ $i -lt 2000 ]; do echo '{"type":"system","subtype":"status","padding":"` + strings.Repeat("p", 1000) + `"}'; i=$((i+1)); done` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"num_turns":3,"duration_ms":1234,"result":"done"}'` + "\n"
	assert.NilError(t, os.WriteFile(bin, []byte(script), 0o755))

	var logBuf bytes.Buffer
	a := &ClaudeAgent{
		log:     &DriverLog{Logger: *slog.New(slog.NewTextHandler(&logBuf, nil))},
		cwd:     dir,
		options: &ClaudeOptions{Bin: bin},
	}

	// Act - bounded, so a fake blocked on an undrained pipe fails the test
	// rather than hanging it
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	err := a.Prompt(ctx, "prompt", false)

	// Assert - the events on both sides of the drained output are logged
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(logBuf.String(), "after-long-line"))
	assert.Assert(t, cmp.Contains(logBuf.String(), "msg=result subtype=success"))
	assert.Assert(t, !strings.Contains(logBuf.String(), "level=ERROR"))
}

func TestClaudeHandleStreamEvent_ResultAndRetry(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  string // the exact log line, time attr dropped; empty for none
	}{
		{
			name:  "result success",
			event: `{"type":"result","subtype":"success","is_error":false,"api_error_status":null,"duration_ms":26362,"num_turns":6,"result":"Done."}`,
			want:  `level=INFO msg=result subtype=success turns=6 duration_ms=26362`,
		},
		{
			name:  "result error subtype",
			event: `{"type":"result","subtype":"error_max_turns","is_error":true,"duration_ms":500,"num_turns":10,"errors":["Reached maximum number of turns (10)"]}`,
			want:  `level=ERROR msg=result subtype=error_max_turns turns=10 duration_ms=500 err="Reached maximum number of turns (10)"`,
		},
		{
			name:  "result success with is_error",
			event: `{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"duration_ms":700,"num_turns":1,"result":"API Error: 529 Overloaded"}`,
			want:  `level=ERROR msg=result subtype=success turns=1 duration_ms=700 err="API Error: 529 Overloaded"`,
		},
		{
			name:  "api retry",
			event: `{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"retry_delay_ms":1200,"error_status":529,"error":"overloaded","uuid":"u","session_id":"s"}`,
			want:  `level=WARN msg=api_retry status=529 err=overloaded attempt=2 max_retries=10 retry_delay_ms=1200`,
		},
		{
			name:  "api retry connection error",
			event: `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":500,"error_status":null,"error":"unknown","uuid":"u","session_id":"s"}`,
			want:  `level=WARN msg=api_retry status=none err=unknown attempt=1 max_retries=10 retry_delay_ms=500`,
		},
		{
			name:  "system init",
			event: `{"type":"system","subtype":"init","cwd":"/root","session_id":"s"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
				Level: slog.LevelDebug,
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey {
						return slog.Attr{}
					}
					return a
				},
			}))
			a := &ClaudeAgent{log: &DriverLog{Logger: *log}}

			// Act
			handled := a.handleStreamEvent([]byte(tt.event))

			// Assert
			assert.Assert(t, handled)
			assert.Equal(t, strings.TrimSpace(buf.String()), tt.want)
		})
	}
}
