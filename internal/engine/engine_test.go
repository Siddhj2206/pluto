package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes a script named like an engine that logs its arguments and runs body.
func fakeBin(t *testing.T, name, body string) (*CLI, string) {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$*\" >> " + logFile + "\n" + body + "\n"
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &CLI{Bin: bin}, logFile
}

func argsOf(t *testing.T, logFile string) string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRemoveUsesFlagsEachEngineAccepts(t *testing.T) {
	for name, want := range map[string]string{"podman": "rm -f -v -t 1 box", "docker": "rm -f -v box"} {
		c, logFile := fakeBin(t, name, "exit 0")
		if err := c.Remove(context.Background(), "box"); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(argsOf(t, logFile)); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestRemovingAGoneBoxIsFine(t *testing.T) {
	c, _ := fakeBin(t, "podman", `echo "Error: no such container box" >&2; exit 1`)
	if err := c.Remove(context.Background(), "box"); err != nil {
		t.Fatal(err)
	}
}

func TestRunningReportsAMissingBox(t *testing.T) {
	c, _ := fakeBin(t, "podman", `echo "Error: no such container box" >&2; exit 1`)
	if _, err := c.Running(context.Background(), "box"); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestStartKeepsSecretsOutOfArguments(t *testing.T) {
	c, logFile := fakeBin(t, "podman", "exit 0")
	err := c.Start(context.Background(), Spec{
		Name: "box", Image: "img", Env: map[string]string{"PLUTO_JIT": "super-secret"},
		Cmd: []string{"sh", "-c", "x"}, Files: map[string][]byte{"a.sh": []byte("hi")}, CopyDir: "/tmp/h",
	})
	if err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, logFile)
	if strings.Contains(args, "super-secret") {
		t.Fatalf("secret in arguments: %s", args)
	}
	for _, want := range []string{"--env PLUTO_JIT", "--label pluto.box=1", "cp ", "box:/tmp/h", "start box"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
}

func TestAFailedStartCleansUpTheBox(t *testing.T) {
	c, logFile := fakeBin(t, "podman", `[ "$1" = start ] && { echo broke >&2; exit 1; }; exit 0`)
	if err := c.Start(context.Background(), Spec{Name: "box", Image: "img"}); err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(argsOf(t, logFile), "rm -f") {
		t.Error("the box wasn't removed")
	}
}

func TestHasImage(t *testing.T) {
	c, _ := fakeBin(t, "podman", "echo abc123")
	if ok, err := c.HasImage(context.Background(), "img"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	c, _ = fakeBin(t, "podman", "exit 0")
	if ok, err := c.HasImage(context.Background(), "img"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestImageVolumes(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want []string
	}{
		"declared": {`{"/var/lib/containers":{},"/data/":{}}`, []string{"/data", "/var/lib/containers"}},
		"none":     {"null", nil},
		"empty":    {"{}", nil},
	} {
		c, logFile := fakeBin(t, "podman", "echo '"+tc.out+"'")
		got, err := c.ImageVolumes(context.Background(), "img")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
		if a := argsOf(t, logFile); !strings.Contains(a, "image inspect") || !strings.Contains(a, "img") {
			t.Errorf("%s: args %q", name, a)
		}
	}
}
