package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// provisionOf parses a one-node provision body and returns its Provision.
func provisionOf(t *testing.T, body string) *contract.Provision {
	t.Helper()
	p, err := contract.ParseProject("version: 1\nprovision:\n" + indent(body))
	if err != nil {
		t.Fatalf("ParseProject: %v\n%s", err, body)
	}
	return p.Provision
}

func indent(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestBaseDigestTracksTheDeclaredBase proves a base layer's content address is
// stable for identical content and changes when the base's form or content
// changes, for every accepted form.
func TestBaseDigestTracksTheDeclaredBase(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"pluto/Dockerfile": "FROM ubuntu:24.04\n",
	})

	image := provisionOf(t, "base: ubuntu:24.04")
	dockerfile := provisionOf(t, "base: { dockerfile: ./pluto/Dockerfile }")
	run := provisionOf(t, "base: { run: \"apt-get update\" }")

	imgDigest, err := image.BaseDigest(dir)
	if err != nil {
		t.Fatalf("image.BaseDigest: %v", err)
	}
	dfDigest, err := dockerfile.BaseDigest(dir)
	if err != nil {
		t.Fatalf("dockerfile.BaseDigest: %v", err)
	}
	runDigest, err := run.BaseDigest(dir)
	if err != nil {
		t.Fatalf("run.BaseDigest: %v", err)
	}
	if imgDigest == "" || dfDigest == "" || runDigest == "" {
		t.Fatalf("digests must not be empty: image=%q dockerfile=%q run=%q", imgDigest, dfDigest, runDigest)
	}
	if imgDigest == dfDigest || imgDigest == runDigest || dfDigest == runDigest {
		t.Fatalf("distinct base forms share a digest: %q %q %q", imgDigest, dfDigest, runDigest)
	}

	// A different OCI reference is different content.
	other, err := provisionOf(t, "base: ubuntu:22.04").BaseDigest(dir)
	if err != nil {
		t.Fatalf("BaseDigest: %v", err)
	}
	if other == imgDigest {
		t.Fatal("changing the OCI reference did not change the digest")
	}

	// Editing the Dockerfile changes the base's content.
	writeTree(t, dir, map[string]string{"pluto/Dockerfile": "FROM ubuntu:24.04\nRUN true\n"})
	edited, err := dockerfile.BaseDigest(dir)
	if err != nil {
		t.Fatalf("BaseDigest: %v", err)
	}
	if edited == dfDigest {
		t.Fatal("editing the Dockerfile did not change the base digest")
	}

	// A file in the Dockerfile's build context is content: a Dockerfile that
	// COPYs it must rebuild when it changes.
	writeTree(t, dir, map[string]string{"pluto/app.conf": "one\n"})
	withContext, err := dockerfile.BaseDigest(dir)
	if err != nil {
		t.Fatalf("BaseDigest: %v", err)
	}
	if withContext == edited {
		t.Fatal("adding a build-context file did not change the base digest")
	}
	writeTree(t, dir, map[string]string{"pluto/app.conf": "two\n"})
	editedContext, err := dockerfile.BaseDigest(dir)
	if err != nil {
		t.Fatalf("BaseDigest: %v", err)
	}
	if editedContext == withContext {
		t.Fatal("editing a build-context file did not change the base digest")
	}

	// Inline steps are content too.
	otherRun, err := provisionOf(t, "base: { run: \"apt-get update && apt-get install -y curl\" }").BaseDigest(dir)
	if err != nil {
		t.Fatalf("BaseDigest: %v", err)
	}
	if otherRun == runDigest {
		t.Fatal("changing the inline steps did not change the base digest")
	}
}

// TestBaseDigestNamesAMissingDockerfile proves a base that references a file
// which is not there fails with the path named, rather than silently hashing a
// placeholder.
func TestBaseDigestNamesAMissingDockerfile(t *testing.T) {
	p := provisionOf(t, "base: { dockerfile: ./pluto/nope.Dockerfile }")
	_, err := p.BaseDigest(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "nope.Dockerfile") {
		t.Fatalf("err = %v, want the missing Dockerfile named", err)
	}
}

