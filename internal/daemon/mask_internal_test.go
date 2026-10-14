package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// projectMasker resolves declared secrets from the host and registers them,
// alongside the explicit mask literals, with one masker.
func TestProjectMaskerResolvesEnvFilesAndLiterals(t *testing.T) {
	t.Setenv("HOST_TOKEN", "env-secret-value")
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "token.txt"), []byte("file-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := contract.ParseProject("version: 1\nmask: [\"ghp_\"]\n" +
		"secrets:\n" +
		"  FROM_ENV: { env: HOST_TOKEN }\n" +
		"  FROM_FILE: { file: token.txt }\n" +
		"  UNSET: { env: NOT_SET_ANYWHERE }\n")
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}

	m, err := projectMasker(p, workspace)
	if err != nil {
		t.Fatalf("projectMasker: %v", err)
	}
	if m.Empty() {
		t.Fatal("masker is empty, want the declared values registered")
	}
	// A trailing newline in a secret file is not part of the value.
	got := string(append(m.Mask([]byte("env-secret-value file-secret-value ghp_x")), m.Flush()...))
	want := "[REDACTED] [REDACTED] [REDACTED]x"
	if got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
}

// A declared secret file that cannot be read fails the masker, so a run fails
// closed rather than stream a value it cannot mask.
func TestProjectMaskerFailsWhenASecretFileIsUnreadable(t *testing.T) {
	p, err := contract.ParseProject("version: 1\nsecrets:\n  MISSING: { file: nope.txt }\n")
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if _, err := projectMasker(p, t.TempDir()); err == nil {
		t.Fatal("projectMasker = nil error, want a failure for the unreadable secret file")
	}
}

// A project that declares nothing masks nothing.
func TestProjectMaskerWithNoDeclarations(t *testing.T) {
	p := &contract.Project{}
	m, err := projectMasker(p, t.TempDir())
	if err != nil {
		t.Fatalf("projectMasker: %v", err)
	}
	if !m.Empty() {
		t.Fatal("masker is not empty for a project with no declarations")
	}
}
