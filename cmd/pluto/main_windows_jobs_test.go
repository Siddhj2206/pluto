//go:build windows

package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/testssh"
)

// The shipped executable dispatches declared-job commands to the host with
// their arguments intact: discovery, a job on a box, and the one-off spelling
// with its `--` fence. The shared fake ssh records argv, so this covers the
// Windows CI path as well as the returned streams.
func TestWindowsClientDispatchesDeclaredJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	t.Setenv("FAKE_SSH_STDOUT", "jobs: dev test\n")

	cases := []struct {
		name string
		args []string
	}{
		{"discover", []string{"job", "ls"}},
		{"discover a box's jobs", []string{"job", "ls", "box-1"}},
		{"declared job on a box", []string{"run", "box-1", "test"}},
		{"one-off after the fence", []string{"run", "box-1", "--", "sh", "-c", "echo hi"}},
	}
	for _, tc := range cases {
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatalf("%s: reset ssh log: %v", tc.name, err)
		}
		code, out, errOut := runPluto(t, exe, "", append([]string{"--device", "sid@neptuno"}, tc.args...)...)
		if code != 0 {
			t.Fatalf("%s exit = %d, stderr: %s", tc.name, code, errOut)
		}
		if !strings.Contains(out, "jobs: dev test") {
			t.Fatalf("%s stdout = %q, want the host's output streamed", tc.name, out)
		}
		want := clientSSHArgs("sid@neptuno", tc.args...)
		if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s ssh argv = %q, want %q", tc.name, got, want)
		}
	}
}

// Durable task and run list/show/logs operations reach the host with the same
// words and ids, so state resolves on the machine that owns it.
func TestWindowsClientDispatchesDurableInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	t.Setenv("FAKE_SSH_STDOUT", "task 12ab34cd completed\n")

	cases := []struct {
		name string
		args []string
	}{
		{"task list", []string{"task", "ls"}},
		{"task show", []string{"task", "show", "12ab34cd"}},
		{"task logs", []string{"task", "logs", "12ab34cd"}},
		{"run list", []string{"run", "ls"}},
		{"run show", []string{"run", "show", "12ab34cd"}},
		{"run logs", []string{"run", "logs", "12ab34cd"}},
	}
	for _, tc := range cases {
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatalf("%s: reset ssh log: %v", tc.name, err)
		}
		code, out, errOut := runPluto(t, exe, "", append([]string{"--device", "sid@neptuno"}, tc.args...)...)
		if code != 0 {
			t.Fatalf("%s exit = %d, stderr: %s", tc.name, code, errOut)
		}
		if !strings.Contains(out, "task 12ab34cd completed") {
			t.Fatalf("%s stdout = %q, want the host's output streamed", tc.name, out)
		}
		want := clientSSHArgs("sid@neptuno", tc.args...)
		if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s ssh argv = %q, want %q", tc.name, got, want)
		}
	}
}

// A remote job that writes several lines returns them in order through the
// real Windows process, so the terminal sees the job's output sequence.
func TestWindowsClientStreamsJobOutputInOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	t.Setenv("FAKE_SSH_STDOUT", "building\nlinking\nrunning\n")

	code, out, errOut := runPluto(t, exe, "", "--device", "sid@neptuno", "run", "box-1", "build")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	previous := -1
	for _, line := range []string{"building", "linking", "running"} {
		at := strings.Index(out, line)
		if at < 0 {
			t.Fatalf("stdout = %q, want %q", out, line)
		}
		if at <= previous {
			t.Fatalf("stdout = %q, want %q in order", out, line)
		}
		previous = at
	}
}

// A failing declared job returns its remote exit status through the Windows
// process unchanged, after its output has been streamed.
func TestWindowsClientReturnsRemoteJobExitStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)

	t.Setenv("FAKE_SSH_STDOUT", "2 of 5 checks failed\n")
	t.Setenv("FAKE_SSH_EXIT", "4")

	code, out, errOut := runPluto(t, exe, "", "--device", "sid@neptuno", "run", "box-1", "test")
	if code != 4 {
		t.Fatalf("exit = %d, want the remote exit status 4 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "2 of 5 checks failed") {
		t.Fatalf("stdout = %q, want the job output before the exit status", out)
	}
}