// TestPackageDigestIsKeyedOnTheWatchedLockfiles proves the package layer is a
// cache hit while the watched lockfiles are unchanged, and a miss when their
// bytes, the package set, the inline steps, or the base key change.
func TestPackageDigestIsKeyedOnTheWatchedLockfiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"Cargo.lock":     "version = 3\n[[package]]\nname = \"serde\"\n",
		"pnpm-lock.yaml": "lockfileVersion: 9\n",
		"README.md":      "not watched\n",
	})

	pv := provisionOf(t, "base: ubuntu:24.04\npackages: [build-essential, nodejs]\nwatch: [Cargo.lock, pnpm-lock.yaml]")

	first, err := pv.PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	// An unrelated file is not part of the package layer's content.
	writeTree(t, dir, map[string]string{"README.md": "still not watched, but different\n"})
	unchanged, err := pv.PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	if unchanged != first {
		t.Fatal("an unwatched file changed the package digest")
	}

	// A watched lockfile's bytes are content.
	writeTree(t, dir, map[string]string{"Cargo.lock": "version = 3\n[[package]]\nname = \"serde\"\nversion = \"1.0.1\"\n"})
	lockChanged, err := pv.PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	if lockChanged == first {
		t.Fatal("editing a watched lockfile did not change the package digest")
	}

	// A base change rebuilds the package layer too: it sits on the base.
	baseChanged, err := pv.PackageDigest(dir, "base:2")
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	if baseChanged == lockChanged {
		t.Fatal("changing the base key did not change the package digest")
	}

	// The package set is content.
	otherPkgs := provisionOf(t, "base: ubuntu:24.04\npackages: [build-essential]\nwatch: [Cargo.lock, pnpm-lock.yaml]")
	pkgsChanged, err := otherPkgs.PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	if pkgsChanged == first {
		t.Fatal("changing the package set did not change the package digest")
	}

	// Inline steps are content.
	otherRun, err := provisionOf(t, "base: ubuntu:24.04\npackages: [build-essential, nodejs]\nwatch: [Cargo.lock, pnpm-lock.yaml]\nrun: \"corepack enable\"").PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	if otherRun == first {
		t.Fatal("adding inline run steps did not change the package digest")
	}
}

// TestPackageDigestDistinguishesAMissingLockfile proves a not-yet-created
// watched lockfile hashes differently from an empty one, so adding it later is
// a rebuild rather than a silent cache hit.
func TestPackageDigestDistinguishesAMissingLockfile(t *testing.T) {
	dir := t.TempDir()
	pv := provisionOf(t, "base: ubuntu:24.04\npackages: [nodejs]\nwatch: [pnpm-lock.yaml]")

	missing, err := pv.PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest(missing): %v", err)
	}
	writeTree(t, dir, map[string]string{"pnpm-lock.yaml": ""})
	empty, err := pv.PackageDigest(dir, "base:1")
	if err != nil {
		t.Fatalf("PackageDigest(empty): %v", err)
	}
	if missing == empty {
		t.Fatal("a missing lockfile and an empty one share a digest")
	}
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

// TestHasPackageLayer proves a provision declares a package layer exactly when
// it has packages or inline steps to run; watch alone does not.
func TestHasPackageLayer(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"base: ubuntu\npackages: [nodejs]\n", true},
		{"base: { run: \"apt-get update\" }\n", false},
		{"base: ubuntu\npackages: [nodejs]\nrun: \"corepack enable\"\n", true},
		{"base: ubuntu\nwatch: [Cargo.lock]\n", false},
		{"base: ubuntu\n", false},
	}
	for _, tc := range cases {
		pv := provisionOf(t, tc.body)
		if got := pv.HasPackageLayer(); got != tc.want {
			t.Errorf("HasPackageLayer(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
