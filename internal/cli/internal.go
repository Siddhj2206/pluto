package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Siddhj2206/pluto/internal/runner"
	"github.com/Siddhj2206/pluto/internal/vsock"
)

// Hidden commands that the per-box units and ssh integration invoke. They
// are not part of the user-facing surface. `pluto box` is the user-facing box
// noun; the per-box unit's processes live under `pluto internal`.

// runInternal runs the per-box unit's processes: "box-run" is the unit's
// ExecStart, "box-holder" is the process inside the box's rootless namespace.
func runInternal(args []string, stateDir string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pluto internal <box-run <id>|box-holder <cfg> <api-sock> <firecracker>>")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "box-run":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: pluto internal box-run <id>")
			return 2
		}
		if err := runner.BoxRun(ctx, stateDir, args[1]); err != nil {
			fmt.Fprintf(stderr, "pluto internal box-run: %v\n", err)
			return 1
		}
		return 0
	case "box-holder":
		if len(args) != 4 {
			fmt.Fprintln(stderr, "usage: pluto internal box-holder <cfg> <api-sock> <firecracker>")
			return 2
		}
		if err := runner.BoxHolder(ctx, args[1], args[2], args[3]); err != nil {
			fmt.Fprintf(stderr, "pluto internal box-holder: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown internal subcommand %q\n", args[0])
		return 2
	}
}

// runVsock bridges stdio to a guest port through a Firecracker vsock UDS; it
// is the ssh ProxyCommand for attach.
func runVsock(args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "connect" {
		fmt.Fprintln(stderr, "usage: pluto vsock connect <uds-path> <port>")
		return 2
	}
	port, err := strconv.ParseUint(args[2], 10, 32)
	if err != nil {
		fmt.Fprintf(stderr, "pluto vsock: invalid port %q\n", args[2])
		return 2
	}
	if err := vsock.Proxy(args[1], uint32(port), os.Stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "pluto vsock: %v\n", err)
		return 1
	}
	return 0
}
