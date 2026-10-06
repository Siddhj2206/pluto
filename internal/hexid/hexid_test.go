package hexid_test

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/hexid"
)

func TestValid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		lengths []int
		want    bool
	}{
		{"lowercase sha1", strings.Repeat("a", 40), []int{40, 64}, true},
		{"mixed-case sha256", strings.Repeat("A", 64), []int{40, 64}, true},
		{"digits", "0123456789abcdef", []int{16}, true},
		{"wrong length", strings.Repeat("a", 41), []int{40, 64}, false},
		{"non-hex", strings.Repeat("g", 40), []int{40, 64}, false},
		{"empty", "", []int{0}, false},
		{"no lengths", strings.Repeat("a", 40), nil, false},
	} {
		if got := hexid.Valid(tc.value, tc.lengths...); got != tc.want {
			t.Errorf("%s: Valid(%q, %v) = %t, want %t", tc.name, tc.value, tc.lengths, got, tc.want)
		}
	}
}
