package demo_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/demo"
	"github.com/Siddhj2206/pluto/internal/githubloop"
)

// The harness is a sample contract and four repo-owned shell scripts, so the
// tests read the shipped files and drive the scripts with fake pluto and gh
// executables. They assert the contract a presenter will copy and what the
// scripts print, never their internals.

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to drive the demo scripts")
	}
}

func scriptPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), name)
}

// TestSampleContract proves the sample parses and carries the shipped job,
// schedule, event, and credential names. A drift here would make the runbook
// tell a presenter to copy a contract the daemon cannot apply.
func TestSampleContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), demo.ContractPath))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := contract.Parse(string(data))
	if err != nil {
		t.Fatalf("parse %s: %v", demo.ContractPath, err)
	}

	if ct.Box.AutoPause != "5m" {
		t.Fatalf("auto_pause = %q, want 5m", ct.Box.AutoPause)
	}
	if ct.Tools == nil || len(ct.Tools.Packages) != 2 ||
		ct.Tools.Packages[0].Name != "curl" || ct.Tools.Packages[1].Name != "gh" {
		t.Fatalf("tools = %+v, want the curl and gh packages", ct.Tools)
	}
	if ct.Provision == nil || ct.Provision.Command.String() != demo.ProvisionScript {
		t.Fatalf("provision = %+v, want %s", ct.Provision, demo.ProvisionScript)
	}

	for _, name := range []string{demo.HeartbeatJob, demo.IssueFixJob, demo.PRTestJob} {
		if _, ok := ct.Jobs[name]; !ok {
			t.Fatalf("missing [jobs.%s]", name)
		}
	}
	if got := ct.Jobs[demo.HeartbeatJob].Command.String(); got != demo.HeartbeatScript {
		t.Fatalf("heartbeat command = %q, want %q", got, demo.HeartbeatScript)
	}
	if got := ct.Jobs[demo.IssueFixJob].Command.String(); got != githubloop.IssueFixScript {
		t.Fatalf("issue-fix command = %q, want the real free-model path %q", got, githubloop.IssueFixScript)
	}
	if got := ct.Jobs[demo.PRTestJob].Command.String(); !strings.Contains(got, githubloop.PRTestScript) {
		t.Fatalf("pr-test command = %q, want it to run %s", got, githubloop.PRTestScript)
	}

	if len(ct.Schedules) != 1 {
		t.Fatalf("schedules = %+v, want one", ct.Schedules)
	}
	sched := ct.Schedules[0]
	if sched.Name != demo.ScheduleName || sched.Cron != "*/2 * * * *" || sched.Job != demo.HeartbeatJob {
		t.Fatalf("schedule = %+v, want heartbeat every two minutes", sched)
	}

	issue := ct.Events.Issue
	if issue == nil {
		t.Fatal("missing [events.issue]")
	}
	if issue.Job != demo.IssueFixJob {
		t.Fatalf("events.issue.job = %q, want %q", issue.Job, demo.IssueFixJob)
	}
	if strings.Join(issue.Actions, ",") != "labeled" {
		t.Fatalf("events.issue.actions = %v, want [labeled]", issue.Actions)
	}
	if strings.Join(issue.Labels, ",") != githubloop.IssueLabel {
		t.Fatalf("events.issue.labels = %v, want [%s]", issue.Labels, githubloop.IssueLabel)
	}
	if strings.Join(issue.CredentialNames, ",") != "PLUTO_ISSUE_TOKEN" {
		t.Fatalf("events.issue.credentials = %v, want [PLUTO_ISSUE_TOKEN]", issue.CredentialNames)
	}

	pr := ct.Events.PullRequest
	if pr == nil {
		t.Fatal("missing [events.pull_request]")
	}
	if pr.Job != demo.PRTestJob {
		t.Fatalf("events.pull_request.job = %q, want %q", pr.Job, demo.PRTestJob)
	}
	if pr.TrustedLabel != githubloop.TrustedLabel {
		t.Fatalf("events.pull_request.trusted_label = %q, want %q", pr.TrustedLabel, githubloop.TrustedLabel)
	}
	if strings.Join(pr.CredentialNames, ",") != "PLUTO_PR_TOKEN" {
		t.Fatalf("events.pull_request.credentials = %v, want [PLUTO_PR_TOKEN]", pr.CredentialNames)
	}
	for _, action := range []string{"opened", "labeled", "synchronize", "reopened", "ready_for_review"} {
		if !pr.Allows(action) {
			t.Fatalf("events.pull_request does not allow %q", action)
		}
	}
}

