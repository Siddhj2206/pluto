package host

import (
	"errors"
	"os"
)

// Lock isn't implemented on Windows, which can't be a host yet.
func Lock(string) (*os.File, error) { return nil, errors.New("not supported on Windows") }
