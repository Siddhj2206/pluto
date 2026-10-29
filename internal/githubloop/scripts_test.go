package githubloop_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/githubloop"
)

// The scripts are repo-owned shell, so the tests drive the shipped files with
// fake gh, git, and agent executables on PATH. Assertions are on what the
// scripts ask those tools to do and on what they print, never on their
// internals.

const fakeGH = `#!/usr/bin/env bash
set -euo pipefail
state=${FAKE_STATE:?}
printf 'gh %s\n' "$*" >> "$FAKE_GH_LOG"
cmd=$1
shift || true
case "$cmd" in
  pr)
    sub=$1
    shift || true
    case "$sub" in
      list)
        if [ -s "$state/pr" ]; then cat "$state/pr"; fi
        ;;
      create)
        body=""
        while [ $# -gt 0 ]; do
          case "$1" in
            --body) body=$2; shift 2 ;;
            *) shift ;;
          esac
        done
        printf '%s' "$body" > "$state/pr_body"
        printf 'https://github.com/example/project/pull/42\n'
        ;;
      edit)
        printf '%s\n' "$*" >> "$state/pr_edit"
        ;;
    esac
    ;;
  label)
    ;;
  api)
    method=GET
    bodyfile=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --paginate) shift ;;
        --jq) shift 2 ;;
        -X) method=$2; shift 2 ;;
        -F)
          case "$2" in
            body=@*) bodyfile=${2#body=@} ;;
          esac
          shift 2
          ;;
        *) shift ;;
      esac
    done
    if [ "$method" = "GET" ]; then
      if [ -s "$state/comment_ids" ]; then cat "$state/comment_ids"; fi
    else
      cp "$bodyfile" "$state/comment_body"
      printf '%s\n' "$method" >> "$state/comment_method"
      printf '{"id":99}\n'
    fi
    ;;
esac
exit 0
`

const fakeGit = `#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >> "$FAKE_GIT_LOG"
case "$1" in
  rev-parse)
    case "$*" in
      *--abbrev-ref*) printf '%s\n' "${FAKE_GIT_BRANCH:-pluto/issue-23}" ;;
      *) printf '%s\n' "${FAKE_GIT_HEAD:-0123456789abcdef0123456789abcdef01234567}" ;;
    esac
    ;;
  diff)
    if [ -f "$FAKE_STATE/changes" ]; then exit 1; fi
    exit 0
    ;;
esac
exit 0
`

const fakeAgent = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${PLUTO_ISSUE_PROMPT:-}" > "$FAKE_STATE/agent_prompt"
if [ "${FAKE_AGENT_CHANGES:-1}" = "1" ]; then : > "$FAKE_STATE/changes"; fi
exit 0
`

const fakeOpencode = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" > "$FAKE_STATE/opencode_args"
if [ "${FAKE_OPENCODE_CHANGES:-1}" = "1" ]; then : > "$FAKE_STATE/changes"; fi
exit 0
`

type harness struct {
	t     *testing.T
	root  string
	work  string
	state string
	env   []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to drive the job scripts")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state")
	work := filepath.Join(root, "work")
	for _, dir := range []string{bin, state, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(bin, "gh"), fakeGH)
	writeExecutable(t, filepath.Join(bin, "git"), fakeGit)
	writeExecutable(t, filepath.Join(bin, "agent"), fakeAgent)
	writeExecutable(t, filepath.Join(bin, "opencode"), fakeOpencode)
	h := &harness{t: t, root: root, work: work, state: state}
	h.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_STATE="+state,
		"FAKE_GH_LOG="+filepath.Join(root, "gh.log"),
		"FAKE_GIT_LOG="+filepath.Join(root, "git.log"),
	)
	return h
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// run executes a shipped script in the fake worktree with the given extra
// environment and returns its exit code and combined output.
func (h *harness) run(script string, env ...string) (int, string) {
	h.t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", script))
	if err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	cmd.Dir = h.work
	cmd.Env = append(append([]string{}, h.env...), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		h.t.Fatalf("run %s: %v", script, err)
	}
	return code, out.String()
}

