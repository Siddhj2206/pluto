# R3 demo — 5 minutes, hooks and observability

This is the short demo script. It is deliberately **not** an agent demo: the
issue and PR jobs are deterministic, so flaky model access cannot break the
run. The story is the part R3 is actually about:

> A hook fires, Pluto admits durable work, a box wakes, the job runs, and the
> ledger explains what happened, where, when, and why — on one screen.

The long-form runbook is [`review-3-demo.md`](review-3-demo.md). The sample
contract is [`../demo/r3/.pluto.toml`](../demo/r3/.pluto.toml). The screenshot
checklist is [`demo/r3/README.md`](demo/r3/README.md).

---

## What is already set up

| Thing | State |
|---|---|
| Cipher `demo/pluto` | Contract + scripts committed and pushed |
| Cipher `master` (default branch) | Same contract + scripts, for trusted event admission |
| Labels | `pluto:fix`, `pluto:trusted` exist on `Siddhj2206/cipher` |
| Pluto build | `pluto` on the host PATH; `pluto.exe` in the Windows Downloads |
| Demo box | `cipher@demo/pluto`, managed worktree `~/.pluto/worktrees/cipher-demo-pluto` |

The cipher contract keeps its normal `build`, `test`, `fmt`, `clippy`, and
`nightly-tests` entries. R3 adds `heartbeat`, `issue-fix`, `pr-test`, a
two-minute schedule, and the `[events.*]` hooks. `issue-fix` uses the
deterministic stand-in agent; `pr-test` uses the deterministic diff check. Both
can be swapped for the real agent / `cargo test` later.

---

## Before the demo (host, once)

Run these on the Linux host. Do not paste token values into a shell that gets
recorded.

### 1. Daemon and image

```sh
systemctl --user start pluto
pluto health
pluto image ls          # at least one image
```

### 2. Secrets in the daemon environment

The daemon needs three names. The values are never written to state or events.

```sh
# Example for a foreground daemon; for systemd use an EnvironmentFile.
export PLUTO_ISSUE_TOKEN='...'          # contents + pull-request write
export PLUTO_PR_TOKEN='...'             # issue-comment write only
export PLUTO_GITHUB_WEBHOOK_SECRET='...'
```

If the daemon is the systemd service, put them in
`~/.config/pluto/webhook.env` and restart the service.

### 3. Register the webhook source

The source is persisted and registered at runtime; no daemon restart is needed.

```sh
pluto ls                                  # find the cipher box id
pluto webhook add github --source production \
  --box <cipher-box-id> \
  --secret-env PLUTO_GITHUB_WEBHOOK_SECRET
pluto webhook ls
```

In GitHub, the repository webhook must point through the TLS proxy to
`/github/production`, use JSON, and subscribe to **Issues** and **Pull
requests**. The proxy is the only public listener; the Pluto control API is
not public.

### 4. Approve the exact contract revision

```sh
pluto trust cipher@demo/pluto --yes
```

The trusted default branch (`master`) and the demo branch carry the same
contract values, so one approval covers the event policy.

### 5. Pre-warm and check

```sh
pluto status cipher@demo/pluto
pluto schedule ls cipher@demo/pluto
pluto events --since 10m
```

The cipher box should be `running` with `provision: done`. The heartbeat
schedule is every two minutes; the nightly test schedule stays at 03:00.

Optional, from the Pluto checkout:

```sh
scripts/r3-demo-preflight.sh \
  --contract ~/.pluto/worktrees/cipher-demo-pluto \
  --github Siddhj2206/cipher
```

### 6. Prepare the GitHub props

Do this before the audience arrives, so the live action is one click:

- Open an issue on `cipher` with a short title and body. **Do not add
  `pluto:fix` yet.**
- Optional: open a small PR from a branch (for the PR-only path). The issue
  path creates its own draft PR.

---

## The 5-minute run

Times are a guide, not a contract. The whole point is to keep `watch` on
screen and narrate the causal chain.

### 0:00–0:45 — One screen for the host

On Windows, from the cipher checkout on `demo/pluto`:

```powershell
pluto.exe --device lab status
pluto.exe --device lab overview
```

Point at: daemon version/uptime, state directory, boxes running/max, queue
depth, active jobs, schedules with next due, webhook sources, recent events.

Say: *"This is the whole host, not one box. Every row is read from the same
durable ledger."*

