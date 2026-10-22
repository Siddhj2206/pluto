// Package demo names the review-3 demo harness: the sample box contract, the
// repo-owned scripts it installs into the demo repository, and the runbook and
// screenshot directory that describe the manual run.
//
// The harness is deliberately data, not daemon or CLI behavior. These names
// exist so the tests that parse the sample contract and drive the scripts do
// not drift from the files the runbook tells a presenter to copy.
package demo

const (
	// ContractPath is the sample contract, copied to the demo repository root
	// as .pluto.toml.
	ContractPath = "demo/r3/.pluto.toml"
	// RunbookPath is the manual review-3 procedure.
	RunbookPath = "docs/review-3-demo.md"
	// ScreenshotDir is where a real run records its screenshots.
	ScreenshotDir = "docs/demo/r3"

	// HeartbeatJob, IssueFixJob, and PRTestJob are the declared jobs.
	HeartbeatJob = "heartbeat"
	IssueFixJob  = "issue-fix"
	PRTestJob    = "pr-test"
	// ScheduleName is the two-minute schedule that runs HeartbeatJob.
	ScheduleName = "heartbeat"

	// HeartbeatScript, AgentScript, and TestScript are the demo-only scripts.
	HeartbeatScript = "scripts/pluto-demo-heartbeat"
	AgentScript     = "scripts/pluto-demo-agent"
	TestScript      = "scripts/pluto-demo-test"
	// ProvisionScript installs OpenCode for the free-model agent.
	ProvisionScript = "scripts/pluto-demo-provision"
	// PreflightScript is the read-only host check.
	PreflightScript = "scripts/r3-demo-preflight.sh"
)

// IssueLabel and TrustedLabel are the two repository labels the loop needs.
const (
	IssueLabel   = "pluto:fix"
	TrustedLabel = "pluto:trusted"
)

// SecretEnv names the three values the daemon environment must hold. The
// contract stores the names, never the values.
//
// The token names are ISSUE_TOKEN and PR_TOKEN, not PLUTO_ISSUE_TOKEN and
// PLUTO_PR_TOKEN: the contract validator reserves the PLUTO_ prefix for
// [events.*] credential names, while the shipped scripts read the PLUTO_
// names. The sample's job commands map the declared names to the script names.
var SecretEnv = []string{
	"ISSUE_TOKEN",
	"PR_TOKEN",
	"PLUTO_GITHUB_WEBHOOK_SECRET",
}
