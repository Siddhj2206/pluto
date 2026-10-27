# Review 3 demo harness

This directory is the sample box contract for the review-3 demo. It is not the
contract of the Pluto repository.

- [`.pluto.toml`](.pluto.toml) — copy to the demo repository root as
  `.pluto.toml`.
- The scripts it names live in the Pluto checkout under `scripts/`:
  `pluto-demo-heartbeat`, `pluto-demo-agent`, `pluto-demo-test`,
  `pluto-demo-provision`, `pluto-issue-fix`, and `pluto-pr-test`.
- The procedure, environment, network truth, and known gaps are in
  [docs/review-3-demo.md](../../docs/review-3-demo.md).
- The screenshot checklist is in
  [docs/demo/r3/README.md](../../docs/demo/r3/README.md); every shot is
  pending until a real run records it.

## Install

```sh
PLUTO_SRC=~/src/pluto
DEMO=~/src/cipher

cd "$DEMO"
git checkout main
mkdir -p scripts
cp "$PLUTO_SRC/demo/r3/.pluto.toml" .pluto.toml
cp "$PLUTO_SRC/scripts/pluto-demo-heartbeat" \
   "$PLUTO_SRC/scripts/pluto-demo-agent" \
   "$PLUTO_SRC/scripts/pluto-demo-test" \
   "$PLUTO_SRC/scripts/pluto-demo-provision" \
   "$PLUTO_SRC/scripts/pluto-issue-fix" \
   "$PLUTO_SRC/scripts/pluto-pr-test" scripts/
chmod +x scripts/pluto-demo-heartbeat scripts/pluto-demo-agent \
  scripts/pluto-demo-test scripts/pluto-demo-provision \
  scripts/pluto-issue-fix scripts/pluto-pr-test
git add .pluto.toml scripts
git commit -m "pluto: review-3 demo harness"
git push origin main
```

Commit the same contract values on the branch the laptop checks out:

```sh
git checkout -B demo/pluto origin/main
git push -u origin demo/pluto
```

The default branch admits GitHub events; the checked-out branch is what the box
runs. `pluto trust` approves one contract revision, so the two must parse to the
same values (comments and whitespace do not matter).

## What each script is for

| Script | Job | Purpose |
| --- | --- | --- |
| `pluto-demo-heartbeat` | `heartbeat` | Appends `date`, `hostname`, `pwd`, trigger class, and task/run/trace to `demo-heartbeat.log`, and prints the line. The `*/2 * * * *` schedule runs it. |
| `pluto-demo-provision` | `[provision]` | Installs OpenCode once with the official installer (`--no-modify-path`) and prints `opencode --version`. |
| `pluto-demo-agent` | `issue-fix` (offline fallback) | Deterministic stand-in agent. Appends the issue prompt to `demo-issue-note.md`. Not the default and not evidence of a real agent run. |
| `pluto-demo-test` | `pr-test` | Deterministic stand-in test. Checks that the PR head has a committed change against `origin/main`, so it works for the real agent and the stand-in. Replace the inline `PLUTO_PR_TEST_COMMAND` with the repository's real test command. |
| `pluto-issue-fix` | `issue-fix` | Shipped in #173: runs the agent, pushes `pluto/issue-<n>`, and opens or reuses one draft PR. The default agent is OpenCode on a free model. |
| `pluto-pr-test` | `pr-test` | Shipped in #173: runs the test and creates or updates one marked PR comment. |

## The default agent

`[jobs.issue-fix]` is the real script, so the issue job runs:

```sh
opencode run --standalone --auto \
  --model opencode/deepseek-v4.1-flash-free "$PLUTO_ISSUE_PROMPT"
```

`opencode/deepseek-v4.1-flash-free` is a free model and needs no account, API
key, or `opencode auth`. `[provision]` installs the CLI; the job adds
`~/.opencode/bin` to `PATH` before running it. The box needs network egress for
the install and the model calls.

Override the model in the job command:

```toml
[jobs.issue-fix]
command = "PLUTO_ISSUE_MODEL=opencode/glm-5.3-flash-free scripts/pluto-issue-fix"
```

Other free models: `opencode/glm-5.3-flash-free`,
`opencode/nemotron-3-ultra-free`, `opencode/mimo-v2.6-flash-free`, and
`opencode/minimax-m3.1-flash-free`.

## Offline fallback

A box with no model or network access can use the stand-in:

```toml
[jobs.issue-fix]
command = "PLUTO_ISSUE_AGENT=scripts/pluto-demo-agent scripts/pluto-issue-fix"
```

That proves the queue, branch, push, and PR plumbing. It is **not** a real agent
run; say so in the run record and do not present it as the free-model path.

## Why the PR test command is inline

A job receives only the contract's environment plus the `PLUTO_*` values the
daemon injects. The `PLUTO_` prefix is reserved in `[env]`, so the sample sets
`PLUTO_PR_TEST_COMMAND` inline in `[jobs.pr-test]`. The issue job needs no
inline agent assignment: the script default is the free model.

The token names are `PLUTO_ISSUE_TOKEN` and `PLUTO_PR_TOKEN`, the same names
the scripts read. A contract may declare `PLUTO_` credentials, but it may not
declare the identities Pluto injects into a job (`PLUTO_EVENT_*`,
`PLUTO_TASK_*`, `PLUTO_TRACE_ID`, `PLUTO_WORKTREE`, `PLUTO_RUN_ID`). The
scripts also accept `ISSUE_TOKEN` / `PR_TOKEN` as fallbacks. See the runbook's
"Known gaps" section.

## Test

The sample contract and the demo scripts are covered by
`go test ./internal/demo`; the shipped loop scripts keep their own tests in
`internal/githubloop`, including the default free-model invocation. The harness
is data and shell, so a passing unit test does not replace the manual demo or
the pending screenshots.
