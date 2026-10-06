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

// ADR 0011: `pluto --version` is the conventional alias of `pluto version`,
// as git, cargo, and mise all provide.
func TestVersionFlagMatchesTheVersionCommand(t *testing.T) {
	wantCode, wantOut, wantErr := runCLI(t, "version")
	if wantCode != 0 {
		t.Fatalf("version exit = %d, want 0 (stderr %q)", wantCode, wantErr)
	}
	for _, args := range [][]string{{"--version"}, {"-version"}} {
		code, out, errOut := runCLI(t, args...)
		if code != wantCode || out != wantOut || errOut != wantErr {
			t.Fatalf("%v = (%d, %q, %q), want (%d, %q, %q)",
				args, code, out, errOut, wantCode, wantOut, wantErr)
		}
	}
}

// ADR 0011: a per-command flag parse error speaks in the CLI's voice and prints
// the command's usage line, instead of the flag package's bare output.
func TestUnknownCommandFlagIsAUsageErrorInTheCLIVoice(t *testing.T) {
	cases := []struct {
		args  []string
		usage string
	}{
		{[]string{"status", "--nope"}, "usage: pluto status <box-id|worktree>"},
		{[]string{"up", "--nope"}, "usage: pluto up [--worktree PATH]"},
		{[]string{"logs", "--nope"}, "usage: pluto logs <box-id|worktree>"},
	}
	for _, tc := range cases {
		code, out, errOut := runCLI(t, tc.args...)
		if code != 2 {
			t.Errorf("%v exit = %d, want 2 (stderr %q)", tc.args, code, errOut)
			continue
		}
		if out != "" {
			t.Errorf("%v stdout = %q, want empty", tc.args, out)
		}
		for _, want := range []string{"pluto: flag provided but not defined: -nope", tc.usage} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%v stderr = %q, want %q", tc.args, errOut, want)
			}
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

// Attach help documents the session form and its usage line.
func TestAttachHelpDocumentsSessions(t *testing.T) {
	code, out, errOut := runCLI(t, "help", "attach")
	if code != 0 {
		t.Fatalf("help attach exit = %d, want 0 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"--session NAME", "pluto attach mybox --session agent", "[sessions.NAME]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help attach output = %q, want %q", out, want)
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

// ADR 0009: subcommand help shows that subcommand's own details and examples,
// not the parent's. A generic "usage: pluto" match would let the fallback
// pass unnoticed, so each case asserts the subcommand's unique copy.
func TestSubcommandHelp(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		detail  string
		example string
	}{
		{"device add", []string{"help", "device", "add"}, "Save a nickname for an ssh destination", "pluto device add neptuno siddhant@neptuno"},
		{"device add", []string{"device", "add", "-h"}, "Save a nickname for an ssh destination", "pluto device add neptuno siddhant@neptuno"},
		{"device add", []string{"device", "add", "--help"}, "Save a nickname for an ssh destination", "pluto device add neptuno siddhant@neptuno"},
		{"device ls", []string{"help", "device", "ls"}, "List saved devices.", "pluto device ls"},
		{"device ls", []string{"device", "ls", "-h"}, "List saved devices.", "pluto device ls"},
		{"device rm", []string{"device", "rm", "-h"}, "Remove a saved device.", "pluto device rm neptuno"},
		{"image import", []string{"help", "image", "import"}, "Verify an artifact directory's manifest", "pluto image import images/out"},
		{"image import", []string{"image", "import", "-h"}, "Verify an artifact directory's manifest", "pluto image import images/out"},
		{"image import", []string{"image", "import", "--help"}, "Verify an artifact directory's manifest", "pluto image import images/out"},
		{"image ls", []string{"help", "image", "ls"}, "List imported images with their version", "pluto image ls"},
		{"image ls", []string{"image", "ls", "-h"}, "List imported images with their version", "pluto image ls"},
	}
	for _, tc := range cases {
		code, out, errOut := runCLI(t, tc.args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", tc.args, code, errOut)
		}
		if errOut != "" {
			t.Fatalf("%v stderr = %q, want empty", tc.args, errOut)
		}
		for _, want := range []string{"usage: pluto " + tc.name, tc.detail, "examples:", tc.example} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v output = %q, want %q", tc.args, out, want)
			}
		}
		// The parent's copy must not leak through when a subcommand asks.
		for _, parentOnly := range []string{"Manage the client-side registry", "Import a built image artifact"} {
			if strings.Contains(out, parentOnly) {
				t.Fatalf("%v printed the parent's details: %q", tc.args, out)
			}
		}
	}
}

// The parent commands still answer their own -h with their own details.
func TestParentCommandHelp(t *testing.T) {
	for _, tc := range []struct {
		cmd    string
		detail string
	}{
		{"device", "Manage the client-side registry"},
		{"image", "Import a built image artifact"},
	} {
		for _, flag := range []string{"-h", "--help"} {
			code, out, errOut := runCLI(t, tc.cmd, flag)
			if code != 0 {
				t.Fatalf("%s %s exit = %d, want 0 (stderr %q)", tc.cmd, flag, code, errOut)
			}
			for _, want := range []string{"usage: pluto " + tc.cmd, tc.detail, "examples:"} {
				if !strings.Contains(out, want) {
					t.Fatalf("%s %s output = %q, want %q", tc.cmd, flag, out, want)
				}
			}
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
