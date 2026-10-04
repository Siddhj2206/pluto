// Command pluto-vsock bridges stdio to a guest port through a Firecracker
// vsock UDS (for ssh ProxyCommand) and waits for guest services to come up.
// The pluto CLI has the same bridge built in for attach; this binary is for
// scripts and image smoke tests.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Siddhj2206/pluto/internal/vsock"
)

func main() {
	if len(os.Args) < 4 {
		usage()
	}
	port, err := strconv.ParseUint(os.Args[3], 10, 32)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluto-vsock: invalid port %q\n", os.Args[3])
		os.Exit(2)
	}
	switch os.Args[1] {
	case "connect":
		if err := vsock.Proxy(os.Args[2], uint32(port), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "pluto-vsock:", err)
			os.Exit(1)
		}
	case "wait":
		timeout := 60 * time.Second
		if len(os.Args) > 4 {
			secs, err := strconv.Atoi(os.Args[4])
			if err != nil || secs <= 0 {
				fmt.Fprintf(os.Stderr, "pluto-vsock: invalid timeout %q\n", os.Args[4])
				os.Exit(2)
			}
			timeout = time.Duration(secs) * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		banner, err := vsock.WaitReady(ctx, os.Args[2], uint32(port))
		if err != nil {
			fmt.Fprintln(os.Stderr, "pluto-vsock:", err)
			os.Exit(1)
		}
		fmt.Println(banner)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  pluto-vsock connect <uds-path> <port>          bridge stdio (ssh ProxyCommand)
  pluto-vsock wait <uds-path> <port> [seconds]   wait for the guest banner`)
	os.Exit(2)
}
