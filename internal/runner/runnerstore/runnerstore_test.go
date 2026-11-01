package runnerstore_test

import (
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
)

func TestCursor_Empty(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })

	// Act
	cursor, err := store.Cursor(t.Context())

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, cursor, "")
}

func TestAppendEvents(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })

	// Act - the first page's cursor is read before the second page is stored.
	err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		},
		"page-1",
	)
	assert.NilError(t, err)
	firstCursor, err := store.Cursor(t.Context())
	assert.NilError(t, err)
	err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
		},
		"page-2",
	)
	assert.NilError(t, err)

	// Assert - each page's cursor is stored with it.
	assert.Equal(t, firstCursor, "page-1")
	cursor, err := store.Cursor(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, cursor, "page-2")

	// Assert - position 0 returns the task's whole log across pages, and a
	// stored id returns only that task's later events.
	events, err := store.TaskEventsAfter(t.Context(), 10, 0)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
		},
		protocmp.Transform(),
	)
	events, err = store.TaskEventsAfter(t.Context(), 10, 1)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
		},
		protocmp.Transform(),
	)
	events, err = store.TaskEventsAfter(t.Context(), 20, 2)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
		},
		protocmp.Transform(),
	)
}
