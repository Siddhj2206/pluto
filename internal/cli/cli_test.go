package cli

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRepoFromRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:o/r.git":             "o/r",
		"git@github.com:o/r":                 "o/r",
		"https://github.com/o/r.git\n":       "o/r",
		"https://github.com/o/r":             "o/r",
		"https://token@github.com/o/r.git":   "o/r",
		"ssh://git@github.com/o/my.repo.git": "o/my.repo",
		"https://gitlab.com/o/r.git":         "",
		"https://github.com.evil.com/o/r":    "",
	}
	for in, want := range cases {
		got, ok := repoFromRemote(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestStripCredentials(t *testing.T) {
	got := stripCredentials("https://x-access-token:ghp_SECRET@gitlab.example.com/a/b\n")
	if strings.Contains(got, "SECRET") || got != "https://gitlab.example.com/a/b" {
		t.Fatalf("got %q", got)
	}
	if got := stripCredentials("git@gitlab.com:a/b.git"); got != "git@gitlab.com:a/b.git" {
		t.Fatalf("got %q", got)
	}
}

func TestUnitFileQuotesAwkwardPaths(t *testing.T) {
	unit := unitFile("/opt/my apps/pluto", "/usr/bin:/mnt/c/Program Files/x:/odd%path")
	for _, want := range []string{
		`ExecStart="/opt/my apps/pluto" host run`,
		`Environment="PATH=/usr/bin:/mnt/c/Program Files/x:/odd%%path"`,
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("missing %s in:\n%s", want, unit)
		}
	}
}

func TestEnterResult(t *testing.T) {
	shell := exec.Command("sh", "-c", "exit 1").Run()
	if err := enterResult(shell); err != nil {
		t.Errorf("a shell's last status isn't an error: %v", err)
	}
	engine := exec.Command("sh", "-c", "exit 125").Run()
	if err := enterResult(engine); err == nil {
		t.Error("the engine failing should be an error")
	}
	other := errors.New("couldn't start")
	if err := enterResult(other); !errors.Is(err, other) {
		t.Errorf("got %v", err)
	}
}
