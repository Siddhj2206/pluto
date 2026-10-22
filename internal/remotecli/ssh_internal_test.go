package remotecli

import (
	"reflect"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// The tty branch is chosen from console.IsTerminal(os.Stdin), which cannot be
// exercised through RunClient without allocating a pty; this private test
// covers the `ssh -tt` shape that the client seam cannot reach.
func TestRemoteSSHArgsAllocateATtyWhenInteractive(t *testing.T) {
	p := devices.Profile{Destination: "host"}
	args := p.Command(devices.SSHOptions{TTY: true, BatchMode: true}, "pluto", []string{"attach"})
	want := []string{"-tt", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new", "--", "host", "sh", "-c", "'pluto attach'"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("attach argv = %q, want a tty request", args)
	}
}
