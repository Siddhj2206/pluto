package cli_test

import (
	"io"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// ADR 0009: every user-facing failure prints `pluto: <what happened>` and, when
// it has one, a `next: <step>` line after it. This guard spans the new commands
// so the voice cannot erode silently.
func TestFailuresKeepThePlutoThenNextVoice(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  build: { run: [\"false\"], lock: exclusive }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, _ container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		return container.ExecResult{ExitCode: 1}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	cases := []struct {
		name string
		args []string
	}{
		{"a failing run", []string{"run", "build"}},
		{"a failing ci run", []string{"ci", "build"}},
		{"an unknown task", []string{"run", "nope"}},
		{"an unknown run", []string{"show", "deadbeef"}},
		{"logs for an unknown run", []string{"logs", "deadbeef"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runCLI(t, append([]string{"--socket", socket}, tc.args...)...)
			if code == 0 {
				t.Fatalf("%v succeeded, want a failure", tc.args)
			}
			assertErrorVoice(t, errOut)
		})
	}
}

// assertErrorVoice proves stderr carries a `pluto:` line and a `next:` line, in
// that order.
func assertErrorVoice(t *testing.T, stderr string) {
	t.Helper()
	plutoAt, nextAt := -1, -1
	for i, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "pluto: ") && plutoAt < 0 {
			plutoAt = i
		}
		if strings.HasPrefix(line, "next: ") && nextAt < 0 {
			nextAt = i
		}
	}
	if plutoAt < 0 {
		t.Fatalf("stderr = %q, want a 'pluto:' line", stderr)
	}
	if nextAt < 0 {
		t.Fatalf("stderr = %q, want a 'next:' line", stderr)
	}
	if nextAt < plutoAt {
		t.Fatalf("stderr = %q, want the 'next:' line after the 'pluto:' line", stderr)
	}
}