func TestHeartbeatScript(t *testing.T) {
	requireBash(t)
	script := scriptPath(t, demo.HeartbeatScript)

	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{
			name: "schedule occurrence",
			env: map[string]string{
				"PLUTO_TASK_ID":  "task_1",
				"PLUTO_RUN_ID":   "run_1",
				"PLUTO_TRACE_ID": "trc_1",
			},
			want: "trigger=task task=task_1 run=run_1 trace=trc_1",
		},
		{name: "manual run", want: "trigger=manual"},
		{
			name: "event job",
			env:  map[string]string{"PLUTO_EVENT_KIND": "issues", "PLUTO_EVENT_ACTION": "labeled"},
			want: "trigger=event:issues/labeled",
		},
		{name: "explicit label", args: []string{"rehearsal"}, want: "trigger=rehearsal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			code, out := runScript(t, dir, script, envWith(os.Environ(), tc.env), tc.args...)
			if code != 0 {
				t.Fatalf("exit = %d, output:\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output = %q, want %q", out, tc.want)
			}
			if !strings.Contains(out, "pwd="+dir) {
				t.Fatalf("output = %q, want pwd=%s", out, dir)
			}
			log, err := os.ReadFile(filepath.Join(dir, "demo-heartbeat.log"))
			if err != nil {
				t.Fatalf("read heartbeat log: %v", err)
			}
			if !strings.Contains(string(log), tc.want) {
				t.Fatalf("log = %q, want %q", log, tc.want)
			}
		})
	}
}

func TestDemoAgentAndTest(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	agent := scriptPath(t, demo.AgentScript)
	test := scriptPath(t, demo.TestScript)

	code, out := runScript(t, dir, agent, envWith(os.Environ(), map[string]string{
		"PLUTO_ISSUE_PROMPT": "Fix issue #7 in this repository.",
	}))
	if code != 0 {
		t.Fatalf("agent exit = %d, output:\n%s", code, out)
	}
	note, err := os.ReadFile(filepath.Join(dir, "demo-issue-note.md"))
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(note), "Fix issue #7") {
		t.Fatalf("note = %q, want the prompt", note)
	}

	code, out = runScript(t, dir, test, os.Environ())
	if code != 0 || !strings.Contains(out, "present") {
		t.Fatalf("test exit = %d, output:\n%s", code, out)
	}

	if err := os.Remove(filepath.Join(dir, "demo-issue-note.md")); err != nil {
		t.Fatal(err)
	}
	code, out = runScript(t, dir, test, os.Environ())
	if code == 0 || !strings.Contains(out, "is missing") {
		t.Fatalf("test with no note: exit = %d, output:\n%s", code, out)
	}
}

const fakePluto = `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  health)
    if [ "${FAKE_HEALTH:-1}" != "1" ]; then
      printf 'dial unix: no such file or directory\n' >&2
      exit 1
    fi
    printf 'status: ok\n'
    ;;
  image)
    printf 'VERSION\tSNAPSHOT\tKERNEL\tROOTFS\n'
    if [ "${FAKE_IMAGE:-1}" = "1" ]; then
      printf 'v1\t2026-01-01\taaaa\tbbbb\n'
    fi
    ;;
  job)
    printf 'heartbeat\trecord date, host, pwd, and trigger\n'
    printf 'issue-fix\tagent work for a labelled issue\n'
    printf 'pr-test\trun the demo test\n'
    ;;
  *)
    printf 'unexpected pluto args: %s\n' "$*" >&2
    exit 2
    ;;
esac
`

