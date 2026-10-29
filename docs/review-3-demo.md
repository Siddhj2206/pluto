# Review 3 demo runbook

**Status: procedure, not a record.** This document describes how to run the
review-3 demo from a clean host state. It is not evidence that the run
happened. No screenshot under [docs/demo/r3](demo/r3/README.md) is filled, and
the Windows acceptance record in
[windows-client-acceptance.md](windows-client-acceptance.md) remains
**unfilled**. A successful rehearsal on Linux does not fill either one.

The demo is the manual script in [r3-plan.md](r3-plan.md) §4. The plan is the
explanation; this page is the exact sequence.

## What the run proves

- A Windows client connects to a Linux host on the LAN and can explain the
  connection (`device add`, `device doctor`).
- From the laptop checkout, `pluto --device <host> up` creates or resumes the
  branch's managed worktree and box without a host path or box id. `pause`,
  `run`, `status`, `attach`, `logs`, and `job ls` resolve the same target.
- A two-minute schedule wakes a paused box, runs a job, and leaves a durable
  result with a trace.
- A real GitHub issue labelled `pluto:fix` is delivered by webhook to the
  server, starts a real OpenCode agent on a free model (no account, API key, or
  `opencode auth`), and produces a draft PR. A pull-request event runs tests and
  creates or updates one marked comment.
- Every step is visible in `watch`, `events --trace`, `timeline`, and
  `overview`: what happened, where, when, and why.

## What the run does not prove

- It does not fill the #162 Windows acceptance record. That record covers a
  cross-network Tailscale run and a real Windows 11 x64 operator; this demo
  stays on a LAN or phone hotspot and does not use Tailscale.
- It does not prove the Pluto control API is public. It is not: the only
  listener that leaves loopback is the signed webhook intake.
- It does not prove a signed local `webhook test` is a GitHub delivery. It is a
  handler check (see step 6).
- It does not prove a GitHub delivery happened if GitHub was unreachable. Say
  so and show the failed or missing delivery; do not substitute a local test.

## The network, exactly

```text
Windows laptop  --SSH over LAN or phone hotspot-->  Linux host
GitHub          --HTTPS over the public internet--> TLS reverse proxy
TLS proxy       --HTTP on 127.0.0.1:8787----------> Pluto webhook listener
```

- The client-to-server leg is **LAN or hotspot only**. Use the host's LAN
  address (`hostname -I`, `ip -4 addr`) or the hotspot address, not a tailnet
  name.
- The server has **inbound internet** so GitHub can reach the TLS reverse proxy
  that terminates the webhook request. That is the only public surface.
- **No Tailscale, relay, or tunnel is used for R3.** The webhook path does not
  depend on the laptop.
- The Pluto control API is a **unix socket**, never a TCP listener. Do not
  expose it through the proxy, and do not add a second public route.
- `pluto webhook test` runs the signed handler locally; it does not cross the
  network. A real GitHub delivery, or an explicit statement that GitHub was
  unreachable, is the demo evidence.

## Host prerequisites

1. Linux x86_64 with KVM, a running daemon, and an imported base image
   ([testing.md](testing.md)).
2. A demo repository with an **HTTPS** origin. The issue job pushes a branch
   with a token; an SSH origin cannot use that token. This runbook uses
   `cipher` as the repository name.
3. The same harness commit on the repository's **default branch** and on the
   branch the laptop checks out. The default branch is what admits GitHub
   events; the branch is what the box runs. The parsed contract values must be
   identical, or `pluto trust` approves one revision and admission sees another.
4. `git`, `curl`, and `gh` on the host. The box gets `gh` and `curl` from the
   contract's `[tools]`; `[provision]` uses `curl` to install OpenCode, and the
   issue and PR scripts use `gh`.
5. The box has **network egress** for the one-time OpenCode install. The agent
   itself uses a free model and needs no OpenCode account or API key.
6. Three values in the **daemon's** environment:
   `PLUTO_ISSUE_TOKEN` (contents + pull-request write), `PLUTO_PR_TOKEN`
   (issue-comment write only), and `PLUTO_GITHUB_WEBHOOK_SECRET`.

