package model

import (
	"fmt"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/proto"
)

// EventCursor is a position in the (xid, id) order ListRunnerEvents reads
// in. The zero value is the start of the log.
type EventCursor struct {
	XID uint64
	ID  int64
}

// Encode returns the cursor as the opaque bytes ListRunnerEvents hands to
// clients: a protobuf-encoded gritz.v1.EventCursor.
func (c EventCursor) Encode() ([]byte, error) {
	return proto.Marshal(&gritzv1.EventCursor{Xid: c.XID, Id: c.ID})
}

// DecodeEventCursor decodes a cursor produced by Encode; empty is the start
// of the log.
func DecodeEventCursor(b []byte) (EventCursor, error) {
	var pb gritzv1.EventCursor
	if err := proto.Unmarshal(b, &pb); err != nil {
		return EventCursor{}, fmt.Errorf("invalid cursor: %w", err)
	}
	return EventCursor{XID: pb.Xid, ID: pb.Id}, nil
}
