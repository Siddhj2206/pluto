package cli_test

import (
	"encoding/json"
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
		{"link-time tag wins", "v0.1.5", "v9.9.9", "v0.1.5"},
		{"build info fills a dev build", "0.1.5-dev", "v0.1.5", "v0.1.5"},
		{"dev build without build info", "0.1.5-dev", "", "0.1.5-dev"},
		{"local build records (devel)", "0.1.5-dev", "(devel)", "0.1.5-dev"},
		{"empty link-time override falls back", "", "v0.1.5", "v0.1.5"},
	}
	for _, tc := range cases {
		if got := cli.ResolveVersion(tc.linkVersion, tc.buildVersion); got != tc.want {
			t.Errorf("%s: ResolveVersion(%q, %q) = %q, want %q",
				tc.name, tc.linkVersion, tc.buildVersion, got, tc.want)
		}
	}
}

// `pluto version --json` is the versioned handshake, and a bad flag is a
// usage error in the CLI's voice rather than a bare flag package line.
func TestVersionJSONAndBadFlags(t *testing.T) {
	code, out, errOut := runCLI(t, "version", "--json")
	if code != 0 {
		t.Fatalf("version --json exit = %d, stderr %q", code, errOut)
	}
	var info struct {
		SchemaVersion int    `json:"schema_version"`
		Version       string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("decode version JSON: %v (%s)", err, out)
	}
	if info.SchemaVersion != 1 || info.Version == "" {
		t.Fatalf("version JSON = %+v, want schema_version 1 and a version", info)
	}

	code, _, errOut = runCLI(t, "version", "--bogus")
	if code != 2 {
		t.Fatalf("version --bogus exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "pluto:") || !strings.Contains(errOut, "usage: pluto version [--json]") {
		t.Fatalf("stderr = %q, want the pluto-prefixed usage", errOut)
	}
}

// `pluto version` and `--version` print the resolved version, so a linked
// release binary reports the tag it was built from.
func TestVersionReportsTheLinkTimeVersion(t *testing.T) {
	old := cli.Version
	cli.Version = "v0.1.5"
	t.Cleanup(func() { cli.Version = old })

	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, errOut := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(out, "v0.1.5") {
			t.Errorf("%v stdout = %q, want the link-time version", args, out)
		}
	}
}
