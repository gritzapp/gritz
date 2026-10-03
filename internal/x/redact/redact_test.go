package redact

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestMarker(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Marker("GH_TOKEN"), "[gritz:masked GH_TOKEN]")
}

func TestWriter(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act
	n, err := w.Write([]byte("cloning with ghp_abc123 now\n"))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, len("cloning with ghp_abc123 now\n"))
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(), "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestWriter_NoHoldbackOnPlainText pins finding 1 of #1578 at the writer level:
// text carrying no secret is out before the Flush, however long the secrets
// are, so the masked stream does not trail the raw one.
func TestWriter_NoHoldbackOnPlainText(t *testing.T) {
	t.Parallel()
	// Arrange -- 900 bytes is the size that held 899 on every write in the
	// probe against the transformer chain.
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"BIG": strings.Repeat("x", 900)})

	// Act -- "example" starts a live prefix that dies on its second byte.
	_, err := w.Write([]byte("a plain example line\n"))

	// Assert -- every byte shipped, with no Flush.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "a plain example line\n")
}

// TestWriter_LargeSecrets pins finding 2 of #1578: the sizes that wedged
// transform.Chain -- a second secret past its 4096-byte internal buffer --
// neither stall the stream nor fail.
func TestWriter_LargeSecrets(t *testing.T) {
	t.Parallel()
	// Arrange
	big, bigger := strings.Repeat("a", 6000), strings.Repeat("b", 4097)
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"BIG": big, "BIGGER": bigger})

	// Act
	_, err := w.Write([]byte("log line one\n"))
	assert.NilError(t, err)
	_, err = w.Write([]byte("using " + bigger + " here\n"))

	// Assert -- the plain line shipped immediately, and the non-longest secret
	// masked whole rather than jamming the stream.
	assert.NilError(t, err)
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(), "log line one\nusing [gritz:masked BIGGER] here\n")
}

// TestWriter_SecretStraddlesWrites asserts a secret split across two writes is
// still masked: the first write holds the live prefix instead of shipping it.
func TestWriter_SecretStraddlesWrites(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act
	_, err := w.Write([]byte("cloning with ghp_a"))
	assert.NilError(t, err)

	// Assert -- the half token is held, not shipped.
	assert.Equal(t, buf.String(), "cloning with ")

	// Act
	_, err = w.Write([]byte("bc123 now\n"))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestWriter_OneByteWrites asserts the mask does not depend on how the stream
// is chopped up: a byte at a time is the worst case of straddling.
func TestWriter_OneByteWrites(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act
	for _, b := range []byte("run ghp_abc123 and ghp_abc123\n") {
		n, err := w.Write([]byte{b})
		assert.NilError(t, err)
		assert.Equal(t, n, 1)
	}

	// Assert
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(),
		"run [gritz:masked GH_TOKEN] and [gritz:masked GH_TOKEN]\n")
}

// TestWriter_PrefixSecrets asserts that when one value is a prefix of another
// the longer one still masks whole: matching is longest, so the shorter secret
// cannot fire first and leave the longer value's tail beside a marker.
func TestWriter_PrefixSecrets(t *testing.T) {
	t.Parallel()
	// Arrange -- A sorts first by name but must not win the overlap.
	short := "gho_abcdefghijklmnop"
	long := short + "QRSTUVWXYZ99"
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"A_TOKEN": short, "B_TOKEN": long})

	// Act
	_, err := w.Write([]byte("using " + long + " and " + short + "\n"))

	// Assert
	assert.NilError(t, err)
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(),
		"using [gritz:masked B_TOKEN] and [gritz:masked A_TOKEN]\n")
}

// TestWriter_RescansFromTheNextByte asserts a failed walk resumes one byte
// past where it started, not at the byte that broke it: the secret "aab" lies
// inside "aaab" only from its second byte.
func TestWriter_RescansFromTheNextByte(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"S": "aab"})

	// Act
	_, err := w.Write([]byte("aaab"))

	// Assert
	assert.NilError(t, err)
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(), "a[gritz:masked S]")
}

// TestWriter_RescansFromTheNextByte_OneByteWrites is the same overlap arriving
// a byte at a time, where the rescan runs over held bytes rather than over one
// write's buffer.
func TestWriter_RescansFromTheNextByte_OneByteWrites(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"S": "aab"})

	// Act
	for _, b := range []byte("aaab") {
		_, err := w.Write([]byte{b})
		assert.NilError(t, err)
	}

	// Assert
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(), "a[gritz:masked S]")
}

// TestWriter_FlushDrainsHeldBytes asserts Flush ships a half secret raw rather
// than holding it forever, which is what its doc comment promises.
func TestWriter_FlushDrainsHeldBytes(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})
	_, err := w.Write([]byte("cloning with ghp_a"))
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "cloning with ")

	// Act
	assert.NilError(t, w.Flush())

	// Assert -- the half token goes out as it came in.
	assert.Equal(t, buf.String(), "cloning with ghp_a")
}

