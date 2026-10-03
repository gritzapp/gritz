// Package redact masks known secret values in a byte stream or a string.
//
// It performs no detection: callers supply the exact values to mask, and every
// occurrence of one is replaced by a marker naming the secret it stood for.
// Masking runs through a [Writer] backed by a byte trie, so one walk covers
// every secret at once and neither the cost nor the buffering grows with the
// number of secrets or the length of their values. Nothing is held back unless
// it is a live prefix of some secret, so the masked stream does not trail the
// raw one between flushes.
//
// Matching is leftmost-longest: at each position the longest value that starts
// there wins, and the scan resumes after it. When the suffix of one value is
// the prefix of another and the input carries both overlapped, the leftmost is
// masked whole and the second value's tail is left beside the marker -- there
// is no order that masks both of an overlapping pair.
package redact

import (
	"io"
	"slices"
	"strings"

	"github.com/icholy/gritz/internal/x/bytetrie"
)

// Marker returns the replacement marker for a named secret:
// "[gritz:masked NAME]".
func Marker(name string) string {
	return "[gritz:masked " + name + "]"
}

// Writer masks secret values in the stream written to it and passes the result
// to an underlying io.Writer. It is not safe for concurrent use.
type Writer struct {
	w io.Writer

	// markers maps each secret value to the marker that replaces it.
	markers bytetrie.Trie[string]

	// held is the tail of the stream that is a live prefix of some secret
	// value: too little to decide on, so it waits for the bytes that follow.
	// It is a proper prefix of a secret, so it never grows past the longest
	// value in the trie however much is written.
	held []byte
}

// NewWriter returns a Writer that replaces every occurrence of each value in
// secrets, a map of secret name to secret value, with Marker(name) before
// writing to w.
//
// Empty values are dropped: an empty value is a prefix of every input and would
// match at every position, shredding the stream instead of redacting it. When
// two names share a value the first name in lexical order wins, so the marker
// does not depend on map iteration order.
//
// The Writer holds a reference to secrets' contents only through its own trie;
// the map may be reused afterwards.
func NewWriter(w io.Writer, secrets map[string]string) *Writer {
	names := make([]string, 0, len(secrets))
	for name, value := range secrets {
		if value != "" {
			names = append(names, name)
		}
	}
	// bytetrie.Put replaces on a duplicate key, so names sharing a value are
	// inserted in reverse order and the smallest name is the one left standing.
	slices.Sort(names)
	rw := &Writer{w: w}
	for _, name := range slices.Backward(names) {
		rw.markers.Put(secrets[name], Marker(name))
	}
	return rw
}

// Write masks p, together with any bytes held back from earlier writes, and
// writes the result to the underlying writer in a single call. A secret value
// split across writes is still masked; bytes that could still turn into one are
// held until enough of the stream has arrived to decide.
//
// It reports len(p) on success. An error from the underlying writer is returned
// with n == 0 -- the bytes that failed to reach it are dropped, but the mask's
// own state survives, so later writes are masked as if the failed one had
// landed.
func (w *Writer) Write(p []byte) (int, error) {
	if err := w.consume(p, false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes every held byte to the underlying writer, masked or not.
//
// A secret value split across a Flush is therefore NOT masked: the part that
// arrived before the Flush goes out raw. Only call it where the stream has
// reached a point the caller knows is not mid-secret -- at EOF, or on a
// boundary the producer controls. A secret that lies whole within the held
// bytes is still masked; only the unfinished tail goes out as it came in.
//
// Flush does not close or flush the underlying writer.
func (w *Writer) Flush() error {
	return w.consume(nil, true)
}

// consume scans the held bytes followed by p and writes out everything it can
// decide on. When final is set it decides on all of them, taking the longest
// match that fits in what it has rather than waiting for more input.
func (w *Writer) consume(p []byte, final bool) error {
	buf := p
	if len(w.held) > 0 {
		w.held = append(w.held, p...)
		buf = w.held
	}

	// out stays nil while nothing has matched, so the common case of a stream
	// carrying no secret copies no bytes: the tail below writes buf directly.
	var out []byte
	// lit is the start of the run of literal bytes not yet copied into out.
	var lit, i int
	for i < len(buf) {
		n, marker, partial := w.markers.Lookup(buf[i:])
		if partial && !final {
			// Some secret starts with buf[i:] but runs past the end of what we
			// have. Even a match in hand may be the short half of a longer one,
			// so hold the rest and settle it when the next bytes arrive.
			break
		}
		if n == 0 {
			// No secret starts here. Emit this byte and rescan from the next
			// one rather than from the end of the failed walk: with the secret
			// "aab", the input "aaab" only matches from its second byte.
			i++
			continue
		}
		out = append(append(out, buf[lit:i]...), marker...)
		i += n
		lit = i
	}

	emit := buf[:i]
	if out != nil {
		emit = append(out, buf[lit:i]...)
	}
	var err error
	if len(emit) > 0 {
		// The write comes first: when nothing matched, emit is a window onto
		// buf, which compacting below would overwrite.
		_, err = w.w.Write(emit)
	}
	// Compacted even on a failed write, so the mask stays aligned with the
	// stream. copy handles buf and w.held sharing an array.
	w.held = append(w.held[:0], buf[i:]...)
	return err
}

// String returns s with every occurrence of each secret value replaced by
// Marker(name). It is the entry point for callers holding a whole value rather
// than a stream, and runs the same Writer, so the two cannot drift.
func String(s string, secrets map[string]string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	w := NewWriter(&sb, secrets)
	// strings.Builder never fails, so neither call below can.
	_, _ = w.Write([]byte(s))
	_ = w.Flush()
	return sb.String()
}
