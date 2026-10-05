package shquote_test

import (
	"testing"

	"github.com/Siddhj2206/pluto/internal/shquote"
)

func TestQuote(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"/usr/bin/pluto", "/usr/bin/pluto"},
		{"/opt/my tools/pluto", "'/opt/my tools/pluto'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
		{"a;b", "'a;b'"},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
	} {
		if got := shquote.Quote(tc.in); got != tc.want {
			t.Fatalf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
