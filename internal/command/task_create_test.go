package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestParseVars(t *testing.T) {
	t.Parallel()
	// Arrange
	path := filepath.Join(t.TempDir(), "notes.txt")
	assert.NilError(t, os.WriteFile(path, []byte("line 1\nline 2\n"), 0o600))

	// Act
	got, err := parseVars([]string{
		"JIRA_ISSUE=APP-142",
		"LABEL=a,b=c",
		"EMPTY=",
		"NOTES=@" + path,
		"STDIN=@-",
	}, strings.NewReader("from stdin"))

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, map[string]string{
		"JIRA_ISSUE": "APP-142",
		"LABEL":      "a,b=c",
		"EMPTY":      "",
		"NOTES":      "line 1\nline 2\n",
		"STDIN":      "from stdin",
	})
}

func TestParseVars_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		err  string
	}{
		{"no equals", []string{"JIRA_ISSUE"}, "want NAME=VALUE"},
		{"no name", []string{"=APP-142"}, "want NAME=VALUE"},
		{"duplicate", []string{"A=1", "A=2"}, "set more than once"},
		{"stdin twice", []string{"A=@-", "B=@-"}, "only one variable can read stdin"},
		{"missing file", []string{"A=@" + filepath.Join(t.TempDir(), "missing")}, "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseVars(tt.args, strings.NewReader(""))
			assert.ErrorContains(t, err, tt.err)
		})
	}
}
