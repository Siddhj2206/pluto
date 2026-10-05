package contract_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// A declared session parses with its command, description, dir, and env, and
// resolves to an execution spec like a job (ticket #55).
func TestParseSession(t *testing.T) {
	c, err := contract.Parse(`
[env]
FOO = "top"

[sessions.agent]
description = "the coding agent"
command = ["opencode", "run"]
dir = "app"
env = { FOO = "session" }
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	agent, ok := c.Sessions["agent"]
	if !ok {
		t.Fatalf("sessions = %+v, want agent", c.Sessions)
	}
	if agent.Description != "the coding agent" || strings.Join(agent.Command.Argv(), " ") != "opencode run" || agent.Dir != "app" {
		t.Fatalf("sessions.agent = %+v", agent)
	}

	exec, err := c.SessionExec("agent")
	if err != nil {
		t.Fatalf("SessionExec: %v", err)
	}
	if exec.Dir != "app" || strings.Join(exec.Command.Argv(), " ") != "opencode run" || exec.Timeout != 0 {
		t.Fatalf("SessionExec = %+v", exec)
	}
	if exec.Env["FOO"] != "session" {
		t.Fatalf("session env = %v, want the session value over the top level", exec.Env)
	}
}

func TestSessionsValidated(t *testing.T) {
	cases := map[string]string{
		"bad name":         "[sessions.\"bad name\"]\ncommand = \"x\"\n",
		"missing command":  "[sessions.agent]\ndescription = \"x\"\n",
		"empty command":    "[sessions.agent]\ncommand = \"\"\n",
		"empty argv":       "[sessions.agent]\ncommand = []\n",
		"number command":   "[sessions.agent]\ncommand = 5\n",
		"non-string argv":  "[sessions.agent]\ncommand = [\"ok\", 5]\n",
		"reserved env":     "[sessions.agent]\ncommand = \"x\"\nenv = { PLUTO_X = \"1\" }\n",
		"unknown key":      "[sessions.agent]\ncommand = \"x\"\nbogus = \"y\"\n",
		"port rejected":    "[sessions.agent]\ncommand = \"x\"\nport = 3000\n",
		"timeout rejected": "[sessions.agent]\ncommand = \"x\"\ntimeout = \"30m\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

// The TOML decoder rejects a duplicate [sessions.<name>] table at the second
// header; Load renders the parser position as file:line (ADR 0009). No manual
// duplicate detection is needed.
func TestDuplicateSessionNamesFailAtParse(t *testing.T) {
	body := "[sessions.agent]\ncommand = \"a\"\n[sessions.agent]\ncommand = \"b\"\n"
	if _, err := contract.Parse(body); err == nil {
		t.Fatal("Parse should reject a duplicate session table")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, contract.FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := contract.Load(dir)
	if err == nil {
		t.Fatal("Load should reject a duplicate session table")
	}
	if want := fmt.Sprintf("%s:3:", path); !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if !errors.Is(err, contract.ErrInvalid) {
		t.Fatalf("error = %v, want it to match contract.ErrInvalid", err)
	}
}

func TestSessionNamesSorted(t *testing.T) {
	c, err := contract.Parse("[sessions.web]\ncommand = \"x\"\n[sessions.db]\ncommand = \"y\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(c.SessionNames(), ","); got != "db,web" {
		t.Fatalf("SessionNames = %q, want db,web", got)
	}
}

func TestSessionExecUnknownListsDeclaredSessions(t *testing.T) {
	c, err := contract.Parse(`
[sessions.agent]
description = "the coding agent"
command = "opencode"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = c.SessionExec("tui")
	if err == nil {
		t.Fatal("SessionExec(tui) should fail")
	}
	if !errors.Is(err, contract.ErrNoSuchSession) {
		t.Fatalf("error = %v, want ErrNoSuchSession", err)
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Fatalf("error = %q, want it to list the declared sessions", err)
	}

	empty, err := contract.Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := empty.SessionExec("agent"); err == nil || !strings.Contains(err.Error(), "no sessions declared") {
		t.Fatalf("empty error = %v, want 'no sessions declared'", err)
	}
}

func TestContractWithSessionsIsNotEmpty(t *testing.T) {
	c, err := contract.Parse("[sessions.agent]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Empty() {
		t.Fatal("a contract with a declared session should not be empty")
	}
}

func TestSessionEnvMergesPerKeyOverTheTopLevel(t *testing.T) {
	c, err := contract.Parse(`
[env]
FOO = "top"
BAR = "top"

[sessions.agent]
command = "true"
env = { BAR = "session", BAZ = "session" }
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	exec, err := c.SessionExec("agent")
	if err != nil {
		t.Fatalf("SessionExec: %v", err)
	}
	want := map[string]string{"FOO": "top", "BAR": "session", "BAZ": "session"}
	if len(exec.Env) != len(want) {
		t.Fatalf("session env = %v, want %v", exec.Env, want)
	}
	for k, v := range want {
		if exec.Env[k] != v {
			t.Fatalf("session env[%s] = %q, want %q", k, exec.Env[k], v)
		}
	}
}
