//go:build windows

package devices

import "testing"

// Regression: Windows rejects FlushFileBuffers on a directory handle with
// "Access denied". writeFileAtomic renames the file into place before this
// step, so the save is already durable; syncDir must not turn that into a
// failed save. This runs the real Windows directory handle, so it fails if the
// flush is attempted.
func TestSyncDirDoesNotFlushTheDirectoryOnWindows(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir: %v", err)
	}
}
