// Command pluto-vsock bridges stdio to a guest port through a Firecracker
// vsock UDS (for ssh ProxyCommand) and waits for guest services to come up.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
		connect(os.Args[2], uint32(port))
	case "wait":
		timeout := 60 * time.Second
		if len(os.Args) > 4 {
			if secs, err := strconv.Atoi(os.Args[4]); err == nil {
				timeout = time.Duration(secs) * time.Second
			}
		}
		wait(os.Args[2], uint32(port), timeout)
	default:
		usage()
	}
}

func connect(uds string, port uint32) {
	conn, err := vsock.Connect(uds, port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pluto-vsock:", err)
		os.Exit(1)
	}
	go func() {
		io.Copy(conn, os.Stdin)
		conn.Close()
	}()
	io.Copy(os.Stdout, conn)
}

// wait polls until the guest port answers with a banner, prints it, and
// exits 0; it exits 1 after the timeout.
func wait(uds string, port uint32, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := vsock.Connect(uds, port)
		if err == nil {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 256)
			n, _ := conn.Read(buf)
			conn.Close()
			if n > 0 {
				fmt.Println(strings.TrimSpace(string(buf[:n])))
				return
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "pluto-vsock: timed out waiting for guest port", port)
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  pluto-vsock connect <uds-path> <port>          bridge stdio (ssh ProxyCommand)
  pluto-vsock wait <uds-path> <port> [seconds]   wait for the guest banner`)
	os.Exit(2)
}
