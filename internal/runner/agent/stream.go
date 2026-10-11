package agent

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// readLines calls fn with each line read from r, without its trailing newline,
// until EOF. Lines may be any length: an agent CLI's stream puts a whole tool
// result on one line, which easily exceeds bufio.Scanner's 64 KiB default.
//
// A read error is logged and the rest of r is discarded rather than left
// unread, so the CLI writing to the other end of the pipe can never block on a
// full pipe.
func readLines(r io.Reader, log *DriverLog, fn func(line []byte)) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		switch {
		case err == nil:
			fn(bytes.TrimRight(line, "\r\n"))
		case errors.Is(err, io.EOF):
			// The last line may be unterminated.
			if len(line) > 0 {
				fn(bytes.TrimRight(line, "\r"))
			}
			return
		default:
			// The partial line is dropped: it is cut short at an arbitrary byte.
			log.Error("failed to read agent output", "err", err)
			_, _ = io.Copy(io.Discard, br)
			return
		}
	}
}