func (h *harness) readLog(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.root, name))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *harness) writeState(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.state, name), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) readState(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.state, name))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *harness) issueEnv(overrides ...string) []string {
	env := []string{
		"PLUTO_EVENT_KIND=issue",
		"PLUTO_EVENT_ACTION=labeled",
		"PLUTO_EVENT_OBJECT_ID=23",
		"PLUTO_EVENT_URL=https://github.com/example/project/issues/23",
		"PLUTO_EVENT_REPO=https://github.com/example/project",
		"PLUTO_EVENT_REF=0123456789abcdef0123456789abcdef01234567",
		"PLUTO_EVENT_HEAD_REF=refs/heads/main",
		"PLUTO_EVENT_LABELS=pluto:fix",
		"PLUTO_EVENT_TRUSTED=true",
		`PLUTO_EVENT_PAYLOAD={"action":"labeled","issue":{"number":23}}`,
		"PLUTO_TASK_ID=task_abc123",
		"PLUTO_RUN_ID=run_abc123",
		"PLUTO_TRACE_ID=trc_abc123",
		"PLUTO_ISSUE_TOKEN=issue-secret-value",
		"PLUTO_ISSUE_AGENT=agent",
	}
	return append(env, overrides...)
}

// issueEnvWithoutAgent clears PLUTO_ISSUE_AGENT so the shipped script takes
// its default free-model OpenCode command. An empty value is appended last so
// an ambient override from the developer's environment cannot replace the
// default under test.
func (h *harness) issueEnvWithoutAgent(overrides ...string) []string {
	env := h.issueEnv(overrides...)
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "PLUTO_ISSUE_AGENT=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "PLUTO_ISSUE_AGENT=")
}

func (h *harness) prEnv(overrides ...string) []string {
	env := []string{
		"PLUTO_EVENT_KIND=pull_request",
		"PLUTO_EVENT_ACTION=synchronize",
		"PLUTO_EVENT_OBJECT_ID=7",
		"PLUTO_EVENT_URL=https://github.com/example/project/pull/7",
		"PLUTO_EVENT_REPO=https://github.com/example/project",
		"PLUTO_EVENT_REF=0123456789abcdef0123456789abcdef01234567",
		"PLUTO_EVENT_HEAD_REF=feature",
		"PLUTO_EVENT_TRUSTED=true",
		"PLUTO_TASK_ID=task_pr123",
		"PLUTO_RUN_ID=run_pr123",
		"PLUTO_TRACE_ID=trc_pr123",
		"PLUTO_PR_TOKEN=pr-secret-value",
		"PLUTO_PR_TEST_COMMAND=printf 'ok\\n'",
	}
	return append(env, overrides...)
}

func TestIssueFixScriptOpensOneDraftPR(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-issue-fix", h.issueEnv()...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	gitLog := h.readLog("git.log")
	if !strings.Contains(gitLog, "push --set-upstream origin pluto/issue-23") {
		t.Fatalf("issue branch was not pushed: %s", gitLog)
	}
	if !strings.Contains(gitLog, "commit") {
		t.Fatalf("agent work was not committed: %s", gitLog)
	}
	ghLog := h.readLog("gh.log")
	if !strings.Contains(ghLog, "pr create --draft") || !strings.Contains(ghLog, "--head pluto/issue-23") || !strings.Contains(ghLog, "--base main") {
		t.Fatalf("draft PR was not opened against the deterministic branch: %s", ghLog)
	}
	if !strings.Contains(ghLog, "pr edit 42 --add-label "+githubloop.TrustedLabel) {
		t.Fatalf("the job did not label its own PR trusted: %s", ghLog)
	}
	body := h.readState("pr_body")
	if !strings.Contains(body, "Closes #23") {
		t.Fatalf("PR body does not link the issue: %q", body)
	}
	if !strings.Contains(h.readState("agent_prompt"), "#23") {
		t.Fatalf("agent prompt does not name the issue: %q", h.readState("agent_prompt"))
	}
	for _, secret := range []string{"issue-secret-value"} {
		if strings.Contains(out, secret) || strings.Contains(gitLog, secret) || strings.Contains(ghLog, secret) {
			t.Fatal("the issue token value was printed or passed on a command line")
		}
	}
}