const fakeGH = `#!/usr/bin/env bash
set -euo pipefail
if [ "$1 $2" = "label list" ]; then
  printf '%s\n' ${FAKE_LABELS:-pluto:fix pluto:trusted}
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 2
`

func TestPreflight(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	contractDir := filepath.Join(root, "contract")
	for _, dir := range []string{bin, filepath.Join(contractDir, "scripts")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(bin, "pluto"), fakePluto)
	writeExecutable(t, filepath.Join(bin, "gh"), fakeGH)
	if err := os.WriteFile(filepath.Join(contractDir, ".pluto.toml"), []byte("[jobs.heartbeat]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"pluto-demo-heartbeat", "pluto-demo-agent", "pluto-demo-test",
		"pluto-demo-provision", "pluto-issue-fix", "pluto-pr-test",
	} {
		writeExecutable(t, filepath.Join(contractDir, "scripts", name), "#!/bin/sh\nexit 0\n")
	}

	base := envWith(os.Environ(), map[string]string{
		"PATH":                        bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PLUTO_ISSUE_TOKEN":           "issue-value",
		"PLUTO_PR_TOKEN":              "pr-value",
		"PLUTO_GITHUB_WEBHOOK_SECRET": "secret-value",
	})
	script := scriptPath(t, demo.PreflightScript)

	t.Run("passes on a prepared host", func(t *testing.T) {
		code, out := runScript(t, root, script, base, "--contract", contractDir, "--github", "owner/cipher")
		if code != 0 {
			t.Fatalf("exit = %d, output:\n%s", code, out)
		}
		for _, want := range []string{
			"daemon reachable",
			"base image imported",
			"contract declares job heartbeat",
			"script pluto-demo-heartbeat is executable",
			"label pluto:fix exists on owner/cipher",
			"label pluto:trusted exists on owner/cipher",
			"PLUTO_ISSUE_TOKEN is set in this shell",
			"preflight passed",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("names an unreachable daemon", func(t *testing.T) {
		code, out := runScript(t, root, script, envWith(base, map[string]string{"FAKE_HEALTH": "0"}), "--contract", contractDir)
		if code != 1 {
			t.Fatalf("exit = %d, want 1; output:\n%s", code, out)
		}
		if !strings.Contains(out, "FAIL: daemon not reachable") {
			t.Fatalf("output = %q, want the daemon failure", out)
		}
	})

	t.Run("names a missing label", func(t *testing.T) {
		code, out := runScript(t, root, script, envWith(base, map[string]string{"FAKE_LABELS": "pluto:fix"}), "--contract", contractDir, "--github", "owner/cipher")
		if code != 1 {
			t.Fatalf("exit = %d, want 1; output:\n%s", code, out)
		}
		if !strings.Contains(out, "label pluto:trusted is missing on owner/cipher") {
			t.Fatalf("output = %q, want the missing label", out)
		}
	})

	t.Run("names a missing secret", func(t *testing.T) {
		code, out := runScript(t, root, script, envWithout(base, "PLUTO_PR_TOKEN"), "--contract", contractDir)
		if code != 1 {
			t.Fatalf("exit = %d, want 1; output:\n%s", code, out)
		}
		if !strings.Contains(out, "PLUTO_PR_TOKEN is not set") {
			t.Fatalf("output = %q, want the missing secret", out)
		}
	})
}

func runScript(t *testing.T, dir, script string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", script, err)
	}
	return code, out.String()
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// envWith returns base with over applied, replacing existing keys so a
// subprocess cannot see two values for one name.
func envWith(base []string, over map[string]string) []string {
	out := make([]string, 0, len(base)+len(over))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := over[name]; replaced {
			continue
		}
		out = append(out, entry)
	}
	for name, value := range over {
		out = append(out, name+"="+value)
	}
	return out
}

func envWithout(base []string, name string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		if key, _, _ := strings.Cut(entry, "="); key == name {
			continue
		}
		out = append(out, entry)
	}
	return out
}
