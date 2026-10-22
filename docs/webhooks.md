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
- `git`, `curl`, and either `gh` or a small GitHub API script in the box for
  the issue/PR jobs.

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
| `webhook.ignored` | the delivery was valid but intentionally produced no work (unsupported event, action, ref, or a test) |
| `webhook.admitted` | one task/queue item was created |

Each event carries the delivery ID, source, repo, ref, object ID, payload
size, and payload SHA-256 in `where`/`attrs`. Payload bodies, secrets, tokens,
and environment values are never stored.

## Issue -> draft PR -> test comment

The review-3 loop:

1. A human opens an issue on `cipher` and adds `pluto:fix`.
2. GitHub delivers the `issues` event to `/github/production`.
3. Pluto verifies the signature, admits the event, creates/reuses the issue
   box, and runs `issue-fix` with the issue payload.
4. The job uses an agent to make a focused change, commits on
   `pluto/issue-<number>`, pushes, and opens a **draft** PR linked to the
   issue. It adds `pluto:trusted` to its own same-repository PR.
5. The PR `opened`/`synchronize` delivery runs `pr-test` on the PR box.
6. The job runs the test suite and creates or updates one marked comment with
   the result, command, duration, and a link to the Pluto task/run.

Contract sketch:

```toml
[jobs.issue-fix]
command = "scripts/pluto-issue-fix"

[jobs.pr-test]
command = "scripts/pluto-pr-test"

[events.issue]
job = "issue-fix"
actions = ["labeled"]
credentials = ["PLUTO_ISSUE_TOKEN"]

[events.pull_request]
job = "pr-test"
actions = ["opened", "synchronize", "reopened", "ready_for_review"]
trusted_label = "pluto:trusted"
credentials = ["PLUTO_PR_TOKEN"]
```

Every opened issue should not start an agent. Restrict issue work to the
`pluto:fix` label, either through a declarative `labels` filter or a check in
the job. The event is still durable and visible even when the job ignores it.

Credentials:

- `PLUTO_ISSUE_TOKEN`: repository contents write and pull-request write, so
  the issue job can push a branch and open a draft PR.
- `PLUTO_PR_TOKEN`: pull-request write and issue-comment write, so the test
  job can comment. It should not be able to push, approve, or merge.
- Values live in the daemon environment. The contract stores only names. The
  queue, event log, and job output never store values.
- An unlabeled or fork PR is untrusted and receives no host credentials. It
  may run tests without a token, but it must not comment. A maintainer labels
  it `pluto:trusted` to opt in.

Idempotency:

- The issue branch is deterministic: `pluto/issue-<number>`. Check for an
  existing PR with that head before creating another.
- The PR comment carries a hidden marker, for example
  `<!-- pluto:test-report -->`. Search, then update the marked comment or
  create one.
- One durable task per issue/PR already exists. Later events append serialized
  runs; the job recomputes and updates rather than appending comments.
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
