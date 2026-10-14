package cli_test

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// A declared secret is injected into the task's exec environment under its
// declared name, so a task can read it with $NAME.
func TestRunInjectsDeclaredSecretsIntoTheTaskEnv(t *testing.T) {
	const secret = "host-secret-value"
	t.Setenv("REGISTRY_TOKEN", secret)
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  REGISTRY_TOKEN: { env: REGISTRY_TOKEN }\ntasks:\n  dev: { run: \"deploy\" }\n")
	engine := container.NewFake()
	var got container.ExecSpec
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		got = spec
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	if got.Env["REGISTRY_TOKEN"] != secret {
		t.Fatalf("exec env = %v, want REGISTRY_TOKEN injected", got.Env)
	}
}

// A secret declared from a file is injected too, with the trailing newline
// trimmed as it is for masking.
func TestRunInjectsADeclaredFileSecretIntoTheTaskEnv(t *testing.T) {
	const secret = "file-secret-value"
	repo := gitRepo(t)
	writeWorkspace(t, repo, map[string]string{"token.txt": secret + "\n"})
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  TOKEN: { file: token.txt }\ntasks:\n  dev: { run: \"deploy\" }\n")
	engine := container.NewFake()
	var got container.ExecSpec
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		got = spec
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	if got.Env["TOKEN"] != secret {
		t.Fatalf("exec env = %v, want TOKEN injected from the file", got.Env)
	}
}

// A CI run injects declared secrets into the ephemeral box's task environment.
func TestCIInjectsDeclaredSecretsIntoTheTaskEnv(t *testing.T) {
	const secret = "ci-secret-value"
	t.Setenv("CI_TOKEN", secret)
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  CI_TOKEN: { env: CI_TOKEN }\ntasks:\n  test: { run: \"deploy\", lock: exclusive }\n")
	engine := container.NewFake()
	var got container.ExecSpec
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		got = spec
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "ci", "test"); code != 0 {
		t.Fatalf("ci test exit = %d, stderr: %s", code, errOut)
	}
	if got.Env["CI_TOKEN"] != secret {
		t.Fatalf("exec env = %v, want CI_TOKEN injected", got.Env)
	}
}

// A scheduled run injects declared secrets into the task environment.
func TestScheduledRunInjectsDeclaredSecretsIntoTheTaskEnv(t *testing.T) {
	const secret = "sched-secret-value"
	t.Setenv("SCHED_TOKEN", secret)
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  SCHED_TOKEN: { env: SCHED_TOKEN }\ntasks:\n  build: { run: \"deploy\" }\nschedules:\n  nightly: { task: build, cron: \"* * * * *\" }\n")
	engine := container.NewFake()
	var mu sync.Mutex
	var got container.ExecSpec
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		mu.Lock()
		got = spec
		mu.Unlock()
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	box := boxRecord(t, st, repo)
	armed := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "build", armed)
	runSchedulerLoop(t, srv, &testClock{now: armed.Add(70 * time.Second)})

	waitForCondition(t, "the scheduled run recorded", func() bool {
		runs, err := st.Runs()
		return err == nil && len(runs) == 1 && runs[0].State == state.RunDone
	})
	mu.Lock()
	defer mu.Unlock()
	if got.Env["SCHED_TOKEN"] != secret {
		t.Fatalf("exec env = %v, want SCHED_TOKEN injected", got.Env)
	}
}
