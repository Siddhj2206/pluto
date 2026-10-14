package cli_test

import (
	"io"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// maskedRun is one run observed through the surfaces a user reads: the live
// stream of `pluto run`, the retained log on disk, and the recorded run.
type maskedRun struct {
	live     string
	retained string
	run      *state.Run
	socket   string
}

// runMaskedTask runs a task whose fake box writes the given output chunks
// through the real CLI and daemon, and returns what each surface saw.
func runMaskedTask(t *testing.T, contract string, output ...string) maskedRun {
	t.Helper()
	repo := gitRepo(t)
	writePlutoYML(t, repo, contract)
	engine := container.NewFake()
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		for _, chunk := range output {
			_, _ = sink.Write([]byte(chunk))
		}
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "run", "dev")
	if code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}
	if len(runs[0].Steps) != 1 || runs[0].Steps[0].Log == "" {
		t.Fatalf("steps = %+v, want one step with a retained log", runs[0].Steps)
	}
	retained, err := st.ReadRunLog(runs[0].Steps[0].Log)
	if err != nil {
		t.Fatalf("ReadRunLog: %v", err)
	}
	return maskedRun{live: out, retained: retained, run: runs[0], socket: socket}
}

// assertMasked proves a value is gone from a surface and a redaction shows.
func assertMasked(t *testing.T, surface, got, secret string) {
	t.Helper()
	if strings.Contains(got, secret) {
		t.Fatalf("%s leaked the secret: %q", surface, got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("%s = %q, want a redaction", surface, got)
	}
}

// A secret declared from a host environment variable is masked on every
// surface — the live stream, the retained log on disk, and the log the CLI
// serves — even when the value arrives split across output chunks.
func TestRunMasksADeclaredEnvSecretEverywhere(t *testing.T) {
	const secret = "host-secret-value"
	t.Setenv("REGISTRY_TOKEN", secret)

	res := runMaskedTask(t,
		"version: 1\nsecrets:\n  REGISTRY_TOKEN: { env: REGISTRY_TOKEN }\ntasks:\n  dev: { run: deploy }\n",
		"push token=host-se", "cret-value done\n")

	assertMasked(t, "live stream", res.live, secret)
	assertMasked(t, "retained log", res.retained, secret)

	code, out, errOut := runCLI(t, "--socket", res.socket, "logs", res.run.ID)
	if code != 0 {
		t.Fatalf("logs exit = %d, stderr: %s", code, errOut)
	}
	assertMasked(t, "logs output", out, secret)
}

// A secret declared from a file is masked too, and a trailing newline in the
// file does not keep the value from being caught.
func TestRunMasksADeclaredFileSecretEverywhere(t *testing.T) {
	const secret = "file-token-9876"
	repo := gitRepo(t)
	writeWorkspace(t, repo, map[string]string{"token.txt": secret + "\n"})
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  TOKEN: { file: token.txt }\ntasks:\n  dev: { run: deploy }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = sink.Write([]byte("using " + secret + "\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}
	retained, err := st.ReadRunLog(runs[0].Steps[0].Log)
	if err != nil {
		t.Fatalf("ReadRunLog: %v", err)
	}
	assertMasked(t, "retained log", retained, secret)
}

// An explicit mask literal is redacted without a matching declared secret.
func TestRunMasksAnExplicitMaskLiteral(t *testing.T) {
	res := runMaskedTask(t,
		"version: 1\nmask: [\"ghp_\"]\ntasks:\n  dev: { run: echo }\n",
		"token ghp_abc123\n")

	for _, surface := range []struct{ name, got string }{
		{"live stream", res.live},
		{"retained log", res.retained},
	} {
		if strings.Contains(surface.got, "ghp_") {
			t.Fatalf("%s leaked the mask literal: %q", surface.name, surface.got)
		}
		if !strings.Contains(surface.got, "[REDACTED]") {
			t.Fatalf("%s = %q, want a redaction", surface.name, surface.got)
		}
	}
}

// A run that declares no secret streams its output unchanged.
func TestRunLeavesOutputUnmaskedWithoutDeclaredSecrets(t *testing.T) {
	res := runMaskedTask(t,
		"version: 1\ntasks:\n  dev: { run: echo }\n",
		"nothing secret here\n")
	if res.live != "nothing secret here\n" {
		t.Fatalf("live stream = %q, want the output unchanged", res.live)
	}
	if res.retained != "nothing secret here\n" {
		t.Fatalf("retained log = %q, want the output unchanged", res.retained)
	}
}
