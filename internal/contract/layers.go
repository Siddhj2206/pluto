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
	"io/fs"
	"os"
	"path/filepath"
)

// missingLockfile is the marker a watched lockfile hashes as when it is not
// present, so creating or deleting it changes the package layer's key.
const missingLockfile = "\x00pluto:lockfile-missing"

// BaseDigest returns the content address of provision.base: the OCI
// reference, the Dockerfile's bytes, or the inline steps. It is stable across
// reloads and changes on any edit to the base's declared content.
func (pv *Provision) BaseDigest(workspace string) (string, error) {
	d := newDigest("pluto/base/v1")
	switch pv.Base.Kind {
	case BaseImage:
		d.add("image", pv.Base.Image)
	case BaseDockerfile:
		path := pv.Base.Dockerfile
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(workspace, path)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return "", fmt.Errorf("provision.base.dockerfile: %w", err)
		}
		d.add("dockerfile", filepath.ToSlash(path), hashBytes(data))
		// A Dockerfile builds against its directory as the context, so a file
		// the Dockerfile COPYs or ADDs is part of the layer's content: editing
		// it must invalidate the layer. The declared (relative) context path is
		// hashed, not the absolute one, so the address is stable across
		// checkouts on different hosts.
		context := filepath.Dir(full)
		tree, err := hashTree(context)
		if err != nil {
			return "", fmt.Errorf("provision.base.dockerfile context %s: %w", filepath.Dir(path), err)
		}
		d.add("context", filepath.ToSlash(filepath.Dir(path)), tree)
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
func (pv *Provision) PackageDigest(workspace, baseKey string) (string, error) {
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
			full = filepath.Join(workspace, name)
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

// hashTree returns a deterministic content hash of every entry under root, so
// a Dockerfile's build context is part of the base layer's address. Entries are
// hashed by their slash-separated relative path, files by their bytes, and
// symlinks by their target; version-control metadata is ignored. WalkDir visits
// in lexical order, so the result is stable.
func hashTree(root string) (string, error) {
	d := newDigest("pluto/tree/v1")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case entry.IsDir():
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			if rel != "." {
				d.add("dir", rel)
			}
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			d.add("link", rel, target)
		case entry.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			d.add("file", rel, hashBytes(data))
		default:
			d.add("other", rel, entry.Type().String())
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return d.sum(), nil
}