### 0:45–1:45 — A schedule wakes a paused box

```powershell
pluto.exe --device lab pause
pluto.exe --device lab schedule ls cipher@demo/pluto
pluto.exe --device lab schedule run cipher@demo/pluto heartbeat
pluto.exe --device lab watch --once
```

If the two-minute cron is about to fire, you can skip `schedule run` and just
wait; the explicit run is the reliable version.

Point at the LIVE tail:

```text
schedule.fired -> queue.accepted -> box.wake.started -> phase.wake.done
-> job.started -> job.done
```

Say: *"The schedule did not just run a command. It produced a durable queue
item, woke the box, ran a job, and left a reason for every step."*

### 1:45–3:15 — The GitHub hook

Add the `pluto:fix` label to the issue you prepared. Keep `watch` open.

What appears, in order:

```text
webhook.received -> webhook.verified -> webhook.admitted
-> queue.accepted -> task.created -> job.started -> job.done
```

Then show the result:

```powershell
pluto.exe --device lab webhook ls
pluto.exe --device lab events --kind webhook. --since 5m
```

The issue job:

1. checks the label gate;
2. checks the trusted contract revision;
3. creates or reuses the issue work-item box;
4. appends the issue prompt to `demo-issue-note.md` with the deterministic
   stand-in agent;
5. commits and pushes `pluto/issue-<n>`;
6. opens or reuses one **draft** PR and labels it `pluto:trusted`.

The `labeled` PR delivery then runs `pr-test` and posts one marked comment.
`opened` alone is untrusted by design, so the first PR event runs tests but
does not comment; `labeled` is the trusted run.

Say: *"The repository event became durable work. The hook is the same signed
webhook as before; R3 made its intake and decisions visible."*

### 3:15–4:15 — One causal chain

Pick the trace from the watch tail or the webhook/issue event:

```powershell
pluto.exe --device lab events --trace trc_...
pluto.exe --device lab timeline trc_...
```

Point at the `where` columns: host, box, project, branch, worktree, task, run,
job, queue, schedule, source, delivery. Point at the `reason` on any wait,
block, or failure.

Say: *"This is the answer to 'what happened, where, when, and why'. It
survives a daemon restart because it is a file, not scrollback."*

### 4:15–5:00 — Close on the aggregate

```powershell
pluto.exe --device lab overview
pluto.exe --device lab health
```

End on the one-screen answer: boxes, queue, schedules, webhook sources, and
the recent-event tail. Mention that the same data is available as JSON
(`--json`) and as a stream (`events --follow`).

---

## If something is slow or unavailable

| Problem | Fallback |
|---|---|
| GitHub delivery is slow or the proxy is down | Run `pluto webhook test production` (handler check only, no work), then show the trace from the pre-run chain. Say clearly that it is a local signed check, not a GitHub delivery. |
| The issue job is provisioning its box | Keep `watch` open; narrate `phase.provision.*` and `queue.dispatched`. The job is bounded; do not start a second one. |
| The PR comment did not appear | Check `pluto webhook ls` for the rejection reason. If the PR only received `opened`, add `pluto:trusted` manually to trigger the `labeled` delivery. |
| Model access is flaky | The demo does not use a model. The stand-in agent is the default. Do not switch to the free-model path live. |
| The daemon is down | `pluto health` prints the exact `next:` hint; restart the service before the demo, not during it. |

---

## What not to claim

- This is **not** the #162 Windows/Tailscale acceptance run. That record stays
  unfilled.
- `pluto webhook test` is an in-process handler check, not a GitHub delivery.
- The stand-in agent is plumbing evidence, not a real agent run.
- Screenshots are pending until a real run records them; do not present a
  mock as a capture.
- The client-to-server leg is LAN/hotspot only. GitHub reaches the server's
  public webhook endpoint; GitHub never talks to the laptop.

---

## One-command recap

```powershell
# Windows, cipher checkout on demo/pluto
pluto.exe --device lab status
pluto.exe --device lab pause
pluto.exe --device lab schedule run cipher@demo/pluto heartbeat
pluto.exe --device lab watch --once
# add pluto:fix to the issue
pluto.exe --device lab webhook ls
pluto.exe --device lab events --trace trc_...
pluto.exe --device lab timeline trc_...
pluto.exe --device lab overview
```