// withoutEnv removes named variables so a test can exercise a fallback name
// without a duplicate entry from the harness environment.
func withoutEnv(env []string, names ...string) []string {
	skip := make(map[string]bool, len(names))
	for _, name := range names {
		skip[name] = true
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if skip[name] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func TestIssueFixScriptAcceptsTheContractCredentialName(t *testing.T) {
	h := newHarness(t)
	env := withoutEnv(h.issueEnvWithoutAgent(), "PLUTO_ISSUE_TOKEN")
	env = append(env, "HOME="+h.root, "ISSUE_TOKEN=issue-secret-value")
	code, out := h.run("pluto-issue-fix", env...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if !strings.Contains(h.readLog("git.log"), "push --set-upstream origin pluto/issue-23") {
		t.Fatal("the ISSUE_TOKEN fallback did not authorize the push")
	}
	if strings.Contains(out, "issue-secret-value") {
		t.Fatal("the fallback token value was printed")
	}
}

func TestPRTestScriptAcceptsTheContractCredentialName(t *testing.T) {
	h := newHarness(t)
	env := withoutEnv(h.prEnv(), "PLUTO_PR_TOKEN")
	env = append(env, "PR_TOKEN=pr-secret-value")
	code, out := h.run("pluto-pr-test", env...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if got := strings.TrimSpace(h.readState("comment_method")); got != "POST" {
		t.Fatalf("comment method = %q, want one POST from the PR_TOKEN fallback", got)
	}
	if strings.Contains(out, "pr-secret-value") {
		t.Fatal("the fallback token value was printed")
	}
}

func TestIssueFixScriptDefaultsToTheFreeOpenCodeModel(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-issue-fix", append(h.issueEnvWithoutAgent(), "HOME="+h.root)...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	args := h.readState("opencode_args")
	for _, want := range []string{
		"run",
		"--standalone",
		"--auto",
		"--model opencode/deepseek-v4.1-flash-free",
		"Fix GitHub issue #23",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("default agent invocation %q is missing %q", args, want)
		}
	}
}

func TestIssueFixScriptModelOverride(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-issue-fix",
		append(h.issueEnvWithoutAgent(), "HOME="+h.root, "PLUTO_ISSUE_MODEL=opencode/glm-5.3-flash-free")...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	args := h.readState("opencode_args")
	if !strings.Contains(args, "--model opencode/glm-5.3-flash-free") {
		t.Fatalf("agent invocation %q does not use the overridden model", args)
	}
	if strings.Contains(args, "deepseek-v4.1-flash-free") {
		t.Fatalf("agent invocation %q still names the default model", args)
	}
}

func TestIssueFixScriptIgnoresAnIssueWithoutTheLabel(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-issue-fix", h.issueEnv("PLUTO_EVENT_LABELS=bug")...)
	if code != 0 {
		t.Fatalf("an ignored issue must not fail the job: exit = %d: %s", code, out)
	}
	if !strings.Contains(out, githubloop.IssueLabel) {
		t.Fatalf("output does not name the missing label: %q", out)
	}
	if ghLog := h.readLog("gh.log"); ghLog != "" {
		t.Fatalf("ignored issue called GitHub: %s", ghLog)
	}
	if gitLog := h.readLog("git.log"); strings.Contains(gitLog, "push") {
		t.Fatalf("ignored issue pushed a branch: %s", gitLog)
	}
}

func TestIssueFixScriptReusesAnExistingPR(t *testing.T) {
	h := newHarness(t)
	h.writeState("pr", "42\tfalse\thttps://github.com/example/project/pull/42\n")
	code, out := h.run("pluto-issue-fix", h.issueEnv()...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	ghLog := h.readLog("gh.log")
	if strings.Contains(ghLog, "pr create") {
		t.Fatalf("a duplicate delivery opened a second PR: %s", ghLog)
	}
	if !strings.Contains(ghLog, "pr edit 42 --add-label "+githubloop.TrustedLabel) {
		t.Fatalf("existing PR was not labelled trusted: %s", ghLog)
	}
	if !strings.Contains(ghLog, "reusing") && !strings.Contains(out, "reusing") {
		t.Fatalf("output does not say the PR was reused: %q", out)
	}
}

func TestIssueFixScriptDoesNothingWhenTheAgentChangesNothing(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-issue-fix", append(h.issueEnv(), "FAKE_AGENT_CHANGES=0")...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	gitLog := h.readLog("git.log")
	if strings.Contains(gitLog, "push") || strings.Contains(gitLog, "commit") {
		t.Fatalf("empty work was committed or pushed: %s", gitLog)
	}
	if ghLog := h.readLog("gh.log"); strings.Contains(ghLog, "pr create") {
		t.Fatalf("empty work opened a PR: %s", ghLog)
	}
	if !strings.Contains(out, "no changes") {
		t.Fatalf("output does not explain the empty run: %q", out)
	}
}

func TestPRTestScriptCreatesOneMarkedComment(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-pr-test", h.prEnv()...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if got := strings.TrimSpace(h.readState("comment_method")); got != "POST" {
		t.Fatalf("comment method = %q, want one POST", got)
	}
	body := h.readState("comment_body")
	for _, want := range []string{
		githubloop.TestReportMarker,
		"PASSED",
		"printf 'ok",
		"Duration:",
		"task_pr123",
		"run_pr123",
		"trc_pr123",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment body is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "pr-secret-value") {
		t.Fatal("comment body leaked the PR token")
	}
}

func TestPRTestScriptCommentsOnFailureAndNeverPushes(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-pr-test", h.prEnv("PLUTO_PR_TEST_COMMAND=printf 'boom\\n'; exit 3")...)
	if code == 0 {
		t.Fatalf("a failing test must fail the job: %s", out)
	}
	body := h.readState("comment_body")
	if !strings.Contains(body, githubloop.TestReportMarker) || !strings.Contains(body, "FAILED") {
		t.Fatalf("failure comment was not posted or does not say FAILED:\n%s", body)
	}
	if strings.Contains(h.readLog("git.log"), "push") {
		t.Fatal("the PR test job pushed on failure")
	}
}

func TestPRTestScriptUpdatesTheMarkedCommentInsteadOfAddingOne(t *testing.T) {
	h := newHarness(t)
	h.writeState("comment_ids", "99\n")
	code, out := h.run("pluto-pr-test", h.prEnv()...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if got := strings.TrimSpace(h.readState("comment_method")); got != "PATCH" {
		t.Fatalf("comment method = %q, want one PATCH of the existing comment", got)
	}
	ghLog := h.readLog("gh.log")
	if strings.Contains(ghLog, "-X POST") {
		t.Fatalf("a repeated run posted a second comment: %s", ghLog)
	}
	if !strings.Contains(ghLog, "repos/example/project/issues/comments/99") {
		t.Fatalf("the marked comment was not updated by id: %s", ghLog)
	}
}

func TestPRTestScriptRunsTestsButNeverCommentsWithoutTrust(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-pr-test", h.prEnv("PLUTO_EVENT_TRUSTED=false", "PLUTO_PR_TOKEN=")...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if ghLog := h.readLog("gh.log"); ghLog != "" {
		t.Fatalf("an untrusted PR reached the GitHub API: %s", ghLog)
	}
	if !strings.Contains(out, "untrusted") {
		t.Fatalf("output does not explain the skipped comment: %q", out)
	}
}

func TestPRTestScriptKeepsLongOutputInPluto(t *testing.T) {
	h := newHarness(t)
	code, out := h.run("pluto-pr-test", h.prEnv(
		`PLUTO_PR_TEST_COMMAND=i=1; while [ $i -le 60 ]; do printf 'line %03d\n' $i; i=$((i+1)); done`,
		"PLUTO_PR_TEST_TAIL=5",
	)...)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	body := h.readState("comment_body")
	if strings.Contains(body, "line 001") {
		t.Fatalf("comment quoted the whole log instead of a tail:\n%s", body)
	}
	if !strings.Contains(body, "line 060") {
		t.Fatalf("comment does not quote the tail:\n%s", body)
	}
	if !strings.Contains(out, "line 001") {
		t.Fatal("the full test output did not stay in the run log")
	}
	if !strings.Contains(body, "run_pr123") {
		t.Fatal("comment does not link the run that holds the full output")
	}
}
