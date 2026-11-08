// Package shellwire defines the end-to-end framing for the driver reverse shell
// (step 3 of the design in
// docs/src/content/docs/proposals/implemented/driver-reverse-shell.md).
//
// Frames travel as binary WebSocket messages between the driver (which owns the
// PTY) and the operator's client. The server-side relay
// (internal/shell/shellrelay) passes them through opaquely and never parses
// them — the framing is a contract between the two endpoints only. Both the
// driver leg (shell.Serve) and the operator leg (shell.Attach) import this codec
// so the wire format has a single definition.
//
// A frame is [1-byte type][payload]:
//
//	0x00 data   — raw PTY bytes (both directions; a PTY master is one stream)
//	0x01 resize — terminal size, two big-endian uint16s: rows then cols
//	0x02 exit   — shell exit code, one big-endian int32
//	0x03 ping   — keepalive, no payload
package shellwire

import (
	"encoding/binary"
	"fmt"
)

// Subprotocol is the WebSocket subprotocol version token negotiated on both
// legs (Sec-WebSocket-Protocol: gritz-shell.v1). It carries no credential —
// the attach leg authenticates with a Bearer token on the request. It lives
// here so the relay, the driver, and the CLI client all reference the wire
// contract from a single package.
const Subprotocol = "gritz-shell.v1"

// ReadLimit is the per-message read limit applied to every *websocket.Conn on
// every leg of a shell session (both dial legs and both relay legs). It lives
// here — the common leaf both the client legs and the server-side relay import —
// so the value has a single definition and neither side has to reach across to
// the other's package for it.
//
// coder/websocket defaults to 32768 bytes, but the driver frames PTY output with
// a 32*1024 buffer, so a full read yields a 1 + 32768 = 32769-byte data frame
// that trips the default limit and tears the session down on the first large
// output burst. 1 MiB comfortably covers a full output chunk (or a large
// operator paste) with room to spare.
const ReadLimit = 1 << 20

// Type is the one-byte frame discriminator.
type Type byte

const (
	// TypeData carries raw PTY bytes in either direction.
	TypeData Type = 0x00
	// TypeResize carries a new terminal size (rows, cols).
	TypeResize Type = 0x01
	// TypeExit carries the shell's exit code.
	TypeExit Type = 0x02
	// TypePing is a keepalive with no payload.
	TypePing Type = 0x03
)

// Frame is a decoded wire frame: a type and its raw payload.
type Frame struct {
	Type    Type
	Payload []byte
}

// Data encodes a data frame carrying raw PTY bytes.
func Data(b []byte) []byte {
	msg := make([]byte, 1+len(b))
	msg[0] = byte(TypeData)
	copy(msg[1:], b)
	return msg
}

// Resize encodes a resize frame from rows and cols.
func Resize(rows, cols uint16) []byte {
	msg := make([]byte, 5)
	msg[0] = byte(TypeResize)
	binary.BigEndian.PutUint16(msg[1:3], rows)
	binary.BigEndian.PutUint16(msg[3:5], cols)
	return msg
}

// Exit encodes an exit frame carrying the shell's exit code.
func Exit(code int) []byte {
	msg := make([]byte, 5)
	msg[0] = byte(TypeExit)
	binary.BigEndian.PutUint32(msg[1:5], uint32(int32(code)))
	return msg
}

// Ping encodes a keepalive frame.
func Ping() []byte {
	return []byte{byte(TypePing)}
}

// Parse decodes a wire message into a Frame. It errors on an empty message.
func Parse(msg []byte) (Frame, error) {
	if len(msg) == 0 {
		return Frame{}, fmt.Errorf("shellwire: empty frame")
	}
	return Frame{Type: Type(msg[0]), Payload: msg[1:]}, nil
}

// ResizeDims decodes the rows and cols from a resize frame's payload.
func (f Frame) ResizeDims() (rows, cols uint16, err error) {
	if f.Type != TypeResize {
		return 0, 0, fmt.Errorf("shellwire: not a resize frame: type %#x", byte(f.Type))
	}
	if len(f.Payload) != 4 {
		return 0, 0, fmt.Errorf("shellwire: resize payload must be 4 bytes, got %d", len(f.Payload))
	}
	rows = binary.BigEndian.Uint16(f.Payload[0:2])
	cols = binary.BigEndian.Uint16(f.Payload[2:4])
	return rows, cols, nil
}

// ExitCode decodes the exit code from an exit frame's payload.
func (f Frame) ExitCode() (int, error) {
	if f.Type != TypeExit {
		return 0, fmt.Errorf("shellwire: not an exit frame: type %#x", byte(f.Type))
	}
	if len(f.Payload) != 4 {
		return 0, fmt.Errorf("shellwire: exit payload must be 4 bytes, got %d", len(f.Payload))
	}
	return int(int32(binary.BigEndian.Uint32(f.Payload))), nil
}
