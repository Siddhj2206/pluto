package fsutil_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/fsutil"
)

func TestCloneFileCopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	content := []byte("hello pluto\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := fsutil.CloneFile(src, dst); err != nil {
		t.Fatalf("CloneFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("dst = %q, want %q", got, content)
	}
}

func TestCloneFileCopiesLargeContentAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	content := make([]byte, 1<<20)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("stale bytes that must disappear"), 0o644); err != nil {
		t.Fatalf("write stale dst: %v", err)
	}
	if err := fsutil.CloneFile(src, dst); err != nil {
		t.Fatalf("CloneFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(content) {
		t.Fatal("dst content does not match src")
	}
}

func TestCloneFileMissingSource(t *testing.T) {
	if err := fsutil.CloneFile(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("CloneFile should fail when src is missing")
	}
}
