package validatescript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
)

// waitFinished calls Validate until it stops returning ErrInProgress and
// returns that result.
func waitFinished(t *testing.T, s *Script) error {
	t.Helper()
	var err error
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		err = s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{})
		if errors.Is(err, awsmicrovm.ErrInProgress) {
			return poll.Continue("script still running")
		}
		return poll.Success()
	}, poll.WithTimeout(10*time.Second), poll.WithDelay(10*time.Millisecond))
	return err
}

func TestNew_Missing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validate")

	_, err := New(t.Context(), Options{Path: path})

	assert.ErrorContains(t, err, path)
}

func TestNew_NotExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validate")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644))

	_, err := New(t.Context(), Options{Path: path})

	assert.ErrorContains(t, err, "not executable")
}

func TestNew_Directory(t *testing.T) {
	path := t.TempDir()

	_, err := New(t.Context(), Options{Path: path})

	assert.ErrorContains(t, err, "not a regular file")
}

func TestValidate_InProgressRunsOnce(t *testing.T) {
	// Arrange: the script records each start, then waits for release.
	dir := t.TempDir()
	path := filepath.Join(dir, "validate")
	assert.NilError(t, os.WriteFile(path, []byte(`#!/bin/sh
echo run >> "$DIR/runs"
while [ ! -e "$DIR/release" ]; do sleep 0.01; done
`), 0o755))
	s, err := New(t.Context(), Options{Path: path, Environ: []string{"DIR=" + dir, "PATH=" + os.Getenv("PATH")}})
	assert.NilError(t, err)

	// Act
	first := s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{})
	second := s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{})
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o644))
	final := waitFinished(t, s)

	// Assert
	assert.ErrorIs(t, first, awsmicrovm.ErrInProgress)
	assert.ErrorIs(t, second, awsmicrovm.ErrInProgress)
	assert.NilError(t, final)
	runs, err := os.ReadFile(filepath.Join(dir, "runs"))
	assert.NilError(t, err)
	assert.Equal(t, string(runs), "run\n")
}

func TestValidate_ExitZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validate")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	s, err := New(t.Context(), Options{Path: path})
	assert.NilError(t, err)

	assert.ErrorIs(t, s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{}), awsmicrovm.ErrInProgress)
	assert.NilError(t, waitFinished(t, s))

	assert.NilError(t, s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{}))
	assert.NilError(t, s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{}))
}

func TestValidate_ExitOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validate")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	s, err := New(t.Context(), Options{Path: path})
	assert.NilError(t, err)

	assert.ErrorIs(t, s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{}), awsmicrovm.ErrInProgress)
	assert.ErrorContains(t, waitFinished(t, s), "exit status 1")

	assert.ErrorContains(t, s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{}), "exit status 1")
	assert.ErrorContains(t, s.Validate(t.Context(), awsmicrovm.ValidateHookRequest{}), "exit status 1")
}
