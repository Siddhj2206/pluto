# GitHub events: webhook, issue-to-PR loop, and local hooks

Pluto turns GitHub activity into durable work. The signed GitHub webhook —
signature verification, push/PR/issue parsing, delivery deduplication, trust
checks, and task/queue admission — is already implemented. This document
describes how to operate it, not a redesign.

Webhook reception is host-side. The client device is irrelevant once the
repository and box are registered: GitHub connects to the server's listener,
not to the laptop. In the review-3 setup the server has full inbound internet;
only the client-to-server SSH leg is LAN-only.

The detailed review-3 plan is in [r3-plan.md](r3-plan.md).

## Prerequisites

- A GitHub repository with a `.pluto.toml` that declares the jobs and
  `[events.*]` policy.
- A running host daemon with an imported base image.
- A public HTTPS route to the webhook listener. Start the daemon with
  `--webhook-listen 127.0.0.1:8787` and put it behind the TLS reverse proxy
  that terminates GitHub's request. Do not expose the Pluto control API.
- A webhook secret in the daemon environment. Use the same secret in GitHub.
- `git`, `curl`, and `gh` in the box for the shipped issue/PR job scripts
  (`gh` can come from the contract's `[tools]`).

## Configure a source

```sh
export PLUTO_GITHUB_WEBHOOK_SECRET='a-long-random-secret'

pluto webhook add github --source production --box <box> \
  --secret-env PLUTO_GITHUB_WEBHOOK_SECRET
pluto webhook ls
pluto webhook test production     # signed local check, labelled as a test
```

`pluto webhook add` persists the source in the daemon state directory
(`webhooks.json`) and registers it at runtime, so the daemon unit does not
need a restart or new flags. The named environment variable must be set in
the **daemon's** environment, not only in the shell that runs the CLI. The
existing daemon flags remain supported and their sources also appear in
`pluto webhook ls`:

```sh
pluto daemon --webhook-listen 127.0.0.1:8787 \
  --github-push-source production --github-push-box <box>
```

In GitHub, create a webhook pointing through the TLS proxy to
`/github/production`, choose JSON, and subscribe to **Pushes**, **Pull
requests**, and **Issues** as configured. The secret is read from the named
daemon environment variable, used only to verify `X-Hub-Signature-256`, and
never passed to a box or job. It is never written to state.

`pluto webhook ls` shows source, kind, endpoint, secret-present flag, last
delivery, accepted/rejected counts, and the last rejection reason. A source
whose environment variable is missing stays listed with `SECRET missing` but
does not receive deliveries, so it cannot verify an empty-key signature.
Rejections are durable events, so a failed delivery is visible instead of
disappearing into journald.

`pluto webhook test <source>` sends one signed delivery through the real
handler. It is labelled as a test, records `webhook.ignored` with reason
`signed local test delivery`, and admits no work. It proves the endpoint and
secret; it does not prove GitHub can reach the host. A real GitHub delivery,
or an explicit statement that GitHub was unreachable, is still the demo
evidence.

Every intake attempt appends a durable event:

| Event | When |
|---|---|
| `webhook.received` | the body arrived, before verification |
| `webhook.verified` | the signature and route were valid |
| `webhook.rejected` | a signature, payload, trust, or queue check failed; the reason is durable |
| `webhook.deduped` | the delivery ID already resolved to a queued item |
| `webhook.ignored` | the delivery was valid but intentionally produced no work (unsupported event, action, ref, missing issue label, or a test) |
| `webhook.admitted` | one task/queue item was created |

Each event carries the delivery ID, source, repo, ref, object ID, payload
size, and payload SHA-256 in `where`/`attrs`. Payload bodies, secrets, tokens,
and environment values are never stored.

## Issue -> draft PR -> test comment

The review-3 loop:

1. A human opens an issue on `cipher` and adds `pluto:fix`.
2. GitHub delivers the `issues` event to `/github/production`.
3. Pluto verifies the signature, applies the declared `labels` gate, admits
   the event, creates/reuses the issue box, and runs `issue-fix` with the
   issue payload.
4. The job re-checks the label, runs the agent, commits on
   `pluto/issue-<number>`, pushes, and opens or reuses one **draft** PR linked
   to the issue. It adds `pluto:trusted` to its own same-repository PR.
5. The PR `opened`/`synchronize` delivery runs `pr-test` on the PR box.
6. The job runs the declared test command and creates or updates one marked
   comment with the result, command, duration, and the Pluto task/run/trace.
   It comments on failure too, and never pushes a fix.

Contract sketch:

```toml
[jobs.issue-fix]
command = "scripts/pluto-issue-fix"

[jobs.pr-test]
command = "scripts/pluto-pr-test"

[events.issue]
job = "issue-fix"
actions = ["labeled"]
labels = ["pluto:fix"]
credentials = ["PLUTO_ISSUE_TOKEN"]

[events.pull_request]
job = "pr-test"
actions = ["opened", "labeled", "synchronize", "reopened", "ready_for_review"]
trusted_label = "pluto:trusted"
credentials = ["PLUTO_PR_TOKEN"]
```

The credential names are `PLUTO_ISSUE_TOKEN` and `PLUTO_PR_TOKEN`, the same
names the scripts and the daemon environment use. A contract may declare
`PLUTO_` credential names but may not shadow the identities Pluto injects
(`PLUTO_EVENT_*`, `PLUTO_TASK_*`, `PLUTO_TRACE_ID`, `PLUTO_WORKTREE`,
`PLUTO_RUN_ID`). The scripts also accept `ISSUE_TOKEN` / `PR_TOKEN` as
fallbacks for older environments.

### The issue label gate

Every opened issue should not start an agent. `[events.issue] labels` is the
admission gate: when it is set, the issue must carry **every** listed label
(`pluto:fix` in the sketch). An unlabelled issue, or one with a different
label, is not admitted: Pluto records a durable `webhook.ignored` whose reason
names the missing label, creates no box or task, and answers `204`. The event
stays visible in `pluto events`, so "we opened an issue and nothing happened"
has an answer.

The job re-checks the same label as defense in depth (the label list reaches
it as `PLUTO_EVENT_LABELS`), so a manually queued run cannot start the agent
either. `pluto trust` shows the gate on the `[events.issue]` line before you
approve the revision.

### The job scripts

The two scripts are repo-owned, not part of the Pluto binary. They run inside
the box's worktree and use `git` plus `gh` (install `gh` through the contract's
`[tools]`, or set the agent/test commands to something that does not need it).

