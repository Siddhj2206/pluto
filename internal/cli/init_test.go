package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

func TestInitCommandNeedsNoDaemonOrGitRepo(t *testing.T) {
	dir := t.TempDir()
	stateHome := filepath.Join(t.TempDir(), "state-home")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Chdir(dir)
	var out, stderr bytes.Buffer
	if code := Run([]string{"init"}, &out, &stderr); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pluto.yml")); err != nil {
		t.Fatalf("starter contract missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "pluto")); err == nil {
		t.Fatal("init unexpectedly created state directory")
	}
}

// The scaffolded contract must load and validate under the pluto.yml schema.
func TestInitScaffoldsAWorkingContract(t *testing.T) {
	dir := t.TempDir()
	if err := initRepo(dir); err != nil {
		t.Fatal(err)
	}
	p, err := contract.LoadProject(dir)
	if err != nil {
		t.Fatalf("scaffolded contract does not load: %v", err)
	}
	if p.Version != 1 || len(p.Tasks) == 0 {
		t.Fatalf("scaffolded project = %+v, want version 1 with a task", p)
	}
}

func TestInitCreatesContractAndPreservesExistingContract(t *testing.T) {
	dir := t.TempDir()
	if err := initRepo(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pluto.yml")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "version: 1") {
		t.Fatalf("starter contract = %q", first)
	}
	if err := os.WriteFile(path, []byte("version: 1\ntasks: { dev: { run: a } }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initRepo(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "dev") {
		t.Fatalf("existing contract changed: %q", got)
	}
}
