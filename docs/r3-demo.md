# R3 demo — a PR triggers a test

This is the short, reliable demo: open a pull request from a pre-existing
branch, GitHub sends the webhook, Pluto admits a task, and the `test-pr` job
runs the check. No agent, no model, no comment.

## What is already set up

| Piece | State |
|---|---|
| Cipher `master` | `.pluto.toml` with `[events.pull_request] job = "test-pr"` and `.pluto/test-pr.sh` |
| Cipher `demo/pluto` | Same contract; this is the laptop box's branch |
| Cipher `demo/webhook-pr-2` | Pre-existing PR branch; open a PR from it during the demo |
| GitHub webhook | `pull_request`, active, id `704339018` |
| Public URL | `https://interviews-procedure-northeast-retailers.trycloudflare.com/github/production` |
| Pluto listener | `127.0.0.1:8787` in the daemon service |
| Source | `production`, registered to the cipher box |
| Contract | revision `360b3382…` approved |
| Box | `cipher@demo/pluto` running and provisioned |

The PR job is intentionally small:

```sh
# .pluto/test-pr.sh
cd "${PLUTO_WORKTREE:-$PWD}"
git diff --check
```

It is a real check that runs on the base image; it does not need the Rust
toolchain. Swap it for `cargo test --locked` when the PR is trusted and the
box has the environment layer.

## Before the demo

On the server:

```sh
systemctl --user is-active pluto
pluto health
pluto webhook ls
```

You should see `production`, `secret present`, and the last delivery. The
quick tunnel process must still be running:

```sh
pgrep -af 'cloudflared tunnel --no-autoupdate --url http://127.0.0.1:8787'
```

If the tunnel is gone, the public URL changes and the GitHub hook must be
updated. Keep the server on between now and the demo.

On the Windows laptop:

1. Copy `pluto.exe` from the server's Downloads (or the copy you already have).
2. Find the server's hotspot address:
   ```sh
   # on the server
   ip -4 addr show wlp1s0 | grep 'inet '
   ```
3. Add the device once:
   ```powershell
   pluto.exe device add lab sid@<server-ip> --trust accept-new
   pluto.exe device doctor lab
   ```
4. Clone cipher and check out the laptop branch:
   ```powershell
   git clone https://github.com/Siddhj2206/cipher.git
   cd cipher
   git checkout demo/pluto
   pluto.exe --device lab up
   ```
   The box is already running; this should resume it quickly.

If `device add` is too slow, `--device sid@<server-ip>` works directly and
prompts for the SSH password.

## The 5-minute run

### 0:00–0:45 — Host view

```powershell
pluto.exe --device lab overview
pluto.exe --device lab watch --once
```

Point at: daemon uptime, boxes, queue, schedules, webhook sources, recent
events. This is the “what is happening” screen.

### 0:45–1:45 — Open the PR

Use the GitHub UI or:

```powershell
gh pr create --repo Siddhj2206/cipher --base master `
  --head demo/webhook-pr-2 `
  --title "Webhook demo" `
  --body "A pull request from a pre-existing branch."
```

The `opened` delivery hits the hook. On the server:

```powershell
pluto.exe --device lab webhook ls
pluto.exe --device lab events --since 5m --kind webhook.
```

Expected:

```text
webhook.received
webhook.verified
webhook.admitted
```

`webhook ls` shows the last delivery and `accepted 1` (or more).

### 1:45–3:00 — The task

```powershell
pluto.exe --device lab task ls
```

Look for `cipher/pull_request/<number>`, state `completed` (the test is fast).
Then:

```powershell
pluto.exe --device lab task show <task-id>
pluto.exe --device lab run logs <run-id>
```

The run log ends with:

```text
pr-test: <short-sha> on pluto/pr-<number>
pr-test: ok
```

This is the proof: a GitHub pull-request delivery became a durable task and
ran the test.

### 3:00–4:00 — One causal chain

```powershell
pluto.exe --device lab timeline <task-id>
```

You should see:

```text
webhook.received -> webhook.verified -> webhook.admitted
-> queue.accepted -> task.created -> queue.dispatched
-> task.run.started -> job.started -> job.done -> task.run.done
```

Point at the `where` column: `cipher/pluto/pr-<number>` and the delivery id.

### 4:00–5:00 — Close on the aggregate

```powershell
pluto.exe --device lab overview
```

The same webhook source, queue, task, and recent events are on one screen.

## Optional: the schedule

If you have a spare minute, show a time hook too:

```powershell
pluto.exe --device lab pause
pluto.exe --device lab schedule run cipher@demo/pluto heartbeat
pluto.exe --device lab watch --once
```

The chain is `schedule.fired -> queue.accepted -> task.created -> box.wake ->
job.started -> job.done`. The heartbeat schedule also fires every two minutes
on its own.

## If the webhook does not arrive

1. `pluto webhook ls` — check `last delivery` and `last rejection reason`.
2. If there is no delivery, the tunnel or the GitHub hook is the problem:
   - Check the cloudflared process.
   - The quick-tunnel URL changes if the process restarts; update the hook:
     ```sh
     gh api repos/Siddhj2206/cipher/hooks/704339018 -X PATCH \
       -f config[url]="https://<new-url>/github/production"
     ```
3. `pluto webhook test production` exercises the handler in-process, but it
   admits no work. It is a handler check, not a GitHub delivery.
4. If GitHub is unreachable, say so and show the last recorded delivery and
   task from the ledger. Do not claim a live delivery that did not happen.

## What not to claim

- This is not the #162 Windows/Tailscale acceptance run.
- `webhook test` is not a GitHub delivery.
- The PR test is a fast git check, not the full cipher suite. Swap in
  `cargo test --locked` if you want the real suite and can wait.
- The quick tunnel is a demo ingress, not a production webhook setup.
