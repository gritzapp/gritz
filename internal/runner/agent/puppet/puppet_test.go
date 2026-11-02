package puppet

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

const taskID = 42

// setup serves a Server over httptest and returns it with an Agent for task 42
// pointed at it.
func setup(t *testing.T) (*Server, *Agent) {
	t.Helper()
	srv := NewServer()
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	t.Cleanup(srv.Close) // runs first, so no handler is left holding hs.Close
	return srv, New(taskID, Options{URL: hs.URL})
}

// prompt calls a.Prompt in the background and returns its result channel.
func prompt(ctx context.Context, a *Agent, text string, resume bool) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- a.Prompt(ctx, text, resume) }()
	return errc
}

func TestComplete(t *testing.T) {
	t.Parallel()
	// Arrange
	srv, a := setup(t)
	errc := prompt(t.Context(), a, "do the thing", true)
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	assert.NilError(t, run.Complete())

	// Assert
	assert.NilError(t, <-errc)
	assert.Equal(t, run.TaskID, int64(taskID))
	assert.Equal(t, run.Prompt, "do the thing")
	assert.Equal(t, run.Resume, true)
}

func TestFail(t *testing.T) {
	t.Parallel()
	// Arrange
	srv, a := setup(t)
	errc := prompt(t.Context(), a, "do the thing", false)
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	assert.NilError(t, run.Fail("run failed on purpose"))

	// Assert
	assert.Error(t, <-errc, "run failed on purpose")
}

func TestCancel(t *testing.T) {
	t.Parallel()
	// Arrange
	srv, a := setup(t)
	ctx, cancel := context.WithCancel(t.Context())
	errc := prompt(ctx, a, "do the thing", false)
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	cancel()

	// Assert
	err = <-errc
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	<-run.Cancelled()
	assert.ErrorContains(t, run.Complete(), "already ended")
}

func TestNext_InOrder(t *testing.T) {
	t.Parallel()
	// Arrange - two runs, the second arriving after the first was taken
	srv, a := setup(t)
	errc1 := prompt(t.Context(), a, "one", false)
	run1, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)
	errc2 := prompt(t.Context(), a, "two", true)

	// Act
	run2, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, run1.Prompt, "one")
	assert.Equal(t, run2.Prompt, "two")
	assert.Check(t, slices.Equal(srv.Runs(taskID), []*Run{run1, run2}))
	assert.Check(t, is.Len(srv.Runs(taskID+1), 0))
	assert.NilError(t, run1.Complete())
	assert.NilError(t, run2.Complete())
	assert.NilError(t, <-errc1)
	assert.NilError(t, <-errc2)
}

func TestComplete_Ended(t *testing.T) {
	t.Parallel()
	// Arrange - a completed run
	srv, a := setup(t)
	errc := prompt(t.Context(), a, "do the thing", false)
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)
	assert.NilError(t, run.Complete())
	assert.NilError(t, <-errc)

	// Act
	err = run.Complete()

	// Assert
	assert.ErrorContains(t, err, "already ended")
	assert.ErrorContains(t, run.Fail("too late"), "already ended")
}

func TestClose(t *testing.T) {
	t.Parallel()
	// Arrange - an open run
	srv, a := setup(t)
	errc := prompt(t.Context(), a, "do the thing", false)
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	srv.Close()

	// Assert
	assert.Error(t, <-errc, "puppet server closed")
	assert.ErrorContains(t, run.Complete(), "already ended")
	assert.Error(t, a.Prompt(t.Context(), "after close", false), "puppet server closed")
}
