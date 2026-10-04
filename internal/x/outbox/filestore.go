package outbox

import (
	"cmp"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gritzapp/gritz/internal/x/atomicio"
)

// seqDigits is the zero-padded width of a uint64 Seq in a filename, so lexical
// filename order equals numeric Seq order. math.MaxUint64 is 20 digits.
const seqDigits = 20

// FileStore is a crash-safe, concurrency-safe Store backed by one atomic JSON
// file per record. Live records live at <dir>/<seq>.json and dead-lettered ones
// at <dir>/dead/<seq>.json, where <seq> is a 20-digit zero-padded uint64.
//
// The live records are held in an in-memory FIFO (a container/list, mirroring
// the EventQueue this replaces) loaded once at Open and kept in sync by every
// mutation, so only the head's file is ever touched after startup — disk is
// read only at Open. Writes stay per-record atomic (temp-file + fsync + rename)
// for durability.
type FileStore struct {
	mu      sync.Mutex
	dir     string
	deadDir string
	next    uint64     // next Seq to assign; guarded by mu
	live    *list.List // live Records in FIFO (ascending Seq) order; guarded by mu
}

// Open returns a FileStore backed by dir, creating dir and its dead-letter
// subdirectory if they do not exist. It reads every live record into memory;
// a live record file that cannot be read or decoded fails Open, so corrupt
// durable state surfaces loudly rather than silently dropping a message. The
// Seq counter is seeded from the maximum filename across both the live and dead
// directories, so sequence numbers never repeat even after dead-lettering or a
// restart.
func Open(dir string) (*FileStore, error) {
	deadDir := filepath.Join(dir, "dead")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		return nil, fmt.Errorf("outbox: create dir: %w", err)
	}
	s := &FileStore{dir: dir, deadDir: deadDir, live: list.New()}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("outbox: read dir: %w", err)
	}
	// Load every live record. os.ReadDir returns entries in filename order,
	// which for zero-padded Seqs is ascending Seq order; sort defensively so the
	// FIFO invariant never relies on that.
	var records []Record
	var liveMax uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		seq, ok := parseRecordName(entry.Name())
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("outbox: read record %d: %w", seq, err)
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("outbox: unmarshal record %d: %w", seq, err)
		}
		records = append(records, rec)
		liveMax = max(liveMax, seq)
	}
	slices.SortFunc(records, func(a, b Record) int { return cmp.Compare(a.Seq, b.Seq) })
	for _, rec := range records {
		s.live.PushBack(rec)
	}

	deadMax, err := maxSeq(deadDir)
	if err != nil {
		return nil, err
	}
	s.next = max(liveMax, deadMax) + 1
	return s, nil
}

// livePath returns the on-disk file path for a live record's Seq.
func (s *FileStore) livePath(seq uint64) string {
	return filepath.Join(s.dir, formatSeq(seq)+".json")
}

// deadPath returns the on-disk file path for a dead-lettered record's Seq.
func (s *FileStore) deadPath(seq uint64) string {
	return filepath.Join(s.deadDir, formatSeq(seq)+".json")
}

// Append durably appends payload to the tail under a new, strictly increasing
// Seq. The write is crash-safe: the record is marshalled and written to a temp
// file in the same directory, fsync'd, and renamed over the target, so a crash
// never leaves a half-written record. The record is then appended to the
// in-memory FIFO.
func (s *FileStore) Append(payload json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq := s.next
	rec := Record{Seq: seq, Payload: payload}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("outbox: marshal record: %w", err)
	}
	if err := atomicio.WriteFile(s.livePath(seq), data, 0o600); err != nil {
		return err
	}
	s.live.PushBack(rec)
	s.next = seq + 1
	return nil
}

// Peek returns the head record without removing it. ok is false when the queue
// is empty. It reads only the in-memory FIFO. The returned error is always nil;
// it exists to satisfy the Store interface, whose other implementations may
// fail.
func (s *FileStore) Peek() (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	front := s.live.Front()
	if front == nil {
		return Record{}, false, nil
	}
	return front.Value.(Record), true, nil
}

// Drop durably removes the head record from the live queue. If dead is true the
// record's file is atomically renamed into the dead-letter area; otherwise it is
// deleted (idempotent: a missing file, e.g. from a prior interrupted Drop, is
// not an error). The record is then dropped from the in-memory FIFO. Dropping an
// empty queue is a no-op.
func (s *FileStore) Drop(dead bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	front := s.live.Front()
	if front == nil {
		return nil
	}
	rec := front.Value.(Record)
	if dead {
		if err := os.Rename(s.livePath(rec.Seq), s.deadPath(rec.Seq)); err != nil {
			return fmt.Errorf("outbox: dead-letter record %d: %w", rec.Seq, err)
		}
	} else {
		if err := os.Remove(s.livePath(rec.Seq)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("outbox: remove record %d: %w", rec.Seq, err)
		}
	}
	s.live.Remove(front)
	return nil
}

// Len reports the number of live records.
func (s *FileStore) Len() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live.Len(), nil
}

// maxSeq returns the largest Seq among the <seq>.json record files in dir, or 0
// if there are none. Non-record files are ignored.
func maxSeq(dir string) (uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("outbox: read dir: %w", err)
	}
	var highest uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		seq, ok := parseRecordName(entry.Name())
		if !ok {
			continue
		}
		highest = max(highest, seq)
	}
	return highest, nil
}

// formatSeq renders seq as a 20-digit zero-padded string so lexical order equals
// numeric order.
func formatSeq(seq uint64) string {
	return fmt.Sprintf("%0*d", seqDigits, seq)
}

// parseRecordName reports whether name is a valid <uint64>.json record file and
// returns the Seq if so.
func parseRecordName(name string) (uint64, bool) {
	base, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return 0, false
	}
	seq, err := strconv.ParseUint(base, 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}
