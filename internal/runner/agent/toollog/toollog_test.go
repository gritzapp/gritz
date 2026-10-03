package toollog

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSummarizeInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "nil falls back to empty",
			input: nil,
			want:  "",
		},
		{
			name:  "empty falls back to empty",
			input: map[string]any{},
			want:  "",
		},
		{
			name:  "string without space is unquoted",
			input: map[string]any{"file_path": "internal/runner/agent/claude.go"},
			want:  "file_path=internal/runner/agent/claude.go",
		},
		{
			name:  "string with space is quoted",
			input: map[string]any{"title": "Add summaries"},
			want:  `title="Add summaries"`,
		},
		{
			name:  "bool and number render as-is",
			input: map[string]any{"subscribe": true, "count": float64(42)},
			want:  "count=42 subscribe=true",
		},
		{
			name:  "float keeps decimal",
			input: map[string]any{"ratio": float64(1.5)},
			want:  "ratio=1.5",
		},
		{
			name:  "keys are sorted",
			input: map[string]any{"b": "2", "a": "1", "c": "3"},
			want:  "a=1 b=2 c=3",
		},
		{
			name:  "scalar array joins with spaces",
			input: map[string]any{"command": []any{"go", "test", "./..."}},
			want:  `command="go test ./..."`,
		},
		{
			name:  "single element scalar array is unquoted",
			input: map[string]any{"command": []any{"ls"}},
			want:  "command=ls",
		},
		{
			name:  "non-scalar array collapses to count",
			input: map[string]any{"items": []any{map[string]any{"x": 1}, "y"}},
			want:  "items=[2 items]",
		},
		{
			name:  "object is not expanded",
			input: map[string]any{"opts": map[string]any{"deep": true}},
			want:  "opts={…}",
		},
		{
			name:  "null values are skipped",
			input: map[string]any{"a": nil, "b": "keep"},
			want:  "b=keep",
		},
		{
			name:  "only-null falls back to empty",
			input: map[string]any{"a": nil},
			want:  "",
		},
		{
			name:  "multi-line and whitespace runs collapse",
			input: map[string]any{"text": "line one\n\tline   two\r\nthree"},
			want:  `text="line one line two three"`,
		},
		{
			name:  "redaction placeholder renders unquoted",
			input: map[string]any{"new_string": truncatedPlaceholder},
			want:  "new_string=<truncated>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Summarize(tt.input)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestSummarizeInput_PerValueTruncation(t *testing.T) {
	t.Parallel()
	// Arrange
	long := strings.Repeat("x", maxValueLen+50)

	// Act
	got := Summarize(map[string]any{"v": long})

	// Assert
	want := "v=" + strings.Repeat("x", maxValueLen) + "…"
	assert.Equal(t, got, want)
}

func TestSummarizeInput_OverallTruncation(t *testing.T) {
	t.Parallel()
	// Arrange: many short keys whose combined length exceeds the overall cap.
	input := map[string]any{}
	for i := 0; i < 60; i++ {
		// keys k00..k59 keep deterministic sorted order and each value is short.
		input[string(rune('a'+i/26))+string(rune('a'+i%26))] = "vvvvv"
	}

	// Act
	got := Summarize(input)

	// Assert
	r := []rune(got)
	assert.Equal(t, len(r), maxSummaryLen+1) // capped runes plus the ellipsis
	assert.Equal(t, string(r[len(r)-1]), "…")
}

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input map[string]any
		keys  []string
		want  map[string]any
	}{
		{
			name:  "replaces present keys",
			input: map[string]any{"old_string": "a", "new_string": "b", "file_path": "f"},
			keys:  []string{"old_string", "new_string"},
			want:  map[string]any{"old_string": truncatedPlaceholder, "new_string": truncatedPlaceholder, "file_path": "f"},
		},
		{
			name:  "ignores absent keys",
			input: map[string]any{"file_path": "f"},
			keys:  []string{"content"},
			want:  map[string]any{"file_path": "f"},
		},
		{
			name:  "nil map is safe",
			input: nil,
			keys:  []string{"content"},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Redact(tt.input, tt.keys...)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}
