package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// ADR 0009: a failure names the next step. These are the newer read surfaces;
// each must print a pluto-prefixed line, a next: hint, and exit 1 when the
// daemon cannot answer.
func TestNewReadSurfacesFailWithANextStep(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sock")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"events", []string{"events"}},
		{"overview", []string{"overview"}},
		{"timeline", []string{"timeline", "no-such-target"}},
		{"webhook", []string{"webhook", "ls"}},
		{"health", []string{"health"}},
	} {
		code, _, errOut := runCLI(t, append([]string{"--socket", missing}, tc.args...)...)
		if code != 1 {
			t.Errorf("%s without a daemon: exit = %d, want 1 (stderr %q)", tc.name, code, errOut)
			continue
		}
		if !strings.Contains(errOut, "pluto:") {
			t.Errorf("%s stderr = %q, want a pluto: failure line", tc.name, errOut)
		}
		if !strings.Contains(errOut, "next:") {
			t.Errorf("%s stderr = %q, want a next: hint", tc.name, errOut)
		}
	}
}
