//go:build windows

package devices

// syncDir is a no-op on Windows. Windows rejects FlushFileBuffers on a
// directory handle with "Access denied", and writeFileAtomic has already
// renamed the file into place, so the save is durable without this step.
// Returning nil keeps a successful save from being reported as a failure.
func syncDir(dir string) error {
	return nil
}