> **Credential names.** The contract and the scripts use `PLUTO_ISSUE_TOKEN`
> and `PLUTO_PR_TOKEN`. The validator allows `PLUTO_` credential names and
> refuses only the identities Pluto injects (`PLUTO_EVENT_*`, `PLUTO_TASK_*`,
> `PLUTO_TRACE_ID`, `PLUTO_WORKTREE`, `PLUTO_RUN_ID`). Export the same names in
> the daemon environment. The scripts also accept `ISSUE_TOKEN` / `PR_TOKEN`
> as fallbacks for older environments.

### Daemon environment and webhook listener

The webhook listener is a daemon flag. `pluto webhook add` registers a source at
runtime, but the daemon still has to be listening. For a foreground demo:

```sh
install -d -m 700 ~/.config/pluto
cat > ~/.config/pluto/daemon.env <<'EOF'
PLUTO_ISSUE_TOKEN=replace-with-the-issue-token
PLUTO_PR_TOKEN=replace-with-the-comment-only-token
PLUTO_GITHUB_WEBHOOK_SECRET=replace-with-a-long-random-secret
EOF
chmod 600 ~/.config/pluto/daemon.env

set -a
. ~/.config/pluto/daemon.env
set +a
pluto daemon --webhook-listen 127.0.0.1:8787
```

For the installed systemd user service, add a drop-in instead:

```sh
systemctl --user edit pluto
```

```ini
[Service]
EnvironmentFile=%h/.config/pluto/daemon.env
ExecStart=
ExecStart=%h/.local/bin/pluto daemon --webhook-listen 127.0.0.1:8787
```

Then `systemctl --user daemon-reload && systemctl --user restart pluto`. Use the
absolute path from `command -v pluto`; the example assumes `~/.local/bin`.

Point the existing TLS reverse proxy at `127.0.0.1:8787`, with
`https://<host>/github/production` as the only public path. Do not proxy the
unix socket or any other daemon route. An unsigned request should be rejected:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' -X POST https://<host>/github/production
```

Expect a 4xx (missing or invalid signature/payload), not a connection error.
That proves the route is reachable, not that GitHub delivered anything.

### Repository labels

Create both labels once:

```sh
gh label create pluto:fix --repo OWNER/cipher \
  --description "Pluto may start an agent for this issue" --color 0e8a16
gh label create pluto:trusted --repo OWNER/cipher \
  --description "Pluto may run trusted automation on this pull request" --color ededed
```

The issue script also creates `pluto:trusted` best-effort on its first run, but
the preflight wants it before the demo.

## Install the harness into the demo repository

The sample lives in this checkout at [demo/r3/.pluto.toml](../demo/r3/.pluto.toml).
It names six repo-owned scripts: `pluto-demo-heartbeat`, `pluto-demo-agent`,
`pluto-demo-test`, `pluto-demo-provision`, `pluto-issue-fix`, and
`pluto-pr-test`.

```sh
PLUTO_SRC=~/src/pluto          # this checkout
DEMO=~/src/cipher              # the demo repository

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

Now create or update the branch the laptop checks out, with the same contract
values:

```sh
git checkout -B demo/pluto origin/main
git push -u origin demo/pluto
```

If `demo/pluto` already has work, merge `main` into it instead of resetting it;
do not rewrite the presenter's branch. The contract hash is over parsed values,
so comments and whitespace may differ, but the values must not.

## 0. Preflight

Run this on the host before touching the client:

```sh
cd ~/src/pluto
set -a; . ~/.config/pluto/daemon.env; set +a
scripts/r3-demo-preflight.sh --contract ~/src/cipher --github OWNER/cipher
```

