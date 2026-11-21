package puppet

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/gritzapp/gritz/internal/x/testx"
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

func TestComplete(t *testing.T) {
	t.Parallel()
	// Arrange
	srv, a := setup(t)
	prompt := testx.Go(func() error { return a.Prompt(t.Context(), "do the thing", true) })
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	assert.NilError(t, run.Complete())

	// Assert
	assert.NilError(t, prompt.Wait(t, 5*time.Second))
	assert.Equal(t, run.TaskID, int64(taskID))
	assert.Equal(t, run.Prompt, "do the thing")
	assert.Equal(t, run.Resume, true)
}

func TestFail(t *testing.T) {
	t.Parallel()
	// Arrange
	srv, a := setup(t)
	prompt := testx.Go(func() error { return a.Prompt(t.Context(), "do the thing", false) })
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	assert.NilError(t, run.Fail("run failed on purpose"))

	// Assert
	assert.Error(t, prompt.Wait(t, 5*time.Second), "run failed on purpose")
}

func TestCancel(t *testing.T) {
	t.Parallel()
	// Arrange
	srv, a := setup(t)
	ctx, cancel := context.WithCancel(t.Context())
	prompt := testx.Go(func() error { return a.Prompt(ctx, "do the thing", false) })
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	cancel()

	// Assert
	err = prompt.Wait(t, 5*time.Second)
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	<-run.Cancelled()
	assert.ErrorContains(t, run.Complete(), "already ended")
}

func TestNext_InOrder(t *testing.T) {
	t.Parallel()
	// Arrange - two runs, the second arriving after the first was taken
	srv, a := setup(t)
	prompt1 := testx.Go(func() error { return a.Prompt(t.Context(), "one", false) })
	run1, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)
	prompt2 := testx.Go(func() error { return a.Prompt(t.Context(), "two", true) })

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
	assert.NilError(t, prompt1.Wait(t, 5*time.Second))
	assert.NilError(t, prompt2.Wait(t, 5*time.Second))
}

func TestComplete_Ended(t *testing.T) {
	t.Parallel()
	// Arrange - a completed run
	srv, a := setup(t)
	prompt := testx.Go(func() error { return a.Prompt(t.Context(), "do the thing", false) })
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)
	assert.NilError(t, run.Complete())
	assert.NilError(t, prompt.Wait(t, 5*time.Second))

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
	prompt := testx.Go(func() error { return a.Prompt(t.Context(), "do the thing", false) })
	run, err := srv.Next(t.Context(), taskID)
	assert.NilError(t, err)

	// Act
	srv.Close()

	// Assert
	assert.Error(t, prompt.Wait(t, 5*time.Second), "puppet server closed")
	assert.ErrorContains(t, run.Complete(), "already ended")
	assert.Error(t, a.Prompt(t.Context(), "after close", false), "puppet server closed")
}
