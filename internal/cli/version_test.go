package cli_test

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/cli"
)

// Ticket #61: a release overrides Version at link time; a `go install
// ...@vX.Y.Z` binary has no link-time override and carries the tag in its Go
// build info instead.
func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name         string
		linkVersion  string
		buildVersion string
		want         string
	}{
		{"link-time tag wins", "v0.1.0", "v9.9.9", "v0.1.0"},
		{"build info fills a dev build", "0.1.0-dev", "v0.1.0", "v0.1.0"},
		{"dev build without build info", "0.1.0-dev", "", "0.1.0-dev"},
		{"local build records (devel)", "0.1.0-dev", "(devel)", "0.1.0-dev"},
		{"empty link-time override falls back", "", "v0.1.0", "v0.1.0"},
	}
	for _, tc := range cases {
		if got := cli.ResolveVersion(tc.linkVersion, tc.buildVersion); got != tc.want {
			t.Errorf("%s: ResolveVersion(%q, %q) = %q, want %q",
				tc.name, tc.linkVersion, tc.buildVersion, got, tc.want)
		}
	}
}

// `pluto version` and `--version` print the resolved version, so a linked
// release binary reports the tag it was built from.
func TestVersionReportsTheLinkTimeVersion(t *testing.T) {
	old := cli.Version
	cli.Version = "v0.1.0"
	t.Cleanup(func() { cli.Version = old })

	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, errOut := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(out, "v0.1.0") {
			t.Errorf("%v stdout = %q, want the link-time version", args, out)
		}
	}
}
