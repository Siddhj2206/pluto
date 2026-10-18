//go:build windows

// The Windows binary is client-only: it drives a Linux host over SSH and
// links no host packages (state, daemon, vsock, and friends), which do not
// cross-compile because they use unix syscalls.
package main

import (
	"os"

	"github.com/Siddhj2206/pluto/internal/remotecli"
)

func main() {
	os.Exit(remotecli.RunClient(os.Args[1:], os.Stdout, os.Stderr))
}
