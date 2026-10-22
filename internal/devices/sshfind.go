package devices

import "os"

// envSSH is the explicit OpenSSH path override.
func envSSH() string {
	return os.Getenv("PLUTO_SSH")
}
