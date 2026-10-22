package cli

import (
	"fmt"
	"io"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
)

const healthUsage = "usage: pluto health"

// runHealth answers "which daemon am I talking to, and since when?" with one
// static read: status, version, state version, uptime, socket, state directory,
// and the box count. It never changes state.
func runHealth(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "health", commandHelp, stdout) {
		return 0
	}
	if len(args) != 0 {
		return usageError(stderr, "health takes no arguments", healthUsage)
	}
	h, err := client.New(socket).Health()
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "status:        %s\n", orDash(h.Status))
	fmt.Fprintf(stdout, "version:       %s\n", orDash(h.Version))
	fmt.Fprintf(stdout, "state version: %d\n", h.StateVersion)
	fmt.Fprintf(stdout, "uptime:        %s\n", uptimeLabel(h.UptimeMS))
	fmt.Fprintf(stdout, "socket:        %s\n", orDash(h.Socket))
	fmt.Fprintf(stdout, "state dir:     %s\n", orDash(h.StateDir))
	fmt.Fprintf(stdout, "boxes:         %d\n", h.Boxes)
	return 0
}
