package cli

import (
	"reflect"
	"testing"
)

// The tty branch is chosen from isTerminal(os.Stdin), which cannot be
// exercised through cli.Run without allocating a pty; every other
// remoteSSHArgs behavior is asserted at the CLI seam in remote_cli_test.go
// (spec Testing Decisions). Keeping this one internal test preserves the
// `ssh -t` coverage that the CLI seam cannot reach.
func TestRemoteSSHArgsAllocateATtyWhenInteractive(t *testing.T) {
	args := remoteSSHArgs("host", []string{"attach"}, true)
	want := []string{"-t", "--", "host", "pluto", "attach"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want a tty request", args)
	}
}
