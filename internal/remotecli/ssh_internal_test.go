package remotecli

import (
	"reflect"
	"testing"
)

// The tty branch is chosen from console.IsTerminal(os.Stdin), which cannot be
// exercised through RunClient without allocating a pty; this private test
// covers the `ssh -t` shape that the client seam cannot reach.
func TestRemoteSSHArgsAllocateATtyWhenInteractive(t *testing.T) {
	args := remoteSSHArgs("host", "pluto", []string{"attach"}, "", true)
	want := []string{"-t", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new", "--", "host", "pluto", "attach"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want a tty request", args)
	}
}

// A client context reaches the remote command through env(1), which needs no
// AcceptEnv configuration on the host sshd.
func TestRemoteSSHArgsPassTheClientContextThroughEnv(t *testing.T) {
	args := remoteSSHArgs("host", "/usr/local/bin/pluto", []string{"status"}, "{}", false)
	want := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new", "--", "host",
		"env", "PLUTO_CLIENT_CONTEXT='{}'", "/usr/local/bin/pluto", "status"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want the context before the remote pluto", args)
	}
}
