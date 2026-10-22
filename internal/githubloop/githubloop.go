// Package githubloop names the contract between the daemon's GitHub webhook
// admission, the repo-owned job scripts under scripts/, and the tests that
// drive those scripts with fake gh/git/agent executables.
//
// The scripts are repo-owned on purpose: they are the part of the loop a
// project can change (agent command, test command, PR body) without changing
// Pluto. This package only fixes the names both sides must agree on.
package githubloop

import "strings"

// Scripts are the default [jobs.*] commands for the review-3 loop.
const (
	// IssueFixScript runs on an issue work-item box: it reads the issue
	// payload, runs the agent, commits on IssueBranch, pushes, and opens or
	// reuses one draft PR.
	IssueFixScript = "scripts/pluto-issue-fix"
	// PRTestScript runs on a pull-request work-item box: it runs the declared
	// test command and creates or updates one marked comment.
	PRTestScript = "scripts/pluto-pr-test"
)

// Labels are the human opt-ins the loop uses.
const (
	// IssueLabel is the label an issue must carry before an issue job runs.
	IssueLabel = "pluto:fix"
	// TrustedLabel marks a same-repository PR as safe to hand host
	// credentials to. An unlabelled or fork PR gets none.
	TrustedLabel = "pluto:trusted"
)

// TestReportMarker is the hidden identifier in every PR test comment. The job
// searches for it and updates that comment instead of posting another.
const TestReportMarker = "<!-- pluto:test-report -->"

// Environment the job scripts read. Values are supplied by the daemon's
// queue runner; the token values never appear in state, the ledger, or output.
const (
	// EventPayloadEnv is the signed webhook body. The ledger stores only its
	// size and hash.
	EventPayloadEnv = "PLUTO_EVENT_PAYLOAD"
	// EventLabelsEnv is the comma-separated label list GitHub reported.
	EventLabelsEnv = "PLUTO_EVENT_LABELS"
	// EventTrustedEnv is "true" only for a PR carrying TrustedLabel.
	EventTrustedEnv = "PLUTO_EVENT_TRUSTED"
	// TraceIDEnv joins the comment back to webhook intake in the ledger.
	TraceIDEnv = "PLUTO_TRACE_ID"
	// TaskIDEnv and RunIDEnv identify the durable task and run to cite.
	TaskIDEnv = "PLUTO_TASK_ID"
	RunIDEnv  = "PLUTO_RUN_ID"

	// IssueAgentEnv is the agent command the issue script runs (through
	// `sh -c`) with PLUTO_ISSUE_PROMPT in the environment. Tests point it at
	// a fake executable.
	IssueAgentEnv = "PLUTO_ISSUE_AGENT"
	// IssuePromptEnv is the prompt handed to the agent.
	IssuePromptEnv = "PLUTO_ISSUE_PROMPT"
	// IssueTokenEnv is the issue job's token: contents and pull-request
	// write, enough to push a branch and open a draft PR.
	IssueTokenEnv = "PLUTO_ISSUE_TOKEN"

	// PRTokenEnv is the PR job's token: comment-only. It must not be able to
	// push, approve, or merge.
	PRTokenEnv = "PLUTO_PR_TOKEN"
	// PRTestCommandEnv is the test command the PR script runs (through
	// `sh -c`). It defaults to `go test ./...`.
	PRTestCommandEnv = "PLUTO_PR_TEST_COMMAND"
	// PRTestTailEnv bounds how many output lines the comment quotes; the full
	// output stays in the Pluto run log.
	PRTestTailEnv = "PLUTO_PR_TEST_TAIL"
)

// IssueBranch is the deterministic branch an issue job commits to, so a
// duplicate delivery reuses the branch and its PR instead of creating another.
func IssueBranch(number string) string {
	return "pluto/issue-" + number
}

// IssueGate reports whether a comma-separated label list carries required.
// It is the job-side re-check of the daemon's declarative admission gate.
func IssueGate(labels, required string) bool {
	for _, label := range SplitLabels(labels) {
		if label == required {
			return true
		}
	}
	return false
}

// SplitLabels splits the daemon's comma-separated label list.
func SplitLabels(labels string) []string {
	var out []string
	for _, label := range strings.Split(labels, ",") {
		if label = strings.TrimSpace(label); label != "" {
			out = append(out, label)
		}
	}
	return out
}