// TestWriter_FlushMasksACompleteMatch asserts Flush only gives up on the
// unfinished tail: a whole secret sitting in the held bytes because a longer
// one might have followed is still masked.
func TestWriter_FlushMasksACompleteMatch(t *testing.T) {
	t.Parallel()
	// Arrange -- "ab" is held rather than emitted, since "abcd" may follow.
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"SHORT": "ab", "LONG": "abcd"})
	_, err := w.Write([]byte("x ab"))
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "x ")

	// Act
	assert.NilError(t, w.Flush())

	// Assert
	assert.Equal(t, buf.String(), "x [gritz:masked SHORT]")
}

// TestWriter_FlushSplitSecret asserts the documented limit of Flush: a secret
// split across one is not masked, because the held half is already out.
func TestWriter_FlushSplitSecret(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})
	_, err := w.Write([]byte("ghp_abc"))
	assert.NilError(t, err)
	assert.NilError(t, w.Flush())

	// Act
	_, err = w.Write([]byte("123\n"))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "ghp_abc123\n")
}

// TestWriter_OverlappingTail pins the documented limit of leftmost matching:
// when one value's suffix is the next value's prefix, the first is masked whole
// and the second one's tail is left in the stream.
func TestWriter_OverlappingTail(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"ONE": "abcXYZ", "TWO": "XYZdef"})

	// Act
	_, err := w.Write([]byte("abcXYZdef"))

	// Assert
	assert.NilError(t, err)
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.String(), "[gritz:masked ONE]def")
}

func TestWriter_Empty(t *testing.T) {
	t.Parallel()
	// Arrange
	var buf bytes.Buffer
	w := NewWriter(&buf, map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act
	n, err := w.Write(nil)

	// Assert -- an empty write reaches neither the underlying writer nor an
	// error, and the writer stays usable.
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.NilError(t, w.Flush())
	assert.Equal(t, buf.Len(), 0)
}

// TestWriter_WriteError asserts an error from the underlying writer surfaces
// and leaves the mask aligned with the stream: the held bytes survive it, so
// the secret straddling the failed write still masks on the next one.
func TestWriter_WriteError(t *testing.T) {
	t.Parallel()
	// Arrange
	wantErr := errors.New("pipe closed")
	var buf bytes.Buffer
	fail := true
	w := NewWriter(writerFunc(func(p []byte) (int, error) {
		if fail {
			return 0, wantErr
		}
		return buf.Write(p)
	}), map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Act
	n, err := w.Write([]byte("cloning with ghp_a"))

	// Assert
	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, n, 0)

	// Act -- the rest of the token arrives once the writer recovers.
	fail = false
	_, err = w.Write([]byte("bc123 now\n"))

	// Assert -- only the failed bytes were lost.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "[gritz:masked GH_TOKEN] now\n")
}

// writerFunc adapts a function to io.Writer. moq is the rule for interface
// doubles, but io.Writer is a standard-library interface with no local
// declaration to hang a //go:generate directive on.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestString(t *testing.T) {
	t.Parallel()
	// Act
	got := String("cloning with ghp_abc123 now\n", map[string]string{"GH_TOKEN": "ghp_abc123"})

	// Assert
	assert.Equal(t, got, "cloning with [gritz:masked GH_TOKEN] now\n")
}

// TestString_EmptyValue asserts an empty secret is skipped rather than matching
// at every position: the workspace config does no validation, so an empty value
// can reach the mask.
func TestString_EmptyValue(t *testing.T) {
	t.Parallel()
	// Act
	got := String("hello ghp_abc123\n", map[string]string{"EMPTY": "", "GH_TOKEN": "ghp_abc123"})

	// Assert
	assert.Equal(t, got, "hello [gritz:masked GH_TOKEN]\n")
}

// TestString_NoSecrets asserts an empty map leaves the stream untouched, which
// is what an undeclared workspace ships.
func TestString_NoSecrets(t *testing.T) {
	t.Parallel()
	// Act
	got := String("nothing to hide\n", nil)

	// Assert
	assert.Equal(t, got, "nothing to hide\n")
}

// TestString_SharedValue asserts two names on one value resolve to the same
// marker every run: the trie keeps one value per key, so the winner cannot be
// left to map iteration order.
func TestString_SharedValue(t *testing.T) {
	t.Parallel()
	// Arrange
	secrets := map[string]string{"B_TOKEN": "ghp_abc123", "A_TOKEN": "ghp_abc123"}

	// Act & Assert -- the first name in lexical order wins, on every pass over
	// a freshly ranged map.
	for range 20 {
		assert.Equal(t, String("run ghp_abc123\n", secrets), "run [gritz:masked A_TOKEN]\n")
	}
}

// TestString_BinaryValues asserts the mask is over bytes, not runes: a secret
// is matched as the exact byte string it was declared as.
func TestString_BinaryValues(t *testing.T) {
	t.Parallel()
	// Act
	got := String("pre \x00\xff\x01 post", map[string]string{"RAW": "\x00\xff\x01"})

	// Assert
	assert.Equal(t, got, "pre [gritz:masked RAW] post")
}
