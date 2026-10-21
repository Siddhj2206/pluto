package cli

import (
	"io"

	"github.com/Siddhj2206/pluto/internal/remotecli"
)

// runRemote re-executes the command on a device over ssh. The behavior is
// shared with the Windows client; see internal/remotecli.
func runRemote(deviceName string, argv []string, stdout, stderr io.Writer) int {
	if deviceName == "" || len(argv) == 0 {
		usage(stderr)
		return 2
	}
	return remotecli.Run(deviceName, argv, stdout, stderr)
}
