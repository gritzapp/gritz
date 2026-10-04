package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LogChunk is an opaque run of driver log bytes shipped to the server. Chunks
// are not parsed into lines: a task's transcript is the concatenation of its
// chunks' Data in ID order, which — because the driver ships with a single
// in-flight sender — is the order the bytes were written.
//
// See proposals/implemented/ship-driver-logs-to-server.md.
type LogChunk struct {
	ID     int64 `json:"id"`
	OrgID  int64 `json:"org_id"`
	TaskID int64 `json:"task_id"`
	// Version is the run (Task.Version) the bytes belong to; 0 is the pre-run
	// preamble the driver buffers before it fetches its task.
	Version   int64     `json:"version"`
	Data      []byte    `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}

// Proto converts a LogChunk to its protobuf representation. OrgID is not on the
// wire: it is the caller's tenancy, enforced by the handler, not data a reader
// needs.
func (c *LogChunk) Proto() *gritzv1.LogChunk {
	return &gritzv1.LogChunk{
		Id:        c.ID,
		TaskId:    c.TaskID,
		Version:   c.Version,
		Data:      c.Data,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
}
