//go:build !windows

package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTheDatabaseAndItsSidecarsArePrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	s, path := open(t)
	if err := s.PutRepo(context.Background(), Repo{Name: "o/r", Token: "secret", ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %o", filepath.Base(f), info.Mode().Perm())
		}
	}
}
