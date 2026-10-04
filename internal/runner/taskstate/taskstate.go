// Package taskstate is the runner's authoritative, runner-local store of the
// task→sandbox-handle mapping.
//
// The backing store is one atomic per-task JSON file (<dir>/<id>.json). The
// store is backend-agnostic: it persists, lists, reads, and removes records
// keyed by task id and never decodes a record's opaque Data.
//
// It is a leaf: apart from the stdlib-only internal/x/atomicio helper it uses
// for its writes, it must not import the backend package or anything
// backend-specific.
package taskstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gritzapp/gritz/internal/x/atomicio"
)

// Record is the persisted task→sandbox-handle mapping. Data is opaque,
// backend-defined, and never decoded by the store.
type Record struct {
	TaskID  int64           `json:"task_id"`
	Version int64           `json:"version,omitempty"` // task.Version at launch; legacy records unmarshal to 0
	Type    string          `json:"type"`              // "docker", "lambda-microvm", ... (informational; never read for logic)
	ID      string          `json:"id"`                // backend-produced handle id
	Data    json.RawMessage `json:"data,omitempty"`    // opaque, backend-defined; the store NEVER decodes it
}

// Store is a runner-local, concurrency-safe store of task records backed by
// atomic per-task JSON files in a single flat directory.
type Store struct {
	mu  sync.Mutex
	dir string
}

// Open returns a Store backed by dir, creating dir if it does not exist.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("taskstate: create dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// path returns the on-disk file path for a task id.
func (s *Store) path(taskID int64) string {
	return filepath.Join(s.dir, strconv.FormatInt(taskID, 10)+".json")
}

// Write atomically persists rec to <dir>/<TaskID>.json. The write is crash-safe:
// the record is marshalled to a temp file in the same directory, fsync'd, and
// renamed over the target, so a concurrent List never observes a half-written
// file.
func (s *Store) Write(rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("taskstate: marshal record: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := atomicio.WriteFile(s.path(rec.TaskID), data, 0o600); err != nil {
		return fmt.Errorf("taskstate: write record %d: %w", rec.TaskID, err)
	}
	return nil
}

// Read returns the record for taskID. ok is false (with a nil error) when no
// record exists.
func (s *Store) Read(taskID int64) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(taskID)
}

// read loads a single record. The caller must hold s.mu.
func (s *Store) read(taskID int64) (Record, bool, error) {
	data, err := os.ReadFile(s.path(taskID))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("taskstate: read record %d: %w", taskID, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false, fmt.Errorf("taskstate: unmarshal record %d: %w", taskID, err)
	}
	return rec, true, nil
}

// Remove deletes the record for taskID. It is idempotent: removing an absent
// task is not an error.
func (s *Store) Remove(taskID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(taskID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("taskstate: remove record %d: %w", taskID, err)
	}
	return nil
}

// List returns all records in the store. Only real <id>.json records are
// considered; leftover temp files and any other files that don't parse as
// <int64>.json are ignored, so an interrupted write can never corrupt a listing.
func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("taskstate: read dir: %w", err)
	}

	var records []Record
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		taskID, ok := parseRecordName(entry.Name())
		if !ok {
			continue
		}
		rec, ok, err := s.read(taskID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// parseRecordName reports whether name is a valid <int64>.json record file and
// returns the task id if so.
func parseRecordName(name string) (int64, bool) {
	base, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return 0, false
	}
	taskID, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return 0, false
	}
	return taskID, true
}
