// Layer content addressing: provision compiles to two content-addressed
// layers (ADR 0017). The base layer is keyed on provision.base; the package
// layer is keyed on the base's resolved key plus provision.packages, the
// inline steps, and the bytes of the watched lockfiles. Hashing is pure and
// deterministic so an unchanged declaration is a cache hit and any edit is a
// miss (#140).
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
)

// missingLockfile is the marker a watched lockfile hashes as when it is not
// present, so creating or deleting it changes the package layer's key.
const missingLockfile = "\x00pluto:lockfile-missing"

// BaseDigest returns the content address of provision.base: the OCI
// reference, the Dockerfile's bytes, or the inline steps. It is stable across
// reloads and changes on any edit to the base's declared content.
func (pv *Provision) BaseDigest(worktree string) (string, error) {
	d := newDigest("pluto/base/v1")
	switch pv.Base.Kind {
	case BaseImage:
		d.add("image", pv.Base.Image)
	case BaseDockerfile:
		path := pv.Base.Dockerfile
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(worktree, path)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return "", fmt.Errorf("provision.base.dockerfile: %w", err)
		}
		d.add("dockerfile", filepath.ToSlash(path), hashBytes(data))
	case BaseRun:
		d.add("run", pv.Base.Run.String())
	default:
		return "", fmt.Errorf("provision.base: unsupported base form")
	}
	return d.sum(), nil
}

// PackageDigest returns the content address of the package layer. It binds the
// base layer's resolved key (a base change rebuilds the package layer too),
// the declared packages, the inline steps, and the bytes of every watched
// lockfile. A missing lockfile hashes as missing.
func (pv *Provision) PackageDigest(worktree, baseKey string) (string, error) {
	d := newDigest("pluto/package/v1")
	d.add("base", baseKey)
	d.add("packages", fmt.Sprintf("%d", len(pv.Packages)))
	for _, pkg := range pv.Packages {
		d.add("package", pkg)
	}
	if pv.Run.IsZero() {
		d.add("run", "")
	} else {
		d.add("run", pv.Run.String())
	}
	for _, name := range pv.Watch {
		full := name
		if !filepath.IsAbs(full) {
			full = filepath.Join(worktree, name)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", fmt.Errorf("provision.watch %s: %w", name, err)
			}
			d.add("watch", filepath.ToSlash(name), missingLockfile)
			continue
		}
		d.add("watch", filepath.ToSlash(name), hashBytes(data))
	}
	return d.sum(), nil
}

// HasPackageLayer reports whether provision declares content for a package
// layer: packages to install or inline steps to run on top of the base. A
// bare watch list does not make one.
func (pv *Provision) HasPackageLayer() bool {
	return len(pv.Packages) > 0 || !pv.Run.IsZero()
}

// digestBuilder accumulates length-prefixed fields so no two different
// sequences of parts render to the same bytes.
type digestBuilder struct {
	h hash.Hash
}

func newDigest(namespace string) *digestBuilder {
	d := &digestBuilder{h: sha256.New()}
	d.add(namespace)
	return d
}

func (d *digestBuilder) add(parts ...string) {
	for _, p := range parts {
		fmt.Fprintf(d.h, "%d\x00%s\x00", len(p), p)
	}
}

func (d *digestBuilder) sum() string {
	return hex.EncodeToString(d.h.Sum(nil))
}

// hashBytes is the sha256 of a file's bytes, hex-encoded.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
