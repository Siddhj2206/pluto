package cli_test

import (
	"strings"
	"testing"
)

// ADR 0009: `pluto help` prints grouped top-level usage on stdout and exits 0.
func TestHelpPrintsGroupedUsageOnStdout(t *testing.T) {
	code, out, errOut := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("help exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if errOut != "" {
		t.Fatalf("help stderr = %q, want empty", errOut)
	}
	for _, want := range []string{
		"usage: pluto",
		"boxes:",
		"work:",
		"images:",
		"devices:",
		"host:",
		"up ", "run ", "attach ", "pause ", "destroy ", "ls ", "status ",
		"jobs ", "logs ", "image ", "device ", "daemon ", "install ",
		"uninstall ", "version ",
		"'pluto help <command>'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output = %q, want %q", out, want)
		}
	}
	// box and vsock are internal and stay out of the listing.
	for _, hidden := range []string{"\n  box", "\n  vsock"} {
		if strings.Contains(out, hidden) {
			t.Fatalf("help output lists internal command %q:\n%s", hidden, out)
		}
	}
}

// ADR 0009: a bare pluto prints usage on stderr and exits 2.
func TestBarePlutoIsAUsageErrorOnStderr(t *testing.T) {
	code, out, errOut := runCLI(t)
	if code != 2 {
		t.Fatalf("bare pluto exit = %d, want 2", code)
	}
	if out != "" {
		t.Fatalf("bare pluto stdout = %q, want empty", out)
	}
	for _, want := range []string{"pluto: no command", "usage: pluto"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("bare pluto stderr = %q, want %q", errOut, want)
		}
	}
}

// ADR 0009: an unknown command gets the closest match plus a next step.
func TestUnknownCommandSuggestsTheClosestMatch(t *testing.T) {
	code, out, errOut := runCLI(t, "statuss")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if out != "" {
		t.Fatalf("unknown command stdout = %q, want empty", out)
	}
	for _, want := range []string{
		`pluto: unknown command "statuss"`,
		"next: did you mean 'pluto status'?",
		"usage: pluto",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("unknown command stderr = %q, want %q", errOut, want)
		}
	}
}

func TestUnknownCommandWithoutACloseMatchPointsAtHelp(t *testing.T) {
	code, _, errOut := runCLI(t, "frobnicate")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	for _, want := range []string{
		`pluto: unknown command "frobnicate"`,
		"next: run 'pluto help' to see the available commands",
		"usage: pluto",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("unknown command stderr = %q, want %q", errOut, want)
		}
	}
}

// ADR 0009: `pluto help <command>` and `<command> -h/--help` print
// per-command details with examples.
func TestCommandHelpPrintsDetailsAndExamples(t *testing.T) {
	for _, args := range [][]string{
		{"help", "run"},
		{"run", "-h"},
		{"run", "--help"},
	} {
		code, out, errOut := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if errOut != "" {
			t.Fatalf("%v stderr = %q, want empty", args, errOut)
		}
		for _, want := range []string{"usage: pluto run", "examples:", "pluto run test", "pluto run -- pnpm test"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v output = %q, want %q", args, out, want)
			}
		}
	}
}

func TestEveryVisibleCommandAnswersHelp(t *testing.T) {
	for _, name := range []string{
		"up", "run", "attach", "pause", "ls", "status", "jobs", "logs",
		"destroy", "image", "device", "daemon", "install", "uninstall",
		"help", "version",
	} {
		code, out, errOut := runCLI(t, name, "-h")
		if code != 0 {
			t.Fatalf("%s -h exit = %d, want 0 (stderr %q)", name, code, errOut)
		}
		if !strings.Contains(out, "usage: pluto "+name) {
			t.Fatalf("%s -h output = %q, want its usage", name, out)
		}
		if !strings.Contains(out, "examples:") {
			t.Fatalf("%s -h output = %q, want examples", name, out)
		}
	}
}

func TestSubcommandHelp(t *testing.T) {
	for _, args := range [][]string{
		{"help", "device", "add"},
		{"device", "add", "-h"},
		{"help", "image", "import"},
		{"image", "import", "--help"},
	} {
		code, out, errOut := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(out, "usage: pluto") || !strings.Contains(out, "examples:") {
			t.Fatalf("%v output = %q, want subcommand usage and examples", args, out)
		}
	}
}

// After '--' the words are the command's argv: '--help' must run as work,
// not trigger help.
func TestHelpFlagAfterDashDashIsWork(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)

	code, out, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "echo", "--help")
	if code != 0 {
		t.Fatalf("run -- echo --help exit = %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out, "usage: pluto run") {
		t.Fatalf("run -- echo --help printed help instead of running: %q", out)
	}
}

func TestTopLevelHelpFlagsPrintUsageOnStdout(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}} {
		code, out, errOut := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(out, "usage: pluto") || !strings.Contains(out, "boxes:") {
			t.Fatalf("%v stdout = %q, want the grouped usage", args, out)
		}
		if errOut != "" {
			t.Fatalf("%v stderr = %q, want empty", args, errOut)
		}
	}
}
