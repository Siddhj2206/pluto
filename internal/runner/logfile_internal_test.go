package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotatingLogBoundsAndKeepsHistory proves the log file stays under the cap
// by rotating once: the newest writes live in the current file and the
// previous segment is retained beside it.
func TestRotatingLogBoundsAndKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serial.log")
	log, err := openRotatingLog(path, 32)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	if _, err := log.Write([]byte(strings.Repeat("a", 20))); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := log.Write([]byte(strings.Repeat("b", 20))); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if !strings.HasPrefix(string(current), "bbbb") {
		t.Fatalf("current = %q, want the newest bytes", current)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated: %v", err)
	}
	if !strings.HasPrefix(string(rotated), "aaaa") {
		t.Fatalf("rotated = %q, want the previous segment", rotated)
	}
}
