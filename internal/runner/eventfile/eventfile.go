// Package eventfile is the driver's append-only copy of its task's events, in
// the order the runner forwarded them over the Run stream, which is the order
// of the runner's replica. See proposals/draft/runner-event-replica.md.
//
// The file holds one protojson-encoded gritz.v1.Event per line. Append fsyncs
// before it returns, so a stored event survives a driver restart and a
// suspended sandbox being booted again. A line torn by a crash mid-append is
// the last one, and Open drops it.
package eventfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Path is where the driver stores its task's events inside the sandbox.
const Path = "/gritz/events.jsonl"

// File is the driver's append-only copy of its task's events. It is safe for
// concurrent use.
type File struct {
	path string

	mu sync.Mutex
	// ids is the id of every stored event, so Append skips events already
	// stored. Guarded by mu.
	ids map[int64]struct{}
	// last is the id of the last stored event, or 0. Guarded by mu.
	last int64
}

// Open opens the file at path and reads the last stored id. An absent file
// holds no events; the first Append creates it, in a directory that must
// exist. A torn last line, left by a crash mid-append, is truncated away.
func Open(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("eventfile: read: %w", err)
	}
	events, size, err := decode(data)
	if err != nil {
		return nil, err
	}
	if size < int64(len(data)) {
		if err := os.Truncate(path, size); err != nil {
			return nil, fmt.Errorf("eventfile: truncate torn line: %w", err)
		}
	}
	f := &File{path: path, ids: make(map[int64]struct{}, len(events))}
	for _, ev := range events {
		f.ids[ev.GetId()] = struct{}{}
		f.last = ev.GetId()
	}
	return f, nil
}

// decode parses the complete lines of data, and returns the length of the
// prefix they make up: a last line without its newline is torn, and not
// included.
func decode(data []byte) ([]*gritzv1.Event, int64, error) {
	var events []*gritzv1.Event
	var size int64
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		ev := &gritzv1.Event{}
		if err := protojson.Unmarshal(data[:i], ev); err != nil {
			return nil, 0, fmt.Errorf("eventfile: decode line at offset %d: %w", size, err)
		}
		events = append(events, ev)
		size += int64(i) + 1
		data = data[i+1:]
	}
	return events, size, nil
}

// Append stores events, after those already stored and in the given order,
// skipping any already stored, and fsyncs before returning. A failed write is
// truncated away, so the file never keeps part of a batch.
func (f *File) Append(events []*gritzv1.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var buf bytes.Buffer
	added := map[int64]struct{}{}
	last := f.last
	for _, ev := range events {
		id := ev.GetId()
		if _, ok := f.ids[id]; ok {
			continue
		}
		if _, ok := added[id]; ok {
			continue
		}
		line, err := protojson.Marshal(ev)
		if err != nil {
			return fmt.Errorf("eventfile: encode event %d: %w", id, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		added[id] = struct{}{}
		last = id
	}
	if buf.Len() == 0 {
		return nil
	}
	if err := f.write(buf.Bytes()); err != nil {
		return err
	}
	for id := range added {
		f.ids[id] = struct{}{}
	}
	f.last = last
	return nil
}

// write appends data to the file and fsyncs it. On failure it truncates the
// file back to its size before the write. The caller holds f.mu.
func (f *File) write(data []byte) (err error) {
	file, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("eventfile: open: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("eventfile: stat: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(fmt.Errorf("eventfile: write: %w", err), file.Truncate(info.Size()))
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("eventfile: sync: %w", err), file.Truncate(info.Size()))
	}
	return nil
}

// LastID returns the id of the last stored event, or 0.
func (f *File) LastID() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// After returns the stored events after the event with id, in stored order;
// id 0 returns all of them. An id that isn't stored returns none.
func (f *File) After(id int64) ([]*gritzv1.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("eventfile: read: %w", err)
	}
	events, _, err := decode(data)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return events, nil
	}
	for i, ev := range events {
		if ev.GetId() == id {
			return events[i+1:], nil
		}
	}
	return nil, nil
}
