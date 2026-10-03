package agent

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

// TestClaudeGolden feeds a real captured `claude --output-format stream-json`
// session through handleStreamEvent and golden-asserts the rendered log output.
// Regenerate the golden with: go test ./internal/runner/agent/ -run TestClaudeGolden -update
func TestClaudeGolden(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "claude-session.jsonl"))
	assert.NilError(t, err)
	defer f.Close()

	// Render through a TextHandler with the (non-deterministic) time attr
	// dropped so the golden output is stable.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	agent := &ClaudeAgent{log: &DriverLog{Logger: *log}}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		agent.handleStreamEvent(sc.Bytes())
	}
	assert.NilError(t, sc.Err())

	golden.Assert(t, buf.String(), "claude-session.golden")
}
