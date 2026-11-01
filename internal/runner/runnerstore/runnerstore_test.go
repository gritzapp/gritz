package runnerstore_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/x/sqlite"
)

// dump renders the database's user_version, then the sql of every
// sqlite_master object in name order, one per line.
func dump(t *testing.T, db *sql.DB) string {
	t.Helper()
	var version int
	assert.NilError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	var b strings.Builder
	fmt.Fprintf(&b, "user_version = %d\n", version)
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY name`)
	assert.NilError(t, err)
	defer rows.Close()
	for rows.Next() {
		var stmt string
		assert.NilError(t, rows.Scan(&stmt))
		b.WriteString(stmt + "\n")
	}
	assert.NilError(t, rows.Err())
	return b.String()
}

// TestOpen_Schema pins the schema the migrations build, so every new
// migration shows up as a diff. Regenerate the golden with:
// go test ./internal/runner/runnerstore/ -run TestOpen_Schema -update
func TestOpen_Schema(t *testing.T) {
	t.Parallel()
	// Arrange
	path := filepath.Join(t.TempDir(), "runner.db")

	// Act
	store, err := runnerstore.Open(path)
	assert.NilError(t, err)
	assert.NilError(t, store.Close())

	// Assert
	db, err := sqlite.Open(path, sqlite.Options{})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, db.Close()) })
	golden.Assert(t, dump(t, db), "schema.golden")
}

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
	err = store.AppendEvents(t.Context(),
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
