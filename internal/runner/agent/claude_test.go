package agent

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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
