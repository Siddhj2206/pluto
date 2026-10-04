// Command pluto-agent is the in-box guest agent placeholder. It keeps the
// image path and systemd unit in place so the box exercises the service seam
// on every boot; the real vsock protocol lands with the guest-agent ticket.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	fmt.Fprintln(os.Stderr, "pluto-agent: placeholder, no protocol yet")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}
