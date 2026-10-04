// Package runrecord reads and writes the driver's run record, the file on the
// sandbox's disk (/gritz/run.json) that lets a restarted driver answer for a
// run it can no longer see.
//
// The record is written atomically with internal/x/atomicio: {version,
// running} before a run starts, and {version, finished, finished_at, reported}
// once the run has finished. On boot, Reconcile turns a record still running,
// which means the process died mid-run, into finished with reported false.
package runrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/x/atomicio"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Path is where the driver keeps its run record inside the sandbox.
const Path = "/gritz/run.json"

// State is the state of a run.
type State string

const (
	Running  State = "running"
	Finished State = "finished"
)

// Record is the persisted run record. It mirrors driver.v1.RunRecord, plus
// Reported, which mirrors driver.v1.Finished.reported.
type Record struct {
	Version int64 `json:"version"`
	State   State `json:"state"`
	// When the run finished. Zero while running.
	FinishedAt time.Time `json:"finished_at,omitzero"`
	// True if the server acknowledged the run's terminal runner event.
	Reported bool `json:"reported,omitempty"`
}

// Proto returns the record as a driver.v1.RunRecord.
func (r Record) Proto() *driverv1.RunRecord {
	pb := &driverv1.RunRecord{Version: r.Version}
	switch r.State {
	case Running:
		pb.State = driverv1.RunRecord_STATE_RUNNING
	case Finished:
		pb.State = driverv1.RunRecord_STATE_FINISHED
	}
	if !r.FinishedAt.IsZero() {
		pb.FinishedAt = timestamppb.New(r.FinishedAt)
	}
	return pb
}

// Read returns the record at path. ok is false (with a nil error) when no
// record exists.
func Read(path string) (rec Record, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("runrecord: read: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false, fmt.Errorf("runrecord: decode: %w", err)
	}
	switch rec.State {
	case Running, Finished:
	default:
		return Record{}, false, fmt.Errorf("runrecord: invalid state %q", rec.State)
	}
	return rec, true, nil
}

// Write atomically replaces the record at path with rec.
func Write(path string, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("runrecord: encode: %w", err)
	}
	if err := atomicio.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("runrecord: write: %w", err)
	}
	return nil
}

// Reconcile is run on boot, before serving. A record still running means the
// process died mid-run, so it is rewritten as finished at now, with reported
// false. Reconcile returns the record as it stands afterwards; ok is false when
// there is no record, and crashed is true when a running record was rewritten.
func Reconcile(path string, now time.Time) (rec Record, ok, crashed bool, err error) {
	rec, ok, err = Read(path)
	if err != nil || !ok || rec.State != Running {
		return rec, ok, false, err
	}
	rec = Record{
		Version:    rec.Version,
		State:      Finished,
		FinishedAt: now,
		Reported:   false,
	}
	if err := Write(path, rec); err != nil {
		return Record{}, false, false, err
	}
	return rec, true, true, nil
}
