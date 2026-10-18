package remotecli

import (
	"reflect"
	"testing"
)

// The tty branch is chosen from console.IsTerminal(os.Stdin), which cannot be
// exercised through RunClient without allocating a pty; this private test
// covers the `ssh -t` shape that the client seam cannot reach.
func TestRemoteSSHArgsAllocateATtyWhenInteractive(t *testing.T) {
	args := remoteSSHArgs("host", []string{"attach"}, true)
	want := []string{"-t", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new", "--", "host", "pluto", "attach"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want a tty request", args)
	}
}
