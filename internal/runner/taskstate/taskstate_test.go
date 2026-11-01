package taskstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

func TestWriteRead(t *testing.T) {
	// Arrange
	s, err := Open(t.TempDir())
	assert.NilError(t, err)
	rec := Record{
		TaskID:  42,
		Version: 7,
		Type:    "docker",
		ID:      "container-abc",
		Data:    json.RawMessage(`{"image_arn":"arn:x","nested":{"k":1}}`),
	}

	// Act
	assert.NilError(t, s.Write(rec))
	got, ok, err := s.Read(42)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, rec)
}

func TestRead_Absent(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	_, ok, err := s.Read(99)

	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestWrite_Overwrite(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)
	assert.NilError(t, s.Write(Record{TaskID: 7, Type: "docker", ID: "old"}))

	assert.NilError(t, s.Write(Record{TaskID: 7, Type: "docker", ID: "new"}))

	got, ok, err := s.Read(7)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, got.ID, "new")
}

func TestRemove(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)
	assert.NilError(t, s.Write(Record{TaskID: 5, Type: "docker", ID: "c5"}))

	assert.NilError(t, s.Remove(5))

	_, ok, err := s.Read(5)
	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestRemove_Absent(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	// Removing an absent task is a no-op, not an error.
	assert.NilError(t, s.Remove(123))
}

func TestList(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Write(Record{TaskID: 1, Type: "docker", ID: "c1"}))
	assert.NilError(t, s.Write(Record{TaskID: 2, Type: "docker", ID: "c2"}))
	assert.NilError(t, s.Write(Record{TaskID: 3, Type: "docker", ID: "c3"}))

	// Drop stray files that must be ignored by List.
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".tmp-leftover"), []byte("garbage"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "not-a-number.json"), []byte("{}"), 0o644))

	// Act
	records, err := s.List()

	// Assert
	assert.NilError(t, err)
	var ids []string
	for _, rec := range records {
		ids = append(ids, rec.ID)
	}
	sort.Strings(ids)
	assert.DeepEqual(t, ids, []string{"c1", "c2", "c3"})
}

func TestWrite_ConcurrentDifferentTasks(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			assert.NilError(t, s.Write(Record{
				TaskID: id,
				Type:   "docker",
				ID:     "c" + string(rune('0'+id%10)),
			}))
		}(int64(i))
	}
	wg.Wait()

	records, err := s.List()
	assert.NilError(t, err)
	assert.Equal(t, len(records), n)
}
