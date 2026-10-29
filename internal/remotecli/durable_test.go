package remotecli_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/remotecli"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// The Windows client replays declared-job commands verbatim: discovery
// (`job ls`), a declared job against a box, and the one-off spelling with its
// `--` fence. The host's stdout returns through the client.
func TestRunClientDispatchesDeclaredJobCommands(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")

	cases := []struct {
		name string
		args []string
	}{
		{"discover", []string{"job", "ls"}},
		{"discover a box's jobs", []string{"job", "ls", "box-1"}},
		{"declared job on a box", []string{"run", "box-1", "test"}},
		{"job named like the run manager", []string{"run", "box-1", "ls"}},
		{"one-off after the fence", []string{"run", "box-1", "--", "sh", "-c", "echo hi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := testssh.Start(t, "jobs: dev test\n", "", 0)
			var out, errOut bytes.Buffer
			code := remotecli.Run("neptuno", tc.args, &out, &errOut)
			if code != 0 {
				t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
			}
			if !strings.Contains(out.String(), "jobs: dev test") {
				t.Fatalf("stdout = %q, want the host's output streamed", out.String())
			}
			want := wantRemoteArgv("siddhant@neptuno", "pluto", tc.args)
			if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
				t.Fatalf("ssh argv = %q, want %q", got, want)
			}
		})
	}
}

// Durable inspection (task and run list/show/logs) reaches the host with the
// same words and the same ids, so resolution happens where the state lives.
func TestRunClientDispatchesDurableInspection(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")

	cases := []struct {
		name string
		args []string
	}{
		{"task list", []string{"task", "ls"}},
		{"task show", []string{"task", "show", "12ab34cd"}},
		{"task logs", []string{"task", "logs", "12ab34cd"}},
		{"task logs tail", []string{"task", "logs", "12ab34cd", "--lines", "20"}},
		{"task logs follow", []string{"task", "logs", "12ab34cd", "--follow"}},
		{"run list", []string{"run", "ls"}},
		{"run show", []string{"run", "show", "12ab34cd"}},
		{"run logs", []string{"run", "logs", "12ab34cd"}},
		{"run logs follow", []string{"run", "logs", "12ab34cd", "--follow"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := testssh.Start(t, "task 12ab34cd completed\n", "", 0)
			var out, errOut bytes.Buffer
			code := remotecli.Run("neptuno", tc.args, &out, &errOut)
			if code != 0 {
				t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
			}
			if !strings.Contains(out.String(), "task 12ab34cd completed") {
				t.Fatalf("stdout = %q, want the host's output streamed", out.String())
			}
			want := wantRemoteArgv("siddhant@neptuno", "pluto", tc.args)
			if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
				t.Fatalf("ssh argv = %q, want %q", got, want)
			}
		})
	}
}

// A job that writes several lines returns them in order on the client side:
// the remote stdout is streamed through unchanged, not reordered or dropped.
func TestRunClientStreamsJobOutputInOrder(t *testing.T) {
	deviceConfig(t)
	testssh.Start(t, "building\nlinking\nrunning\n", "", 0)

	var out, errOut bytes.Buffer
	if code := remotecli.Run("sid@host", []string{"run", "box-1", "build"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
	}
	previous := -1
	for _, line := range []string{"building", "linking", "running"} {
		at := strings.Index(out.String(), line)
		if at < 0 {
			t.Fatalf("stdout = %q, want %q", out.String(), line)
		}
		if at <= previous {
			t.Fatalf("stdout = %q, want %q before %q", out.String(), line, out.String()[previous:])
		}
		previous = at
	}
}

// A declared job that fails returns its exit status through the client, so a
// script or pipeline on Windows sees what the job saw.
func TestRunClientReturnsAFailedJobExitStatus(t *testing.T) {
	deviceConfig(t)
	testssh.Start(t, "2 of 5 checks failed\n", "", 4)

	var out, errOut bytes.Buffer
	code := remotecli.Run("sid@host", []string{"run", "box-1", "test"}, &out, &errOut)
	if code != 4 {
		t.Fatalf("exit = %d, want the job's 4 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "2 of 5 checks failed") {
		t.Fatalf("stdout = %q, want the job output before the exit status", out.String())
	}
}
