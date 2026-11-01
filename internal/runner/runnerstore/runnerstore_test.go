package runnerstore_test

import (
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
)

func TestEventReplicationCursor_Empty(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })

	// Act
	cursor, err := store.EventReplicationCursor(t.Context())

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
	_, err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		},
		"page-1",
	)
	assert.NilError(t, err)
	firstCursor, err := store.EventReplicationCursor(t.Context())
	assert.NilError(t, err)
	_, err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
		},
		"page-2",
	)
	assert.NilError(t, err)

	// Assert - each page's cursor is stored with it.
	assert.Equal(t, firstCursor, "page-1")
	cursor, err := store.EventReplicationCursor(t.Context())
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

func TestAppendEvents_Tasks(t *testing.T) {
	t.Parallel()
	// Arrange - tasks 10, 20 and 30 are started, and 20 is archived.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	_, err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 3, TaskId: 30, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
		},
		"page-1",
	)
	assert.NilError(t, err)

	// Act - a page with only an instruction for 10, a command for the
	// archived 20, an archive and unarchive for 30, and new tasks 40 and 50,
	// which it archives.
	changes, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			{Id: 6, TaskId: 20, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_STOP, Version: 2}}},
			{Id: 7, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			{Id: 8, TaskId: 40, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 9, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
			{Id: 10, TaskId: 50, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 11, TaskId: 50, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
		},
		"page-2",
	)
	assert.NilError(t, err)

	// Assert - each touched task is reported with its state after the page:
	// the command-only page leaves 20 archived, and the archive-only page
	// keeps 30's command. Task 10 isn't touched, and UnarchivedTasks reads
	// the rows written.
	assert.DeepEqual(t,
		changes,
		[]runnerstore.TaskChange{
			{ID: 20, Command: gritzv1.TaskCommand_STOP, Version: 2, Archived: true},
			{ID: 30, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 40, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 50, Command: gritzv1.TaskCommand_START, Version: 1, Archived: true},
		},
	)
	tasks, err := store.UnarchivedTasks(t.Context())
	assert.NilError(t, err)
	assert.DeepEqual(t,
		tasks,
		[]runnerstore.TaskChange{
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 30, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 40, Command: gritzv1.TaskCommand_START, Version: 1},
		},
	)
}