`scripts/pluto-issue-fix`:

- reads `PLUTO_EVENT_PAYLOAD` and `PLUTO_EVENT_LABELS`, and exits 0 without
  work when the issue lacks `pluto:fix`;
- runs `PLUTO_ISSUE_AGENT` through `sh -c` with `PLUTO_ISSUE_PROMPT` in the
  environment; without an override it runs `opencode run --standalone --auto
  --model opencode/deepseek-v4.1-flash-free "$PLUTO_ISSUE_PROMPT"`, a free
  model that needs no account or API key (`PLUTO_ISSUE_MODEL` overrides it);
- commits whatever the agent changed on `pluto/issue-<number>` and pushes it.
  The token reaches git through a credential helper, so it is never in argv or
  output. No changes means no branch, no PR;
- looks up an open PR with that head and reuses it, otherwise opens one draft
  PR whose body contains `Closes #<number>`;
- adds `pluto:trusted` to that PR.

`scripts/pluto-pr-test`:

- runs `PLUTO_PR_TEST_COMMAND` through `sh -c` (default: `go test ./...`) and
  prints the full output to the run log;
- creates or updates exactly one comment carrying the hidden marker
  `<!-- pluto:test-report -->`, with the result, command, duration, head
  commit, and the Pluto task/run/trace. Only the last `PLUTO_PR_TEST_TAIL`
  lines are quoted; the full output stays in Pluto;
- comments on failure too, then exits with the test's status. It never runs
  `git`, so a failing test cannot push and cannot trigger another
  `synchronize` delivery;
- runs the tests but skips the comment when `PLUTO_EVENT_TRUSTED` is not
  `true` or `PLUTO_PR_TOKEN` is not present, and says why in the run log.

The job also sees `PLUTO_TASK_ID`, `PLUTO_RUN_ID`, and `PLUTO_TRACE_ID`, so
`pluto events --trace <id>`, `pluto task show <task>`, and
`pluto run logs <run>` join the comment to the delivery that caused it.

Credentials:

- `PLUTO_ISSUE_TOKEN`: repository contents
  write and pull-request write, so the issue job can push a branch and open a
  draft PR.
- `PLUTO_PR_TOKEN`: issue-comment write only, so
  the test job can comment. It must not be able to push, approve, or merge; do
  not give it contents write.
- Values live in the daemon environment. The contract stores only names. The
  queue, event log, and job output never store values.
- An unlabeled or fork PR is untrusted and receives no host credentials. The
  test job still runs the suite without a token, but it does not comment. A
  maintainer labels the PR `pluto:trusted` to opt in; the issue job labels the
  PRs it opens itself.

Idempotency:

- The issue branch is deterministic: `pluto/issue-<number>`. The job looks up
  an existing PR with that head before creating another, so a retried delivery
  reuses the branch and the PR.
- The PR comment carries a hidden marker, `<!-- pluto:test-report -->`. The
  job searches the PR comments, then updates the marked comment or creates
  one.
- One durable task per issue/PR already exists. Later events append serialized
  runs; the job recomputes and updates the same comment rather than appending
  another.
- The PR test job only comments. It does not push a fix on failure, so a
  failing test cannot create an infinite synchronize loop.

## Trust and admission

Event mappings must be on the trusted default branch and each selected job
must be declared under `[jobs.<name>]`. `pluto setup github` writes the
`[events.*]` sections and ends with the two commands that close the loop:

```text
next: approve this exact contract revision with 'pluto trust <box>'
next: register the source with 'pluto webhook add github --source production --box <box> --secret-env PLUTO_GITHUB_WEBHOOK_SECRET'
```

`pluto trust <box>` reads the box's current worktree contract through the
daemon, shows the revision and the declared jobs and `[events.*]` policy, asks
for confirmation (or takes `--yes`), and calls the existing contract-trust
API. The approval covers one exact revision and is recorded as
`contract.approved`.

Pluto checks the exact trusted revision at admission and again before the job
runs. A contract change blocks queued event work rather than running under a
revision nobody approved.

## Local post-commit hook

`pluto init --with-hooks` installs a best-effort post-commit hook. It submits
the worktree, commit, and branch to the daemon's unix socket. The daemon
accepts it only when the worktree already has a box, the trusted contract
declares `[events.push] job = "name"`, and that exact revision is approved. It
records one durable post-commit task per branch and appends one run per unique
commit. It never creates or starts a box, and a stopped daemon does not affect
the commit. Local hooks do not work from a different host.
