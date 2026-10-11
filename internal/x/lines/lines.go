// Package lines iterates over the lines of a byte stream.
package lines

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"iter"
)

// Read returns an iterator over the lines read from r, without their trailing
// "\n" or "\r\n". Unlike a default bufio.Scanner, it has no maximum line
// length. A final line with no newline is still yielded. Each yielded slice is
// newly allocated and remains valid after the iteration advances.
//
// A read error other than io.EOF is yielded as (nil, err) and ends the
// iteration; the line it cut short is dropped. Read leaves r where the error
// left it, so a caller that must consume r to the end (the read side of a
// subprocess's pipe, for example) drains it itself.
func Read(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadBytes('\n')
			switch {
			case err == nil:
				if !yield(bytes.TrimRight(line, "\r\n"), nil) {
					return
				}
			case errors.Is(err, io.EOF):
				if len(line) > 0 {
					yield(bytes.TrimRight(line, "\r"), nil)
				}
				return
			default:
				yield(nil, err)
				return
			}
		}
	}
}
