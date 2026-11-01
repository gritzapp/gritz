package runnerstore_test

import (
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
)

func TestAddRunnerEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	ev := model.RunnerEvent{
		TaskID:  42,
		Event:   model.RunnerEventFailed,
		Version: 7,
		Reason:  "boom",
	}

	// Act
	assert.NilError(t, store.AddRunnerEvent(t.Context(), ev))
	_, got, ok, err := store.OldestRunnerEvent(t.Context())

	// Assert - every column round-trips.
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, ev)
}

func TestOldestRunnerEvent_Empty(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })

	// Act
	_, _, ok, err := store.OldestRunnerEvent(t.Context())

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestOldestRunnerEvent_FIFO(t *testing.T) {
	t.Parallel()
	// Arrange - three events, added out of task id order.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	events := []model.RunnerEvent{
		{TaskID: 3, Event: model.RunnerEventStarted, Version: 1},
		{TaskID: 1, Event: model.RunnerEventStopped, Version: 2},
		{TaskID: 2, Event: model.RunnerEventFailed, Version: 3, Reason: "x"},
	}
	for _, ev := range events {
		assert.NilError(t, store.AddRunnerEvent(t.Context(), ev))
	}

	// Act - take the oldest and delete it until none is left.
	var got []model.RunnerEvent
	for {
		seq, ev, ok, err := store.OldestRunnerEvent(t.Context())
		assert.NilError(t, err)
		if !ok {
			break
		}
		got = append(got, ev)
		assert.NilError(t, store.DeleteRunnerEvent(t.Context(), seq))
	}

	// Assert - they come back in the order they were added.
	assert.DeepEqual(t, got, events)
}

func TestRunnerEvent_DeleteVsDead(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	first := model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}
	second := model.RunnerEvent{TaskID: 2, Event: model.RunnerEventStarted}
	third := model.RunnerEvent{TaskID: 3, Event: model.RunnerEventStarted}
	for _, ev := range []model.RunnerEvent{first, second, third} {
		assert.NilError(t, store.AddRunnerEvent(t.Context(), ev))
	}

	// Act - delete the first and dead-letter the second.
	seq, _, _, err := store.OldestRunnerEvent(t.Context())
	assert.NilError(t, err)
	assert.NilError(t, store.DeleteRunnerEvent(t.Context(), seq))
	seq, _, _, err = store.OldestRunnerEvent(t.Context())
	assert.NilError(t, err)
	assert.NilError(t, store.MarkRunnerEventDead(t.Context(), seq))

	// Assert - neither is pending any more: only the third is left.
	n, err := store.CountRunnerEvents(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	_, got, ok, err := store.OldestRunnerEvent(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, third)
}

func TestRunnerEvent_SeqNotReused(t *testing.T) {
	t.Parallel()
	// Arrange - the only event is added and deleted, emptying the table.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	assert.NilError(t, store.AddRunnerEvent(t.Context(), model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}))
	deleted, _, _, err := store.OldestRunnerEvent(t.Context())
	assert.NilError(t, err)
	assert.NilError(t, store.DeleteRunnerEvent(t.Context(), deleted))

	// Act
	assert.NilError(t, store.AddRunnerEvent(t.Context(), model.RunnerEvent{TaskID: 2, Event: model.RunnerEventStarted}))
	seq, _, _, err := store.OldestRunnerEvent(t.Context())

	// Assert - the new event gets a seq past the deleted one.
	assert.NilError(t, err)
	assert.Assert(t, seq > deleted, "seq %d reused after deleting %d", seq, deleted)
}

func TestRunnerEvent_SurvivesReopen(t *testing.T) {
	t.Parallel()
	// Arrange - two events, one dead-lettered, in a store that is then closed.
	path := filepath.Join(t.TempDir(), "runner.db")
	store, err := runnerstore.Open(path)
	assert.NilError(t, err)
	dead := model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}
	pending := model.RunnerEvent{TaskID: 2, Event: model.RunnerEventFailed, Version: 5, Reason: "boom"}
	assert.NilError(t, store.AddRunnerEvent(t.Context(), dead))
	assert.NilError(t, store.AddRunnerEvent(t.Context(), pending))
	seq, _, _, err := store.OldestRunnerEvent(t.Context())
	assert.NilError(t, err)
	assert.NilError(t, store.MarkRunnerEventDead(t.Context(), seq))
	assert.NilError(t, store.Close())

	// Act
	store, err = runnerstore.Open(path)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	n, err := store.CountRunnerEvents(t.Context())
	assert.NilError(t, err)
	_, got, ok, err := store.OldestRunnerEvent(t.Context())

	// Assert - the pending event is still pending, and the dead one still dead.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, pending)
}
