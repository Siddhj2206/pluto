// Package mask redacts secret values from an output stream before its bytes
// reach a durable log or a client. Masking is a sliding window as long as the
// longest registered value, so a value split across two writes is redacted
// whole instead of half-emitted.
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

// Masker redacts registered values from a byte stream. It is safe for
// concurrent use.
//
// Mask consumes the next chunk and returns the prefix that is safe to publish
// now; it holds back a tail shorter than the longest registered value, so a
// value split across two chunks is redacted whole. Flush releases that
// held-back tail when the stream ends, so no bytes are dropped.
type Masker struct {
	mu      sync.Mutex
	secrets []string // longest first, so an overlap redacts as a whole
	max     int      // len of the longest registered value
	pending []byte   // bytes held back, awaiting more input or Flush
}

// New returns a Masker registered with secrets. Empty and duplicate values are
// ignored, so a caller can pass declared values directly.
func New(secrets ...string) *Masker {
	m := &Masker{}
	for _, secret := range secrets {
		m.Register(secret)
	}
	return m
}

// Register adds a value to redact. An empty value is ignored and a duplicate is
// a no-op. The sliding window grows to the longest registered value.
func (m *Masker) Register(secret string) {
	if secret == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.secrets {
		if s == secret {
			return
		}
	}
	m.secrets = append(m.secrets, secret)
	// Longest first: replacing a shorter value that is a prefix of a longer
	// one would leave the longer one's tail exposed.
	sort.SliceStable(m.secrets, func(i, j int) bool {
		return len(m.secrets[i]) > len(m.secrets[j])
	})
	m.max = len(m.secrets[0])
}

// Empty reports whether no value is registered, so a caller can skip masking.
func (m *Masker) Empty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.secrets) == 0
}

// Mask consumes the next chunk of a stream and returns the masked bytes safe to
// publish now, holding back a tail as long as the longest registered value. The
// returned slice never ends in the middle of a registered value.
func (m *Masker) Mask(p []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.secrets) == 0 {
		return p
	}
	m.pending = append(m.pending, p...)
	// Publish all but a tail shorter than the longest registered value, then
	// extend past any value that would otherwise straddle the boundary.
	cut := len(m.pending) - m.max + 1
	if cut < 0 {
		cut = 0
	}
	for {
		crossed := -1
		for _, secret := range m.secrets {
			value := []byte(secret)
			for off := 0; off < len(m.pending); {
				i := bytes.Index(m.pending[off:], value)
				if i < 0 {
					break
				}
				i += off
				if end := i + len(value); i < cut && end > cut && end > crossed {
					crossed = end
				}
				off = i + 1
			}
		}
		if crossed < 0 {
			break
		}
		cut = crossed
	}
	if cut == 0 {
		return nil
	}
	return m.take(cut)
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
// The caller holds m.mu.
func (m *Masker) take(n int) []byte {
	if n > len(m.pending) {
		n = len(m.pending)
	}
	data := string(m.pending[:n])
	m.pending = append(m.pending[:0], m.pending[n:]...)
	for _, secret := range m.secrets {
		data = strings.ReplaceAll(data, secret, Placeholder)
	}
	return []byte(data)
}
