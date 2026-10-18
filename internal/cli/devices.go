package cli

import (
	"io"

	"github.com/Siddhj2206/pluto/internal/remotecli"
)

// runDevice manages the client-side registry of saved ssh destinations. The
// behavior is shared with the Windows client; see internal/remotecli.
func runDevice(args []string, stdout, stderr io.Writer) int {
	return remotecli.RunDevice(args, stdout, stderr)
}
