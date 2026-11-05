package host

import "errors"

// DiskFree isn't implemented on Windows, which can't be a host yet.
func DiskFree(string) (uint64, error) { return 0, errors.New("not supported on Windows") }
