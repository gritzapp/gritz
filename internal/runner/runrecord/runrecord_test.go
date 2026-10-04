package runrecord

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
)

func TestWriteRead(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "run.json")
	rec := Record{
		Version:    7,
		State:      Finished,
		FinishedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Reported:   true,
	}

	// Act
	assert.NilError(t, Write(path, rec))
	got, ok, err := Read(path)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.DeepEqual(t, got, rec)
}

func TestWrite_Running(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "run.json")

	// Act
	assert.NilError(t, Write(path, Record{Version: 3, State: Running}))

	// Assert: a running record carries only its version and state.
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(data), `{"version":3,"state":"running"}`)
}

func TestWrite_Overwrite(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")
	assert.NilError(t, Write(path, Record{Version: 1, State: Running}))

	// Act
	assert.NilError(t, Write(path, Record{Version: 2, State: Running}))

	// Assert: the record is replaced and no temp file is left behind.
	got, ok, err := Read(path)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, got.Version, int64(2))
	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
}

func TestRead_Absent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")

	_, ok, err := Read(path)

	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestRead_Invalid(t *testing.T) {
	tests := []struct {
		name string
		data string
		err  string
	}{
		{"malformed", `{"version":`, "runrecord: decode"},
		{"no state", `{"version":1}`, `invalid state ""`},
		{"unknown state", `{"version":1,"state":"paused"}`, `invalid state "paused"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.json")
			assert.NilError(t, os.WriteFile(path, []byte(tt.data), 0o600))

			_, _, err := Read(path)

			assert.ErrorContains(t, err, tt.err)
		})
	}
}

func TestReconcile_Running(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "run.json")
	assert.NilError(t, Write(path, Record{Version: 5, State: Running}))
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	// Act
	rec, ok, crashed, err := Reconcile(path, now)

	// Assert: the record is finished, unreported, and persisted.
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, crashed, true)
	want := Record{Version: 5, State: Finished, FinishedAt: now, Reported: false}
	assert.DeepEqual(t, rec, want)
	got, _, err := Read(path)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)
}

func TestReconcile_Finished(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "run.json")
	want := Record{
		Version:    5,
		State:      Finished,
		FinishedAt: time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC),
		Reported:   true,
	}
	assert.NilError(t, Write(path, want))

	// Act
	rec, ok, crashed, err := Reconcile(path, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))

	// Assert: a finished record is left alone.
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, crashed, false)
	assert.DeepEqual(t, rec, want)
	got, _, err := Read(path)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)
}

func TestReconcile_Absent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")

	_, ok, crashed, err := Reconcile(path, time.Now())

	assert.NilError(t, err)
	assert.Equal(t, ok, false)
	assert.Equal(t, crashed, false)
	_, err = os.Stat(path)
	assert.Assert(t, os.IsNotExist(err))
}

func TestProto(t *testing.T) {
	finishedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rec  Record
		want *driverv1.RunRecord
	}{
		{
			"running",
			Record{Version: 3, State: Running},
			&driverv1.RunRecord{Version: 3, State: driverv1.RunRecord_STATE_RUNNING},
		},
		{
			"finished",
			Record{Version: 3, State: Finished, FinishedAt: finishedAt, Reported: true},
			&driverv1.RunRecord{
				Version:    3,
				State:      driverv1.RunRecord_STATE_FINISHED,
				FinishedAt: timestamppb.New(finishedAt),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, tt.rec.Proto(), tt.want, protocmp.Transform())
		})
	}
}
