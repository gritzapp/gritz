package runnerstore_test

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
)

func openStore(t *testing.T) *runnerstore.Store {
	t.Helper()
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	return store
}

func TestWriteSandbox(t *testing.T) {
	t.Parallel()
	// Arrange
	store := openStore(t)
	rec := runnerstore.SandboxRecord{
		TaskID:  42,
		Version: 7,
		Handle: backend.Handle{
			Type: "docker",
			ID:   "container-abc",
			Data: json.RawMessage(`{"image_arn":"arn:x","nested":{"k":1}}`),
		},
	}

	// Act
	assert.NilError(t, store.WriteSandbox(t.Context(), rec))
	got, ok, err := store.Sandbox(t.Context(), 42)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, rec)
}

func TestWriteSandbox_NoData(t *testing.T) {
	t.Parallel()
	// Arrange
	store := openStore(t)
	rec := runnerstore.SandboxRecord{TaskID: 1, Version: 2, Handle: backend.Handle{Type: "docker", ID: "c1"}}

	// Act
	assert.NilError(t, store.WriteSandbox(t.Context(), rec))
	got, ok, err := store.Sandbox(t.Context(), 1)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, rec)
}

func TestWriteSandbox_Overwrite(t *testing.T) {
	t.Parallel()
	// Arrange
	store := openStore(t)
	assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{
		TaskID:  7,
		Version: 1,
		Handle:  backend.Handle{Type: "docker", ID: "old", Data: json.RawMessage(`{"a":1}`)},
	}))
	rec := runnerstore.SandboxRecord{
		TaskID:  7,
		Version: 2,
		Handle:  backend.Handle{Type: "lambda-microvm", ID: "new"},
	}

	// Act
	assert.NilError(t, store.WriteSandbox(t.Context(), rec))

	// Assert - every column is replaced, including data with none.
	got, ok, err := store.Sandbox(t.Context(), 7)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, rec)
}

func TestSandbox_Absent(t *testing.T) {
	t.Parallel()
	store := openStore(t)

	_, ok, err := store.Sandbox(t.Context(), 99)

	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestRemoveSandbox(t *testing.T) {
	t.Parallel()
	// Arrange
	store := openStore(t)
	assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{
		TaskID: 5,
		Handle: backend.Handle{Type: "docker", ID: "c5"},
	}))

	// Act
	assert.NilError(t, store.RemoveSandbox(t.Context(), 5))

	// Assert
	_, ok, err := store.Sandbox(t.Context(), 5)
	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestRemoveSandbox_Absent(t *testing.T) {
	t.Parallel()
	store := openStore(t)

	// Removing an absent record is a no-op, not an error.
	assert.NilError(t, store.RemoveSandbox(t.Context(), 123))
}

func TestSandboxes(t *testing.T) {
	t.Parallel()
	// Arrange
	store := openStore(t)
	for _, id := range []int64{3, 1, 2} {
		assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{
			TaskID: id,
			Handle: backend.Handle{Type: "docker", ID: "c" + string(rune('0'+id))},
		}))
	}

	// Act
	recs, err := store.Sandboxes(t.Context())

	// Assert - in ascending task id order.
	assert.NilError(t, err)
	assert.DeepEqual(t, recs, []runnerstore.SandboxRecord{
		{TaskID: 1, Handle: backend.Handle{Type: "docker", ID: "c1"}},
		{TaskID: 2, Handle: backend.Handle{Type: "docker", ID: "c2"}},
		{TaskID: 3, Handle: backend.Handle{Type: "docker", ID: "c3"}},
	})
}

func TestSandboxes_Empty(t *testing.T) {
	t.Parallel()
	store := openStore(t)

	recs, err := store.Sandboxes(t.Context())

	assert.NilError(t, err)
	assert.Equal(t, len(recs), 0)
}

func TestSandboxes_SurviveReopen(t *testing.T) {
	t.Parallel()
	// Arrange
	path := filepath.Join(t.TempDir(), "runner.db")
	store, err := runnerstore.Open(path)
	assert.NilError(t, err)
	rec := runnerstore.SandboxRecord{
		TaskID:  4,
		Version: 3,
		Handle:  backend.Handle{Type: "docker", ID: "c4", Data: json.RawMessage(`{"k":"v"}`)},
	}
	assert.NilError(t, store.WriteSandbox(t.Context(), rec))
	assert.NilError(t, store.Close())

	// Act
	store, err = runnerstore.Open(path)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	recs, err := store.Sandboxes(t.Context())

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, recs, []runnerstore.SandboxRecord{rec})
}

func TestWriteSandbox_ConcurrentDifferentTasks(t *testing.T) {
	t.Parallel()
	store := openStore(t)

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{
				TaskID: int64(i),
				Handle: backend.Handle{Type: "docker", ID: "c"},
			}))
		})
	}
	wg.Wait()

	recs, err := store.Sandboxes(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, len(recs), n)
}
