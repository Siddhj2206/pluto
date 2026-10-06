package imagebuilder_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/imagebuilder"
)

const pinsFixture = `# pins.yaml — the single source of truth for every image input.
# Comments and blank lines are ignored.

# renovate: datasource=docker depName=ubuntu
base:
  image: ubuntu:24.04          # floating tag, pinned by digest below
  digest: "sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55"

apt:
  snapshot: "20261001T000000Z"
  packages:
    - ca-certificates
    - curl
    - git

toolchain:
  go: "1.27.1"

firecracker:
  version: 1.17.0
  url: https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz
  sha256: 06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558
kernel:
  url: https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/vmlinux-6.18.51
  sha256: 0545ba1781fc06cfa1d7699069057f4538103fd1644100cf0da434899a1ed447
`

func TestParsePins(t *testing.T) {
	pins, err := imagebuilder.ParsePins([]byte(pinsFixture))
	if err != nil {
		t.Fatalf("ParsePins: %v", err)
	}
	if got, want := pins.Base.Image, "ubuntu:24.04"; got != want {
		t.Errorf("base.image = %q, want %q", got, want)
	}
	if got, want := pins.Base.Digest, "sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55"; got != want {
		t.Errorf("base.digest = %q, want %q", got, want)
	}
	if got, want := pins.Base.String(), "ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55"; got != want {
		t.Errorf("base.String() = %q, want %q", got, want)
	}
	if got, want := pins.Apt.Snapshot, "20261001T000000Z"; got != want {
		t.Errorf("apt.snapshot = %q, want %q", got, want)
	}
	if got, want := strings.Join(pins.Apt.Packages, ","), "ca-certificates,curl,git"; got != want {
		t.Errorf("apt.packages = %q, want %q", got, want)
	}
	if got, want := pins.Toolchain.Go, "1.27.1"; got != want {
		t.Errorf("toolchain.go = %q, want %q", got, want)
	}
	if got, want := pins.Firecracker.Version, "1.17.0"; got != want {
		t.Errorf("firecracker.version = %q, want %q", got, want)
	}
	if got, want := pins.Firecracker.SHA256, "06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558"; got != want {
		t.Errorf("firecracker.sha256 = %q, want %q", got, want)
	}
	if got, want := pins.Kernel.SHA256, "0545ba1781fc06cfa1d7699069057f4538103fd1644100cf0da434899a1ed447"; got != want {
		t.Errorf("kernel.sha256 = %q, want %q", got, want)
	}
}

// TestAptSnapshotEpoch pins the derivation of SOURCE_DATE_EPOCH from the apt
// snapshot id: a temporal field that is stable across rebuilds because it
// comes from the pins, not the clock.
func TestAptSnapshotEpoch(t *testing.T) {
	pins, err := imagebuilder.ParsePins([]byte(pinsFixture))
	if err != nil {
		t.Fatalf("ParsePins: %v", err)
	}
	got, err := pins.Apt.Epoch()
	if err != nil {
		t.Fatalf("Apt.Epoch: %v", err)
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	if got != want {
		t.Errorf("Apt.Epoch() = %d, want %d", got, want)
	}
}

func TestParsePinsRejectsMissingFields(t *testing.T) {
	const packagesBlock = `  packages:
    - ca-certificates
    - curl
    - git
`
	cases := map[string]string{
		"no base image":    without("  image: ubuntu:24.04          # floating tag, pinned by digest below\n", pinsFixture),
		"short digest":     strings.Replace(pinsFixture, "sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55", "sha256:abc", 1),
		"no apt snapshot":  strings.Replace(pinsFixture, `  snapshot: "20261001T000000Z"`, "", 1),
		"bad apt snapshot": strings.Replace(pinsFixture, `  snapshot: "20261001T000000Z"`, `  snapshot: "yesterday"`, 1),
		"no packages":      strings.Replace(pinsFixture, packagesBlock, "  packages:\n", 1),
		"no toolchain":     without("toolchain:\n  go: \"1.27.1\"\n", pinsFixture),
		"no kernel": strings.Replace(pinsFixture, `kernel:
  url: https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/vmlinux-6.18.51
  sha256: 0545ba1781fc06cfa1d7699069057f4538103fd1644100cf0da434899a1ed447
`, "", 1),
		"bad kernel sha": strings.Replace(pinsFixture, "0545ba1781fc06cfa1d7699069057f4538103fd1644100cf0da434899a1ed447", "not-a-hash", 1),
		"no firecracker": without("  version: 1.17.0\n", pinsFixture),
	}
	for name, data := range cases {
		if _, err := imagebuilder.ParsePins([]byte(data)); err == nil {
			t.Errorf("%s: ParsePins succeeded, want an error", name)
		}
	}
}

func TestParsePinsRejectsMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"tab indent": "base:\n\timage: ubuntu:24.04\n",
		"no colon":   "base\n",
	} {
		if _, err := imagebuilder.ParsePins([]byte(data)); err == nil {
			t.Errorf("%s: ParsePins succeeded, want an error", name)
		}
	}
}

// TestLoadRepoPins guards the shipped manifest against drift: the real
// images/pins.yaml must always parse and validate.
func TestLoadRepoPins(t *testing.T) {
	pins, err := imagebuilder.LoadPins("../../images/pins.yaml")
	if err != nil {
		t.Fatalf("LoadPins(images/pins.yaml): %v", err)
	}
	if pins.Kernel.URL == "" || pins.Firecracker.URL == "" || pins.Base.Digest == "" {
		t.Fatalf("repo pins are incomplete: %+v", pins)
	}
	if len(pins.Apt.Packages) == 0 {
		t.Fatal("repo pins list no apt packages")
	}
	if pins.Toolchain.Go == "" {
		t.Fatal("repo pins do not pin the Go toolchain")
	}
}

func without(needle, haystack string) string {
	return strings.Replace(haystack, needle, "", 1)
}
