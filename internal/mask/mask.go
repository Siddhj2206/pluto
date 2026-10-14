// Package mask redacts secret values from an output stream before its bytes
// reach a durable log or a client. Masking is a sliding window, so a value or a
// prefix split across two writes is redacted whole instead of half-emitted.
//
// Two kinds of entry are registered. An exact value — a secret resolved from a
// host environment variable or file — is redacted literally, its own bytes and
// no more. A prefix — an explicit `mask:` literal such as a token's fixed
// leading characters — is redacted together with the run of token characters
// that follows it, so `mask: ["ghp_"]` hides `ghp_abc123` whole. A token
// character is an ASCII letter, digit, `_`, or `-`; the run ends at the first
// other byte, so `ghp_abc.def` redacts only `ghp_abc`.
//
// A Masker is the one host-side registry every output surface draws from: a
// run's retained log and live stream today, and attach/watch (#144) next.
package mask

import (
	"bytes"
	"sort"
	"strings"
	"sync"
)

// Placeholder replaces a redacted value in the stream.
const Placeholder = "[REDACTED]"

// entry is one registered redaction: its text and whether it is a prefix.
type entry struct {
	text   string
	prefix bool
}

// Masker redacts registered values and prefixes from a byte stream. It is safe
// for concurrent use.
//
// Mask consumes the next chunk and returns the prefix that is safe to publish
// now; it holds back a tail shorter than the longest registered entry, so a
// value or prefix split across two chunks is redacted whole. A prefix whose
// token run reaches the end of the held bytes holds from the prefix's start
// until the run ends or the stream does. Flush releases that held-back tail
// when the stream ends, so no bytes are dropped.
type Masker struct {
	mu      sync.Mutex
	entries []entry // longest first, so an overlap redacts as a whole
	max     int     // len of the longest registered entry
	pending []byte  // bytes held back, awaiting more input or Flush
}

// New returns a Masker registered with exact values. Empty and duplicate values
// are ignored, so a caller can pass declared values directly.
func New(secrets ...string) *Masker {
	m := &Masker{}
	for _, secret := range secrets {
		m.Register(secret)
	}
	return m
}

// Register adds an exact value to redact: its own bytes and no more. An empty
// value is ignored and a duplicate is a no-op. The sliding window grows to the
// longest registered entry.
func (m *Masker) Register(secret string) {
	m.register(entry{text: secret})
}

// RegisterPrefix adds a prefix to redact together with the run of token
// characters that follows it. An empty prefix is ignored and a duplicate is a
// no-op. The sliding window grows to the longest registered entry.
func (m *Masker) RegisterPrefix(prefix string) {
	m.register(entry{text: prefix, prefix: true})
}

// register adds an entry, ignoring an empty or duplicate one.
func (m *Masker) register(e entry) {
	if e.text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.entries {
		if existing == e {
			return
		}
	}
	m.entries = append(m.entries, e)
	// Longest first: replacing a shorter entry that is a prefix of a longer one
	// would leave the longer one's tail exposed.
	sort.SliceStable(m.entries, func(i, j int) bool {
		return len(m.entries[i].text) > len(m.entries[j].text)
	})
	m.max = len(m.entries[0].text)
}

// Empty reports whether no entry is registered, so a caller can skip masking.
func (m *Masker) Empty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries) == 0
}

// Mask consumes the next chunk of a stream and returns the masked bytes safe to
// publish now, holding back a tail as long as the longest registered entry. The
// returned slice never ends in the middle of a registered value or prefix run.
func (m *Masker) Mask(p []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return p
	}
	m.pending = append(m.pending, p...)
	cut := m.safeCut()
	if cut == 0 {
		return nil
	}
	return m.take(cut)
}

// safeCut returns how many leading bytes of pending are safe to publish: the
// largest prefix that ends neither inside a registered value nor inside a
// prefix's token run. The caller holds m.mu.
func (m *Masker) safeCut() int {
	n := len(m.pending)
	// Hold back a tail shorter than the longest registered entry, so a value or
	// prefix split across two chunks is not published half-formed.
	cut := n - m.max + 1
	if cut < 0 {
		cut = 0
	}
	// Collect each match's [start, end) extent. An exact value's extent is its
	// own bytes; a prefix's is the run of token characters that follows it. A
	// prefix whose run reaches the end of the buffer may continue into the next
	// chunk, so it constrains the cut from below instead of extending it.
	type span struct{ start, end int }
	var spans []span
	hold := n
	for _, e := range m.entries {
		value := []byte(e.text)
		for off := 0; off < n; {
			i := bytes.Index(m.pending[off:], value)
			if i < 0 {
				break
			}
			i += off
			if e.prefix {
				end := tokenRunEnd(m.pending, i+len(value))
				if end == n {
					if i < hold {
						hold = i
					}
				} else {
					spans = append(spans, span{i, end})
				}
			} else {
				spans = append(spans, span{i, i + len(value)})
			}
			off = i + 1
		}
	}
	// Extend past any match that would otherwise straddle the cut, so the whole
	// match is published at once and redacted in take.
	for {
		crossed := -1
		for _, s := range spans {
			if s.start < cut && s.end > cut && s.end > crossed {
				crossed = s.end
			}
		}
		if crossed < 0 {
			break
		}
		cut = crossed
	}
	// A prefix run that reaches the end caps the cut. If a match would then
	// straddle it, hold from that match's start rather than half-emit it.
	if hold < cut {
		cut = hold
		for {
			start := cut
			for _, s := range spans {
				if s.start < cut && s.end > cut && s.start < start {
					start = s.start
				}
			}
			if start == cut {
				break
			}
			cut = start
		}
	}
	return cut
}

// Flush returns any bytes held back by Mask, masked, and clears the window.
// Call it when the stream ends so the final bytes are published rather than
// dropped.
func (m *Masker) Flush() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	return m.take(len(m.pending))
}

// take removes and returns the first n bytes of the held-back buffer, masked.
// The caller holds m.mu and guarantees n does not fall inside a registered
// value or prefix run.
func (m *Masker) take(n int) []byte {
	if n > len(m.pending) {
		n = len(m.pending)
	}
	data := string(m.pending[:n])
	m.pending = append(m.pending[:0], m.pending[n:]...)
	// Prefixes first: a prefix consumes its token run, which may contain an
	// exact value's bytes, and redacting the whole run covers them.
	for _, e := range m.entries {
		if e.prefix {
			data = replacePrefixRun(data, e.text)
		}
	}
	for _, e := range m.entries {
		if !e.prefix {
			data = strings.ReplaceAll(data, e.text, Placeholder)
		}
	}
	return []byte(data)
}

// isTokenChar reports whether b belongs to the token alphabet a mask prefix
// consumes: an ASCII letter, digit, '_', or '-'.
func isTokenChar(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	}
	return false
}

// tokenRunEnd returns the first index at or after from that is not a token
// character, or len(data) when the run reaches the end.
func tokenRunEnd(data []byte, from int) int {
	for from < len(data) && isTokenChar(data[from]) {
		from++
	}
	return from
}

// replacePrefixRun replaces every prefix occurrence and the run of token
// characters following it with the placeholder.
func replacePrefixRun(data, prefix string) string {
	var b strings.Builder
	for {
		i := strings.Index(data, prefix)
		if i < 0 {
			b.WriteString(data)
			return b.String()
		}
		b.WriteString(data[:i])
		b.WriteString(Placeholder)
		data = data[tokenRunEnd([]byte(data), i+len(prefix)):]
	}
}
