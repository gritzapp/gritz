package eventfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// event returns event id of task 1, an instruction.
func event(id int64) *gritzv1.Event {
	return &gritzv1.Event{
		Id:     id,
		TaskId: 1,
		Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
			Text: "do it\nnow",
		}},
	}
}

func events(ids ...int64) []*gritzv1.Event {
	var out []*gritzv1.Event
	for _, id := range ids {
		out = append(out, event(id))
	}
	return out
}

func open(t *testing.T, path string) *File {
	t.Helper()
	f, err := Open(path)
	assert.NilError(t, err)
	return f
}

func assertAfter(t *testing.T, f *File, id int64, want []*gritzv1.Event) {
	t.Helper()
	got, err := f.After(id)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want, protocmp.Transform(), cmpopts.EquateEmpty())
}

func TestOpen_Absent(t *testing.T) {
	t.Parallel()
	// Arrange
	path := filepath.Join(t.TempDir(), "events.jsonl")

	// Act
	f := open(t, path)

	// Assert
	assert.Equal(t, f.LastID(), int64(0))
	assertAfter(t, f, 0, nil)
}

func TestAppend(t *testing.T) {
	t.Parallel()
	// Arrange
	f := open(t, filepath.Join(t.TempDir(), "events.jsonl"))

	// Act - two batches, in log order, which is not id order
	assert.NilError(t, f.Append(events(5, 3)))
	assert.NilError(t, f.Append(events(8)))

	// Assert - stored in the order they were appended
	assert.Equal(t, f.LastID(), int64(8))
	assertAfter(t, f, 0, events(5, 3, 8))
}

func TestAppend_SkipsStored(t *testing.T) {
	t.Parallel()
	// Arrange
	f := open(t, filepath.Join(t.TempDir(), "events.jsonl"))
	assert.NilError(t, f.Append(events(5, 3)))

	// Act - a batch resending stored events, and one twice
	assert.NilError(t, f.Append(events(3, 5, 9, 9)))
	assert.NilError(t, f.Append(events(9)))

	// Assert
	assert.Equal(t, f.LastID(), int64(9))
	assertAfter(t, f, 0, events(5, 3, 9))
}

func TestOpen_Reopen(t *testing.T) {
	t.Parallel()
	// Arrange - a file a previous process wrote
	path := filepath.Join(t.TempDir(), "events.jsonl")
	assert.NilError(t, open(t, path).Append(events(5, 3)))

	// Act
	f := open(t, path)

	// Assert - the events and the position survive, and stored events are
	// still skipped
	assert.Equal(t, f.LastID(), int64(3))
	assert.NilError(t, f.Append(events(5, 7)))
	assertAfter(t, f, 0, events(5, 3, 7))
}

func TestOpen_TornLine(t *testing.T) {
	t.Parallel()
	// Arrange - a crash mid-append left part of a line
	path := filepath.Join(t.TempDir(), "events.jsonl")
	assert.NilError(t, open(t, path).Append(events(5)))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	assert.NilError(t, err)
	_, err = file.WriteString(`{"id":"6","taskId"`)
	assert.NilError(t, err)
	assert.NilError(t, file.Close())

	// Act
	f := open(t, path)

	// Assert - the torn line is dropped, and appending goes on after it
	assert.Equal(t, f.LastID(), int64(5))
	assert.NilError(t, f.Append(events(6)))
	assertAfter(t, f, 0, events(5, 6))
}

func TestOpen_Corrupt(t *testing.T) {
	t.Parallel()
	// Arrange - a complete line that isn't an event
	path := filepath.Join(t.TempDir(), "events.jsonl")
	assert.NilError(t, os.WriteFile(path, []byte("not json\n"), 0o644))

	// Act
	_, err := Open(path)

	// Assert
	assert.ErrorContains(t, err, "decode line at offset 0")
}

func TestAfter(t *testing.T) {
	t.Parallel()
	f := open(t, filepath.Join(t.TempDir(), "events.jsonl"))
	assert.NilError(t, f.Append(events(5, 3, 8)))
	tests := []struct {
		name string
		id   int64
		want []*gritzv1.Event
	}{
		{"start", 0, events(5, 3, 8)},
		{"middle", 3, events(8)},
		{"last", 8, nil},
		{"not stored", 4, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertAfter(t, f, tt.id, tt.want)
		})
	}
}
