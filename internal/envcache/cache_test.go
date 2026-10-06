package envcache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/envcache"
)

func validInputs() envcache.Inputs {
	return envcache.Inputs{
		Project: "https://example.test/acme/app.git",
		Setup:   "setup-sha",
		Image:   "image-sha",
		Trust:   envcache.Trusted,
	}
}

// The key binds every scoping input: a different project, setup, base image,
// or trust class can never resolve to the same layer.
func TestKeyBindsProjectSetupImageAndTrust(t *testing.T) {
	base, err := envcache.Key(validInputs())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*envcache.Inputs){
		"project": func(i *envcache.Inputs) { i.Project += "/other" },
		"setup":   func(i *envcache.Inputs) { i.Setup = "changed" },
		"image":   func(i *envcache.Inputs) { i.Image = "new-image" },
		"trust":   func(i *envcache.Inputs) { i.Trust = envcache.Untrusted },
	} {
		t.Run(name, func(t *testing.T) {
			in := validInputs()
			change(&in)
			got, err := envcache.Key(in)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatal("changed inputs reused the same cache key")
			}
		})
	}
}

// Missing or unknown scoping inputs fail closed instead of collapsing
// unrelated environments into one key.
func TestKeyRejectsIncompleteScoping(t *testing.T) {
	for name, change := range map[string]func(*envcache.Inputs){
		"project": func(i *envcache.Inputs) { i.Project = "" },
		"setup":   func(i *envcache.Inputs) { i.Setup = "" },
		"image":   func(i *envcache.Inputs) { i.Image = "" },
		"trust":   func(i *envcache.Inputs) { i.Trust = "not-a-class" },
	} {
		t.Run(name, func(t *testing.T) {
			in := validInputs()
			change(&in)
			if _, err := envcache.Key(in); err == nil {
				t.Fatal("incomplete scoping produced a key")
			}
		})
	}
}

func TestLayerDirMissesUntilPublished(t *testing.T) {
	cache := envcache.Cache{Root: t.TempDir()}
	key, _ := envcache.Key(validInputs())
	if _, err := cache.LayerDir(key); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("LayerDir before publish = %v, want ErrMiss", err)
	}
}

// Publish installs an immutable layer; a second publish is a no-op rather than
// an overwrite, so a published layer is never replaced in place.
func TestPublishIsAtomicAndIdempotent(t *testing.T) {
	cache := envcache.Cache{Root: t.TempDir()}
	key, _ := envcache.Key(validInputs())
	src := filepath.Join(t.TempDir(), "rootfs.img")
	if err := os.WriteFile(src, []byte("provisioned rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	var prepared []string
	prep := func(path string) error {
		prepared = append(prepared, path)
		return nil
	}
	if err := cache.Publish(key, src, prep); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	dir, err := cache.LayerDir(key)
	if err != nil {
		t.Fatalf("LayerDir: %v", err)
	}
	if len(prepared) != 1 {
		t.Fatalf("prep calls = %d, want the layer scrubbed once", len(prepared))
	}
	layer := filepath.Join(dir, "rootfs.img")
	info, err := os.Stat(layer)
	if err != nil {
		t.Fatalf("stat layer: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("layer mode = %o, want immutable", info.Mode().Perm())
	}
	data, _ := os.ReadFile(layer)
	if string(data) != "provisioned rootfs" {
		t.Fatalf("layer content = %q", data)
	}

	// A second publish of different bytes is ignored: the key already resolves.
	other := filepath.Join(t.TempDir(), "rootfs.img")
	_ = os.WriteFile(other, []byte("different"), 0o644)
	if err := cache.Publish(key, other, nil); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	data, _ = os.ReadFile(layer)
	if string(data) != "provisioned rootfs" {
		t.Fatalf("published layer was overwritten: %q", data)
	}
}

// A failed scrub leaves nothing behind: a rejected layer is never reused.
func TestPublishDiscardsOnPrepareFailure(t *testing.T) {
	cache := envcache.Cache{Root: t.TempDir()}
	key, _ := envcache.Key(validInputs())
	src := filepath.Join(t.TempDir(), "rootfs.img")
	if err := os.WriteFile(src, []byte("provisioned rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := cache.Publish(key, src, func(string) error { return errors.New("scrub failed") })
	if err == nil {
		t.Fatal("Publish should surface a failed scrub")
	}
	if _, err := cache.LayerDir(key); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("LayerDir after failed scrub = %v, want ErrMiss", err)
	}
}

func TestPublishRejectsMalformedKey(t *testing.T) {
	cache := envcache.Cache{Root: t.TempDir()}
	src := filepath.Join(t.TempDir(), "rootfs.img")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	if err := cache.Publish("short", src, nil); err == nil {
		t.Fatal("Publish accepted a malformed key")
	}
	if _, err := cache.LayerDir("short"); err == nil {
		t.Fatal("LayerDir accepted a malformed key")
	}
}
