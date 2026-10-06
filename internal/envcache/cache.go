// Package envcache stores immutable, opt-in provisioned rootfs layers. A layer
// is content-addressed by project identity, declared setup fingerprint, base
// image, and trust class, so another repo or an incompatible setup can never
// resolve to it. Callers publish only after a successful provision and a clean
// guest shutdown, and scrub per-box state before the layer becomes visible.
package envcache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Siddhj2206/pluto/internal/fsutil"
	"github.com/Siddhj2206/pluto/internal/hexid"
)

// Trust is the class a layer belongs to. Layers are never shared across
// classes: a trusted layer cannot satisfy an untrusted lookup and vice versa.
type Trust string

const (
	Trusted   Trust = "trusted"
	Untrusted Trust = "untrusted"
)

// Valid reports whether t is a known trust class.
func (t Trust) Valid() bool { return t == Trusted || t == Untrusted }

// Inputs identify every declared input that scopes a reusable layer.
type Inputs struct {
	// Project is the primary repository identity, not a per-work-item path.
	Project string
	// Setup is the contract's declared setup fingerprint (its SetupHash):
	// the base image, tools, and provision inputs.
	Setup string
	// Image is the resolved base-image version the layer was provisioned on.
	Image string
	// Trust is the trust class the layer belongs to.
	Trust Trust
}

// Key returns the content address for a layer. Missing or unknown scoping
// inputs fail closed rather than collapsing unrelated environments.
func Key(in Inputs) (string, error) {
	if strings.TrimSpace(in.Project) == "" || strings.TrimSpace(in.Setup) == "" || strings.TrimSpace(in.Image) == "" {
		return "", errors.New("environment cache key requires project, setup, and image identities")
	}
	if !in.Trust.Valid() {
		return "", fmt.Errorf("unknown environment cache trust class %q", in.Trust)
	}
	value := strings.Join([]string{string(in.Trust), in.Project, in.Setup, in.Image}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]), nil
}

// ErrMiss reports that no layer exists for a key.
var ErrMiss = errors.New("environment cache miss")

// ErrBuilding reports that another box is already provisioning this missing
// layer. The caller must not start a duplicate build.
var ErrBuilding = errors.New("environment layer build already in progress")

// Cache is a host-local store of immutable rootfs layers.
type Cache struct{ Root string }

// LayerDir returns the directory holding a published layer's rootfs.img, or
// ErrMiss when the key has no complete layer.
func (c Cache) LayerDir(key string) (string, error) {
	layer, err := c.layerPath(key)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(layer); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrMiss
		}
		return "", fmt.Errorf("stat environment layer: %w", err)
	} else if !info.Mode().IsRegular() {
		return "", errors.New("environment layer is not a regular file")
	}
	return filepath.Dir(layer), nil
}

// Publish installs a layer atomically. source is a complete provisioned rootfs
// image; prep runs against the private copy before it becomes visible, so
// per-box state is scrubbed while a half-written layer can never be read. A
// key that already resolves is left untouched: a published layer is immutable.
func (c Cache) Publish(key, source string, prep func(string) error) error {
	layer, err := c.layerPath(key)
	if err != nil {
		return err
	}
	if info, err := os.Stat(layer); err == nil && info.Mode().IsRegular() {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat environment layer: %w", err)
	}
	dir := filepath.Dir(layer)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create environment cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".rootfs-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary environment layer: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	if err := fsutil.CloneFile(source, tmpPath); err != nil {
		return fmt.Errorf("copy provisioned environment: %w", err)
	}
	if prep != nil {
		if err := prep(tmpPath); err != nil {
			return fmt.Errorf("scrub provisioned environment: %w", err)
		}
	}
	if err := os.Chmod(tmpPath, 0o444); err != nil {
		return fmt.Errorf("make environment layer immutable: %w", err)
	}
	if err := syncFile(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, layer); err != nil {
		return fmt.Errorf("publish environment layer: %w", err)
	}
	return syncDir(dir)
}

func (c Cache) layerPath(key string) (string, error) {
	if !validKey(key) {
		return "", errors.New("invalid environment cache key")
	}
	return filepath.Join(c.Root, key[:2], key, "rootfs.img"), nil
}

func validKey(key string) bool {
	return hexid.Valid(key, 64)
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open environment layer for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync environment layer: %w", err)
	}
	return nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open environment cache directory: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync environment cache directory: %w", err)
	}
	return nil
}
