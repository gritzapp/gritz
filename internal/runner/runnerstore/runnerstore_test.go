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

func TestAppendEvents_NewTask(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })

	// Act - tasks 10 and 20 appear in the first page, and the second page
	// has more of their events and task 30's first.
	first, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		},
		"page-1",
	)
	assert.NilError(t, err)
	second, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
			{Id: 5, TaskId: 30, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
			{Id: 6, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "f"}}},
		},
		"page-2",
	)
	assert.NilError(t, err)

	// Assert - each task is returned once, in order of its first event.
	assert.DeepEqual(t, first, []runnerstore.TaskChange{{ID: 10}, {ID: 20}})
	assert.DeepEqual(t, second, []runnerstore.TaskChange{{ID: 30}})
}

func TestAppendEvents_ArchiveAndUnarchive(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	_, err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
		},
		"page-1",
	)
	assert.NilError(t, err)

	// Act - archive, unarchive, then auto-archive, one page each.
	archived, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
		},
		"page-2",
	)
	assert.NilError(t, err)
	unarchived, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
		},
		"page-3",
	)
	assert.NilError(t, err)
	autoArchived, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 4, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED}}},
		},
		"page-4",
	)
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t,
		[][]runnerstore.TaskChange{archived, unarchived, autoArchived},
		[][]runnerstore.TaskChange{
			{{ID: 10, Archived: true}},
			{{ID: 10}},
			{{ID: 10, Archived: true}},
		},
	)
}

func TestAppendEvents_NewAndArchived(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })

	// Act - task 10 is created and archived within one page.
	changes, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
		},
		"page-1",
	)
	assert.NilError(t, err)

	// Assert - task 10 was never active, so it is neither returned nor
	// unarchived.
	assert.DeepEqual(t, changes, []runnerstore.TaskChange{{ID: 20}})
	ids, err := store.UnarchivedTaskIDs(t.Context())
	assert.NilError(t, err)
	assert.DeepEqual(t, ids, []int64{20})
}

func TestUnarchivedTaskIDs(t *testing.T) {
	t.Parallel()
	// Arrange - tasks 30, 10 and 20 are created, 20 is archived, and 30 is
	// archived and then unarchived.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	_, err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
			{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
			{Id: 3, TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED}}},
		},
		"page-1",
	)
	assert.NilError(t, err)
	_, err = store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			{Id: 5, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			{Id: 6, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
		},
		"page-2",
	)
	assert.NilError(t, err)

	// Act
	ids, err := store.UnarchivedTaskIDs(t.Context())

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, ids, []int64{10, 30})
}
