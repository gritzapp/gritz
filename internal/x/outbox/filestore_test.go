package outbox

import (
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

func TestAppendPeekDrop(t *testing.T) {
	// Arrange
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	// Act
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":3}`)))

	// Assert: records come off the head in FIFO (append) order. Peek is
	// non-destructive; Drop advances the head.
	for _, want := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		rec, ok, err := s.Peek()
		assert.NilError(t, err)
		assert.Assert(t, ok)
		assert.Equal(t, string(rec.Payload), want)

		// Peeking again returns the same head.
		rec2, ok2, err := s.Peek()
		assert.NilError(t, err)
		assert.Assert(t, ok2)
		assert.Equal(t, rec2.Seq, rec.Seq)

		assert.NilError(t, s.Drop(false))
	}

	// The queue is drained.
	n, err := s.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestPeek_Empty(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	_, ok, err := s.Peek()
	assert.NilError(t, err)
	assert.Assert(t, !ok)
}

func TestLen(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	n, err := s.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)

	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))
	n, err = s.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 2)

	assert.NilError(t, s.Drop(false))
	n, err = s.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
}

func TestDrop_Durable(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))

	// Act: drop the head, then restart.
	assert.NilError(t, s.Drop(false))
	s2, err := Open(dir)
	assert.NilError(t, err)

	// Assert: the dropped record does not come back; the second one does.
	n, err := s2.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	rec, ok, err := s2.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, string(rec.Payload), `{"n":2}`)
}

func TestDrop_Empty(t *testing.T) {
	// Drop(false) on an empty queue is a no-op, not an error.
	s, err := Open(t.TempDir())
	assert.NilError(t, err)
	assert.NilError(t, s.Drop(false))
}

func TestDrop_DeadLetter(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))

	// Peek the head so we know its Seq for the on-disk assertion.
	head, ok, err := s.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)

	// Act
	assert.NilError(t, s.Drop(true))

	// Assert: the head advanced to the second record...
	rec, ok, err := s.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, string(rec.Payload), `{"n":2}`)
	n, err := s.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	// ...and the dead-lettered record lands under <dir>/dead/<seq>.json.
	_, err = os.Stat(filepath.Join(dir, "dead", formatSeq(head.Seq)+".json"))
	assert.NilError(t, err)
	// ...and no longer exists in the live dir.
	_, err = os.Stat(s.livePath(head.Seq))
	assert.Assert(t, os.IsNotExist(err))
}

func TestDropDead_Empty(t *testing.T) {
	// Drop(true) on an empty queue is a no-op, not an error.
	s, err := Open(t.TempDir())
	assert.NilError(t, err)
	assert.NilError(t, s.Drop(true))
}

func TestSeqMonotonicAcrossRestart(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))

	// Act: re-Open the same directory (simulating a restart) and append again.
	s2, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s2.Append(jsontext.Value(`{"n":3}`)))

	// Assert: the new record's Seq continues past the highest existing one, so a
	// restart never reuses a Seq. The head is still the oldest record.
	assert.Equal(t, s2.next, uint64(4))
	rec, ok, err := s2.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, rec.Seq, uint64(1))
}

func TestSeqMonotonicAcrossDeadLetterAndRestart(t *testing.T) {
	// Arrange: append two records, drop the head, then dead-letter the next so the
	// live directory is empty but the dead directory holds the highest Seq (2).
	// Without scanning the dead dir at Open, the seq counter would reset and
	// reuse Seq 2.
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))
	assert.NilError(t, s.Drop(false)) // discards Seq 1
	assert.NilError(t, s.Drop(true))  // dead-letters Seq 2

	// Act: re-Open with no live records remaining.
	s2, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s2.Append(jsontext.Value(`{"n":3}`)))

	// Assert: seeding scans the dead dir too, so Seq never repeats.
	assert.Equal(t, s2.next, uint64(4))
	rec, ok, err := s2.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, rec.Seq, uint64(3))
}

func TestOpen_LoadsRecords(t *testing.T) {
	// Arrange: persist a few records, then discard the in-memory store.
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))

	// Act: re-Open the same directory (simulating a restart).
	s2, err := Open(dir)
	assert.NilError(t, err)

	// Assert: the records come back from disk in FIFO order with their payloads.
	first, ok, err := s2.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, first.Seq, uint64(1))
	assert.Equal(t, string(first.Payload), `{"n":1}`)
	assert.NilError(t, s2.Drop(false))

	second, ok, err := s2.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, second.Seq, uint64(2))
	assert.Equal(t, string(second.Payload), `{"n":2}`)
}

func TestOpen_CorruptRecordFails(t *testing.T) {
	// Arrange: a live record file holding undecodable JSON.
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	head, ok, err := s.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.NilError(t, os.WriteFile(s.livePath(head.Seq), []byte("{not json"), 0o644))

	// Act: re-Open must surface the corrupt durable state loudly.
	_, err = Open(dir)

	// Assert
	assert.ErrorContains(t, err, "unmarshal record")
}

func TestOpen_IgnoresGarbage(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	s, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, s.Append(jsontext.Value(`{"n":1}`)))
	assert.NilError(t, s.Append(jsontext.Value(`{"n":2}`)))

	// Drop stray files that must be ignored on load.
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".tmp-leftover"), []byte("garbage"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "not-a-number.json"), []byte("{}"), 0o644))

	// Act: re-Open so the garbage files go through the load path.
	s2, err := Open(dir)
	assert.NilError(t, err)

	// Assert: only the two real records are loaded, in order.
	n, err := s2.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 2)
	rec, ok, err := s2.Peek()
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, rec.Seq, uint64(1))
}

func TestAppend_Concurrent(t *testing.T) {
	s, err := Open(t.TempDir())
	assert.NilError(t, err)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NilError(t, s.Append(jsontext.Value(`{}`)))
		}()
	}
	wg.Wait()

	// Every Append persisted a record under a distinct Seq: draining yields n
	// records with strictly increasing Seqs and no duplicates.
	count, err := s.Len()
	assert.NilError(t, err)
	assert.Equal(t, count, n)

	seen := make(map[uint64]bool, n)
	var prev uint64
	for {
		rec, ok, err := s.Peek()
		assert.NilError(t, err)
		if !ok {
			break
		}
		assert.Assert(t, !seen[rec.Seq], "duplicate Seq %d", rec.Seq)
		assert.Assert(t, rec.Seq > prev, "Seq %d not increasing after %d", rec.Seq, prev)
		seen[rec.Seq] = true
		prev = rec.Seq
		assert.NilError(t, s.Drop(false))
	}
	assert.Equal(t, len(seen), n)
}