It is read-only. Expect `ok:` for the daemon, the image, the three jobs, the
six scripts, the HTTPS origin, `PLUTO_ISSUE_TOKEN`, `PLUTO_PR_TOKEN`,
`PLUTO_GITHUB_WEBHOOK_SECRET`, and both labels,
then `preflight passed`. Every `FAIL:` names one exact next step. The token
checks read this shell; the daemon needs the same names (step "Daemon
environment").

## 1. Windows: add and doctor the device

On the Windows 11 x64 laptop, in PowerShell:

```powershell
ssh -V
cd $env:USERPROFILE\src\cipher
git status --short --branch
pluto.exe device add lab siddhant@192.168.1.20 --trust accept-new
pluto.exe device doctor lab
pluto.exe device ls
```

Replace `siddhant` and `192.168.1.20` with the host account and its LAN or
hotspot address. Host-key checking is strict by default, so the first `device add` passes
`--trust accept-new` once. If the host refuses the laptop's key, `device add`
confirms the account can authenticate, offers `ssh-copy-id` (which asks for
the account password), re-probes with the key, and never stores the password. Expect
`saved device lab (...)` and `connected: pluto at <path>` with no warning.

`device doctor` prints the ordered hops (`local.ssh`, `config.resolve`, `dns`,
`tcp`, `proxy`, `hostkey`, `auth`, `shell`, `pluto.lookup`, `pluto.version`,
`protocol`, `daemon`, `state`, `image`). All should be `ok`; a `failed` hop
names the one command that fixes it.

**Screenshot:** `01-device-add.png`, `02-device-doctor.png`.

## 2. From the checkout: create or resume the box by context

```powershell
git push -u origin demo/pluto
pluto.exe --device lab up
pluto.exe --device lab up
```

The first `up` resolves the client context (`cipher`, `demo/pluto`), clones or
reuses the host project, creates the managed worktree
`~/.pluto/worktrees/cipher-demo-pluto`, and boots the box. Expect:

```text
created box <id> for cipher/demo/pluto
worktree: /home/<user>/.pluto/worktrees/cipher-demo-pluto
box <id> running (image <sha>)
phases: provision:done wake:done
```

The second `up` is idempotent and prints `box <id> already exists for
cipher/demo/pluto`, then the same `worktree:` line and `box <id> running`. No
box id or host path was typed. If `up` refuses because the local branch is ahead
of `origin`, push it; the box only runs a committed ref from origin.

**Screenshot:** `03-up-managed-worktree.png` (both runs), and the local
`git status --short --branch`.

## 3. Lifecycle without a box id

```powershell
pluto.exe --device lab status
pluto.exe --device lab job ls
pluto.exe --device lab run
pluto.exe --device lab run heartbeat
pluto.exe --device lab logs --job last
pluto.exe --device lab pause
pluto.exe --device lab up
pluto.exe --device lab attach
# inside the box:
hostname
uname -a
pwd
exit
```

- `status` shows the project/branch, the managed `worktree:` path, the state,
  the contract staleness, `auto-pause: 5m`, and the `heartbeat` schedule line.
- `job ls` and bare `run` list `heartbeat`, `issue-fix`, and `pr-test` from the
  managed worktree.
- `run heartbeat` is the schedule's job run by hand. Its line says
  `trigger=manual`; the scheduled occurrence later says `trigger=task` and
  carries the task/run/trace identities.
- `logs --job last` prints that line from the job log.
- `pause` prints `box <id> paused`; `up` wakes the same box and runs `wake`,
  not `provision`.
- `attach` opens an interactive shell in the managed worktree.

**Screenshot:** `04-status-no-target.png`.

## 4. Trust the exact contract revision

```powershell
pluto.exe --device lab trust cipher@demo/pluto --yes
```

Expect the box identity, the contract revision, `jobs: heartbeat, issue-fix,
pr-test`, the `[events.issue]` line with `[labels pluto:fix]`, the
`[events.pull_request]` line with `[trusted label pluto:trusted]`, and
`approved revision <hash> for cipher`.

`trust` uses the same client-context default as `status` and `pause`, so
`pluto.exe --device lab trust --yes` approves the checkout's box without a
target. The managed `project@branch` form above still works and wins when
given. `--yes` is the non-interactive form; without it the command asks
`approve this exact revision for unattended work? [y/N]`.

## 5. The schedule fires while paused

```powershell
pluto.exe --device lab pause
pluto.exe --device lab schedule ls
```

`schedule ls` shows the box, `heartbeat`, `*/2 * * * *`, `NEXT` with
`(in 1m)`, and the last outcome. Then either wait for the cron minute or force
one occurrence for a predictable demo:

```powershell
pluto.exe --device lab schedule run cipher@demo/pluto heartbeat
```

`schedule run` materializes one explicit occurrence and does not move the cron
clock. It prints the occurrence, the run, and a `next: pluto events --trace
<trc>` hint. In another Windows terminal, watch the chain:

```powershell
$env:NO_COLOR = "1"
$env:PLUTO_WATCH_WIDTH = "120"
pluto.exe --device lab watch --interval 2s
```

Because the box was paused, expect this order (the box-wake events appear only
for a paused box):

```text
schedule.fired -> queue.accepted -> task.created
box.wake.started -> phase.wake.started -> phase.wake.done -> box.wake.done
job.started -> job.done -> queue.done -> task.run.done
```

Then read the durable result:

```powershell
pluto.exe --device lab schedule show cipher@demo/pluto heartbeat
pluto.exe --device lab logs --job last
pluto.exe --device lab events --kind schedule. --limit 20
```

`schedule show` lists the occurrence with `SOURCE scheduled` (or `manual` for
the explicit run), the run/job, the state, and the reason. The job log line is
`<utc> host=<box> pwd=<worktree> trigger=task task=... run=... trace=...`; the
same line is appended to `demo-heartbeat.log` in the worktree.

**Screenshot:** `05-watch-schedule-wake.png`, `06-schedule-history.png`.

## 6. Register the webhook and check the handler

```powershell
pluto.exe --device lab webhook add github --source production `
  --box cipher@demo/pluto --secret-env PLUTO_GITHUB_WEBHOOK_SECRET
pluto.exe --device lab webhook ls
pluto.exe --device lab webhook test production
pluto.exe --device lab events --kind webhook. --limit 20
```

`webhook add` persists the source and registers it at runtime; the daemon still
needs `--webhook-listen` (host prerequisites). `webhook ls` shows `SECRET
present`, the endpoint `/github/production`, the last delivery, and
accepted/rejected counts.

`webhook test` sends one signed delivery through the **local handler**. Expect
`sent signed test delivery ... verified (ignored: signed local test delivery)`.
It records `webhook.ignored` and admits no work. **It is a handler check, not a
GitHub delivery**, and it does not prove the TLS proxy or GitHub reachability.
Do not screenshot it as the webhook-delivery evidence.

In GitHub, open the repository's Settings, Webhooks, Add webhook:

- Payload URL: `https://<host>/github/production`
- Content type: `application/json`
- Secret: the value of `PLUTO_GITHUB_WEBHOOK_SECRET`
- Events: **Issues** and **Pull requests**

The first real delivery appears under Recent Deliveries and in `webhook ls`.
If GitHub cannot reach the host, say so and show the failed delivery; do not
substitute the local test.

## 7. GitHub issue to draft PR

On the laptop (or in the browser):

```powershell
gh issue create --repo OWNER/cipher `
  --title "Demo: make one focused change" `
  --body "The review-3 demo asks Pluto to make one focused change."
gh issue edit <number> --repo OWNER/cipher --add-label pluto:fix
```

Adding `pluto:fix` is the admission gate. An issue without it is ignored with a
durable `webhook.ignored` whose reason names the missing label, and no box or
task is created.

### The agent and the free model

`[jobs.issue-fix]` runs `scripts/pluto-issue-fix`, which starts a **real
OpenCode agent** by default:

```sh
opencode run --standalone --auto \
  --model opencode/deepseek-v4.1-flash-free "$PLUTO_ISSUE_PROMPT"
```

The model is a free OpenCode model. It needs **no account, API key, or
`opencode auth`**, so the box has nothing to sign in to. `[provision]` installs
the CLI once with the official installer
(`curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path`), and
the job adds `~/.opencode/bin` to `PATH` before it runs. The box needs egress
for that install; the agent's model calls also use the network.

Override the model inline in `[jobs.issue-fix]` when a different free model is
preferred:

```toml
[jobs.issue-fix]
command = "PLUTO_ISSUE_MODEL=opencode/glm-5.3-flash-free scripts/pluto-issue-fix"
```

Other free models verified at the time of writing:
`opencode/glm-5.3-flash-free`, `opencode/nemotron-3-ultra-free`,
`opencode/mimo-v2.6-flash-free`, and `opencode/minimax-m3.1-flash-free`. The
script default is `opencode/deepseek-v4.1-flash-free`; `PLUTO_ISSUE_MODEL` is
the override, and `PLUTO_ISSUE_AGENT` replaces the whole command.

**Offline fallback, not the demo default.** A box with no model access can run
the deterministic stand-in instead:

```toml
[jobs.issue-fix]
command = "PLUTO_ISSUE_AGENT=scripts/pluto-demo-agent scripts/pluto-issue-fix"
```

The stand-in appends the issue prompt to `demo-issue-note.md`. It proves the
queue, branch, push, and PR plumbing, but it is **not evidence of a real agent
run**; label it that way if it is used. The default path above is the real
agent.

The sample PR test (`scripts/pluto-demo-test`) passes when the PR head has a
committed change against `origin/main`, so it works for either agent. Replace
the inline `PLUTO_PR_TEST_COMMAND` in `[jobs.pr-test]` with the demo
repository's real test command for a real review.

Watch the intake and the job:

```powershell
pluto.exe --device lab watch --interval 2s
# or, in another terminal:
pluto.exe --device lab events --kind webhook. --follow
```

Expect one trace through:

```text
webhook.received -> webhook.verified -> webhook.admitted
queue.accepted -> task.created -> job.started -> job.done
```

Then read the identities:

```powershell
pluto.exe --device lab webhook ls
pluto.exe --device lab events --trace <trc>
pluto.exe --device lab task ls
pluto.exe --device lab task show <task>
pluto.exe --device lab run logs <run>
gh pr list --repo OWNER/cipher --head pluto/issue-<number> `
  --json number,isDraft,url,labels
```

The job log shows the deterministic branch `pluto/issue-<number>`, the push,
the draft PR URL, and `labelled PR ... pluto:trusted`. `gh pr list` should show
`"isDraft": true` and the `pluto:trusted` label. The PR body contains
`Closes #<number>` and the Pluto task/run/trace line.

**Screenshot:** `07-webhook-delivery.png` (`webhook ls` plus the ledger),
`08-github-issue.png`, `09-draft-pr.png`.

## 8. Pull request to one test comment

The PR is a draft and the issue job labels it `pluto:trusted` **after** it is
opened. GitHub generates the `opened` delivery before that label exists, so the
first `pr-test` run is untrusted: it runs the test, prints why it is skipping
the comment, and does not comment. That is expected, not a failure.

Adding the label produces a separate `labeled` delivery, which the sample's
`[events.pull_request] actions` allows. That run sees `pluto:trusted` in the
payload and posts the one marked comment. Watch for it; no extra step is
needed:

```powershell
pluto.exe --device lab events --kind webhook. --limit 20
```

Expect a second `pr-test` run, this time with `PLUTO_EVENT_TRUSTED=true` and
`PLUTO_PR_TOKEN` present. It creates one comment containing the hidden marker
`<!-- pluto:test-report -->`, `PASSED`, the command
(`scripts/pluto-demo-test`), the duration, the head's first 12 characters, and
`Pluto task ... · run ... · trace ...`, followed by the quoted output tail.

`ready_for_review` remains an allowed action, so marking the draft ready is a
harmless extra trusted delivery; it is not the mechanism that produces the
first comment.

```powershell
pluto.exe --device lab task ls
pluto.exe --device lab run logs <run>
gh pr view <number> --repo OWNER/cipher --comments
```

To show that later commits update the same comment instead of adding another,
push an empty commit to the PR head:

```powershell
gh pr checkout <number> --repo OWNER/cipher
git commit --allow-empty -m "demo: move the PR head"
git push
```

The `synchronize` delivery is trusted (the label is present), runs the test on
the new head, finds the marked comment, and PATCHes it. Expect the same comment
count, a new head prefix, and a new duration. The job never pushes a fix, so a
failing test cannot loop.

**Screenshot:** `10-pr-comment.png` (first trusted comment),
`10b-pr-comment-updated.png` (after the empty commit), `11-events-trace.png`.

## 9. Watch, overview, and the causal chain

```powershell
$env:NO_COLOR = "1"
$env:PLUTO_WATCH_WIDTH = "120"
pluto.exe --device lab watch --once
pluto.exe --device lab overview
pluto.exe --device lab health
pluto.exe --device lab events --trace <trc>
pluto.exe --device lab timeline <trc>
```

`watch --once` prints exactly one frame with no ANSI escapes when captured, so
it is the deterministic screenshot mode. `overview` is one static read of the
same aggregate. `events --trace` and `timeline` show the whole chain: intake,
queue, task, run, job, and result, with the recorded reason at each step.

**Screenshot:** `12-overview.png` (or a `watch --once` frame).

## 10. Durability

```powershell
pluto.exe --device lab attach
# inside the box, in the managed worktree:
echo "review-3 marker $(date -u +%Y-%m-%dT%H:%M:%SZ)" > demo-durability-marker
cat demo-durability-marker
tail -n 5 demo-heartbeat.log
exit

pluto.exe --device lab pause
pluto.exe --device lab up
pluto.exe --device lab attach
# inside:
cat demo-durability-marker
tail -n 5 demo-heartbeat.log
exit
```

The marker and the heartbeat history survive the pause and the wake because
they are on the box disk. No target was typed for any of these commands.

**Screenshot:** `13-durability-marker.png`.

## 11. Teardown

```powershell
pluto.exe --device lab pause
pluto.exe --device lab destroy cipher@demo/pluto --yes
```

`destroy` is deliberately explicit; it removes the record, the disk, and the
job history. To remove the managed worktree too:

```sh
pluto worktree rm cipher-demo-pluto
```

Do not remove the project or the demo repository until the screenshots and the
run record are saved.

## Screenshots

The exact checklist, filenames, and capture modes are in
[docs/demo/r3/README.md](demo/r3/README.md). Every shot is **pending** until a
real Windows/GitHub run records it. Do not fabricate a shot, do not reuse a
Wine rehearsal, and do not mark a shot recorded from a fake daemon.

## Run record

This table is blank. Fill it only after a real run, against a stated commit,
with the screenshots in `docs/demo/r3/`. It does not fill the #162 acceptance
record.

| Field | Value |
| --- | --- |
| Date (with timezone) | |
| Operator | |
| Branch and commit under test | |
| Windows edition, build, architecture | |
| `pluto.exe version` and SHA-256 | |
| Host `pluto version` and `uname -srm` | |
| Client network and host LAN/hotspot address | |
| Webhook public URL and proxy | |
| Issue number and PR number | |
| Screenshots recorded (filenames) | |
| Result (pass / partial / failed, with the first failing step) | |

## Known gaps and accuracy notes

- **Credential names are the documented ones.** The sample, the scripts, and
  the daemon environment use `PLUTO_ISSUE_TOKEN` and `PLUTO_PR_TOKEN`; the
  scripts keep the unprefixed names as fallbacks. A contract may not declare
  the identities Pluto injects into a job.
- **The free-model default is verified separately from the demo.** The
  anonymous `opencode run --standalone --auto --model
  opencode/deepseek-v4.1-flash-free` invocation and the installer path were
  checked on 2026-10-22. That is not a recorded demo run; the screenshots and
  the run record stay pending until the full loop runs.
- **The stand-in agent is an offline fallback.** It is not evidence of a real
  agent run. If the demo uses it, say so in the run record and do not present
  it as the free-model path.
- **The `opened` PR delivery is untrusted by design.** The issue job labels
  its PR after creation, so the first `pr-test` run skips the comment. The
  following `labeled` delivery is trusted and posts the one comment; later
  `synchronize` deliveries update it. The sample allows `labeled` for this
  reason.
- **`webhook test` is in-process.** It calls the route handler through
  `httptest`, not the TCP listener, so it does not prove the proxy or GitHub
  reachability. The CLI help says "proves the endpoint"; read that as the
  handler and secret.
- **`schedule run` needs a target.** The ticket asks for `schedule run`, but the
  command still takes a box/worktree positional. Use the managed
  `cipher@demo/pluto`, not a box id.
- **The #162 record stays unfilled.** This demo is a LAN/hotspot run; it does
  not claim the cross-network Tailscale acceptance.
