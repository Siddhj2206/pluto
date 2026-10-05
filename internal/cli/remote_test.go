package cli

import (
	"reflect"
	"testing"
)

func TestRemoteSSHArgsRunsPlutoOnTheTarget(t *testing.T) {
	args := remoteSSHArgs("siddhant@neptuno", []string{"up", "--worktree", "/home/sid/app"}, false)
	want := []string{"--", "siddhant@neptuno", "pluto", "up", "--worktree", "/home/sid/app"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want %q", args, want)
	}
}

func TestRemoteSSHArgsAllocateATtyWhenInteractive(t *testing.T) {
	args := remoteSSHArgs("host", []string{"attach"}, true)
	want := []string{"-t", "--", "host", "pluto", "attach"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want a tty request", args)
	}
}

func TestRemoteSSHArgsQuoteWordsForTheRemoteShell(t *testing.T) {
	args := remoteSSHArgs("host", []string{"run", "mybox", "--", "echo", "hello world", "it's"}, false)
	want := []string{"--", "host", "pluto", "run", "mybox", "--", "echo", "'hello world'", `'it'\''s'`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteSSHArgs = %q, want words quoted for the remote shell", args)
	}
}
