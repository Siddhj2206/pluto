package mask_test

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/mask"
)

// maskAll pushes a whole stream through the masker in one chunk and returns
// what would be published, including the tail Flush releases.
func maskAll(m *mask.Masker, chunk string) string {
	return string(append(m.Mask([]byte(chunk)), m.Flush()...))
}

// A value that appears whole in one chunk is redacted in place.
func TestMaskerRedactsAWholeValueInOneChunk(t *testing.T) {
	m := mask.New("s3cr3t")
	got := maskAll(m, "token=s3cr3t\n")
	if want := "token=[REDACTED]\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// A value split across two chunks is redacted whole, not half-emitted. The
// window is as long as the longest registered value, so the first chunk's tail
// is held back until the second chunk completes the value.
func TestMaskerRedactsAValueSplitAcrossChunks(t *testing.T) {
	m := mask.New("host-secret-value")
	var out strings.Builder
	out.Write(m.Mask([]byte("push token=host-se")))
	out.Write(m.Mask([]byte("cret-value done\n")))
	out.Write(m.Flush())
	got := out.String()
	if strings.Contains(got, "host-secret-value") {
		t.Fatalf("masked stream leaked the split value: %q", got)
	}
	if want := "push token=[REDACTED] done\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// A complete value at the very end of the stream is redacted, not dropped or
// leaked, even with nothing after it.
func TestMaskerRedactsAValueAtTheEndOfTheStream(t *testing.T) {
	m := mask.New("s3cr3t")
	if got, want := maskAll(m, "token=s3cr3t"), "token=[REDACTED]"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// Bytes held back to complete the window are released by Flush, so a stream
// that ends before any value is complete drops nothing.
func TestMaskerFlushReleasesTheHeldTail(t *testing.T) {
	m := mask.New("s3cr3t")
	held := string(m.Mask([]byte("token=s3cr")))
	tail := string(m.Flush())
	if got, want := held+tail, "token=s3cr"; got != want {
		t.Fatalf("held+flush = %q, want %q", got, want)
	}
	if held == "" {
		t.Fatal("Mask emitted the whole stream, want a held tail")
	}
}

// A shorter value that is a prefix of a longer one does not eat the longer
// one's tail: the whole longer value is redacted.
func TestMaskerRedactsOverlappingValuesLongestFirst(t *testing.T) {
	m := mask.New("abc", "abcd")
	if got, want := maskAll(m, "value=abcd\n"), "value=[REDACTED]\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// A value that straddles the window boundary later in a single chunk is still
// caught: the masker scans every occurrence, not only the first.
func TestMaskerCatchesALaterStraddlingOccurrence(t *testing.T) {
	m := mask.New("ab")
	if got, want := maskAll(m, "xxabab"), "xx[REDACTED][REDACTED]"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// Chunking never changes the result: the concatenation of masked chunks plus
// the flush equals the value masked as one string.
func TestMaskerResultIsIndependentOfChunking(t *testing.T) {
	const stream = "one s3cr3t two AKIA-short three s3cr3t\n"
	m := mask.New("s3cr3t", "AKIA-short")
	var out strings.Builder
	for i := 0; i < len(stream); i += 3 {
		end := i + 3
		if end > len(stream) {
			end = len(stream)
		}
		out.Write(m.Mask([]byte(stream[i:end])))
	}
	out.Write(m.Flush())
	got := out.String()
	want := maskAll(mask.New("s3cr3t", "AKIA-short"), stream)
	if got != want {
		t.Fatalf("chunked = %q, want %q", got, want)
	}
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "AKIA-short") {
		t.Fatalf("masked stream leaked a value: %q", got)
	}
}

// Output with no registered value passes through unchanged.
func TestMaskerLeavesCleanOutputUntouched(t *testing.T) {
	m := mask.New("s3cr3t")
	if got, want := maskAll(m, "nothing to hide\n"), "nothing to hide\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// An explicit mask literal, such as a token's fixed prefix, is redacted the
// same way a declared value is.
func TestMaskerRedactsAMaskLiteral(t *testing.T) {
	m := mask.New("ghp_")
	got := maskAll(m, "token ghp_abc123\n")
	if strings.Contains(got, "ghp_") {
		t.Fatalf("masked = %q, want the literal gone", got)
	}
	if want := "token [REDACTED]abc123\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// Registering nothing masks nothing and allocates no window.
func TestMaskerWithNoSecretsIsAPassThrough(t *testing.T) {
	m := mask.New()
	if !m.Empty() {
		t.Fatal("Empty = false for a masker with no secrets")
	}
	if got, want := maskAll(m, "plain output\n"), "plain output\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// Empty and duplicate registrations are ignored, so a declaration cannot
// corrupt the stream or redact everything.
func TestMaskerIgnoresEmptyAndDuplicateRegistrations(t *testing.T) {
	m := mask.New("")
	m.Register("")
	m.Register("dup")
	m.Register("dup")
	if m.Empty() {
		t.Fatal("Empty = true, want the real value registered")
	}
	if got, want := maskAll(m, "dup dup\n"), "[REDACTED] [REDACTED]\n"; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}
