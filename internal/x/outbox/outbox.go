// Package outbox is a durable, at-least-once outbox generic over a
// JSON-serializable payload type and a pluggable backing store. It persists
// each message before attempting delivery, survives restarts, and retries
// transient failures with backoff.
//
// The Store interface and Record type define the durable, crash-safe FIFO queue
// the outbox drives; FileStore is a stdlib-only filesystem implementation. The
// Outbox engine (see engine.go) delivers persisted messages in order.
//
// The filesystem implementation ports the proven pattern from the runner's
// taskstate package: one atomic per-record JSON file, written via temp-file +
// fsync + rename so a crash never leaves a half-written record behind.
//
// It is a dependency-free leaf: stdlib only.
package outbox

import "encoding/json/jsontext"

// Record is one persisted, undelivered message. Seq is a per-outbox monotonic
// sequence number that defines delivery (FIFO) order; it is an implementation
// detail of the store's on-disk ordering and logging — callers never address a
// record by it. Payload is the opaque, JSON-encoded T; the store never decodes
// it.
type Record struct {
	Seq     uint64         `json:"seq"`
	Payload jsontext.Value `json:"payload"`
}

// Store is the durable, crash-safe backing store for an outbox: a strict FIFO
// queue. It is single-consumer — the head is stable between a Peek and the Drop
// that follows it — but Append may run concurrently with the consumer.
// Implementations must be safe for concurrent use.
//
//go:generate go tool moq -out store_moq.go . Store
type Store interface {
	// Append durably appends payload to the tail. It must not return until the
	// record is durable.
	Append(payload jsontext.Value) error
	// Peek returns the head record without removing it. ok is false when the
	// queue is empty.
	Peek() (rec Record, ok bool, err error)
	// Drop durably removes the head record. If dead is true the record is moved
	// to the dead-letter area; otherwise it is discarded. Call it after the head
	// has been delivered (dead=false) or judged permanently undeliverable
	// (dead=true). No-op on an empty queue.
	Drop(dead bool) error
	// Len reports the number of live records.
	Len() (int, error)
}
