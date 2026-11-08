package cli

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/store"
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

func TestCommandLineKeepsArgumentBoundaries(t *testing.T) {
	if got := commandLine([]string{"a && b"}); got != "a && b" {
		t.Errorf("one argument is shell: %q", got)
	}
	if got := commandLine([]string{"grep", "foo bar", "it's"}); got != `'grep' 'foo bar' 'it'\''s'` {
		t.Errorf("several keep their boundaries: %q", got)
	}
}

func TestParseTakesFlagsAnywhereButNotAfterDashes(t *testing.T) {
	fs := newFlags("t")
	detach := fs.Bool("detach", false, "")
	got, err := parse(fs, []string{"build", "--detach", "--", "--not-a-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if !*detach || len(got) != 2 || got[0] != "build" || got[1] != "--not-a-flag" {
		t.Fatalf("detach=%v args=%q", *detach, got)
	}
	if _, err := parse(newFlags("t"), []string{"--nope"}); err == nil {
		t.Fatal("an unknown flag should be a usage error")
	}
}

func TestResolveFindsNestedCommandsAndAliases(t *testing.T) {
	root := commands()
	for args, want := range map[string]string{
		"schedule run build": "run",
		"box ls":             "list",
		"secret rm X":        "delete",
		"history --json":     "history",
		"nonsense":           "pluto",
	} {
		cmd, _ := resolve(root, strings.Fields(args))
		if cmd.name != want {
			t.Errorf("%q resolved to %q, want %q", args, cmd.name, want)
		}
	}
}

func TestEveryListTakesJSON(t *testing.T) {
	var walk func(c *command, path string)
	walk = func(c *command, path string) {
		if c.name == "list" && !strings.Contains(c.args, "--json") {
			t.Errorf("%s list doesn't take --json", path)
		}
		for _, s := range c.subs {
			walk(s, path+" "+s.name)
		}
	}
	walk(commands(), "pluto")
}

func TestNoInputIsGlobalUntilDashes(t *testing.T) {
	noInput = false
	t.Cleanup(func() { noInput = false })
	got := globalFlags([]string{"box", "reset", "--no-input", "--", "--no-input"})
	if !noInput || strings.Join(got, " ") != "box reset -- --no-input" {
		t.Fatalf("noInput=%v args=%q", noInput, got)
	}
}

func TestHelpFlagInsideAnExecCommandIsTheCommands(t *testing.T) {
	if !wantsHelp([]string{"--help"}) || wantsHelp([]string{"--", "-h"}) {
		t.Fatal("help detection")
	}
}

func TestNoInputInsideAnExecCommandIsTheCommands(t *testing.T) {
	noInput = false
	t.Cleanup(func() { noInput = false })
	got := globalFlags([]string{"exec", "--detach", "ls", "--no-input"})
	if noInput || strings.Join(got, " ") != "exec --detach ls --no-input" {
		t.Fatalf("noInput=%v args=%q", noInput, got)
	}
}

func TestRepoNamesAreLowercase(t *testing.T) {
	if got, _ := repoFromRemote("git@github.com:Owner/Repo.git"); got != "owner/repo" {
		t.Errorf("got %q", got)
	}
	if key, _ := remoteKey("https://gitlab.com/Owner/Repo.git"); key != "gitlab.com/Owner/Repo" {
		t.Errorf("got %q", key)
	}
}

func TestGlobalFlagsTakeYesAndNoInput(t *testing.T) {
	yes, noInput = false, false
	defer func() { yes, noInput = false, false }()
	got := globalFlags([]string{"box", "grant", "--privileged", "--yes", "--no-input"})
	if strings.Join(got, " ") != "box grant --privileged" || !yes || !noInput {
		t.Fatalf("got %v yes=%v noInput=%v", got, yes, noInput)
	}
	yes = false
	got = globalFlags([]string{"box", "reset", "-y"})
	if strings.Join(got, " ") != "box reset" || !yes {
		t.Fatalf("got %v yes=%v", got, yes)
	}
	yes = false
	got = globalFlags([]string{"exec", "ls", "--yes"})
	if yes || len(got) != 3 {
		t.Fatalf("exec's command words are not Pluto's: %v", got)
	}
}

func TestNoteShowsWhyARunWaitsAndWhatToRun(t *testing.T) {
	r := store.Run{State: store.StateQueued, Waiting: "waiting for r_a (task build) to finish in this repo's box", WaitingNext: "pluto logs r_a"}
	if got, want := note(r), "waiting for r_a (task build) to finish in this repo's box (pluto logs r_a)"; got != want {
		t.Fatalf("note = %q, want %q", got, want)
	}
}
