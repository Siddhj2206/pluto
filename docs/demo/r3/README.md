# Review 3 demo screenshots

**Status: all pending.** No screenshot in this directory has been recorded. The
runbook is [docs/review-3-demo.md](../../review-3-demo.md). A shot is evidence of
a specific branch, commit, client, host, and network; it is not a mockup.

Do not fabricate a shot, reuse a screenshot from another run, or capture the
Wine rehearsal or a fake daemon. If GitHub was unreachable, record that and
leave the GitHub shots pending rather than substituting a local delivery.

## How to record

- Use a 120-column terminal (Windows Terminal on the client, the host terminal
  for host-only views). Do not resize between shots in one run.
- For Pluto views set `NO_COLOR=1` and `PLUTO_WATCH_WIDTH=120`. On Windows:

  ```powershell
  $env:NO_COLOR = "1"
  $env:PLUTO_WATCH_WIDTH = "120"
  ```

- `pluto watch --once` is the deterministic capture mode: one frame, no ANSI
  escapes when captured. A live `pluto watch --interval 2s` is allowed for the
  wake shot, but the command line must be visible.
- Capture the whole window, including the prompt and the command that produced
  the output. Crop only the desktop chrome, never the command.
- Save the file with the exact name below in this directory.
- When the first real run happens, add the date, branch, commit, Windows
  version, host version, and network to the runbook's run record. Leave the
  #162 acceptance record untouched.

## Checklist

| # | Shot | Story | Exact command | Capture mode | File | Must show | Status |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Windows connection card | Device add | `pluto.exe device add lab siddhant@192.168.1.20` | PowerShell, 120 cols | `01-device-add.png` | `saved device lab (...)`, `connected: pluto at <path>`, exit 0, no `warning:` | **pending** |
| 2 | Device doctor | Ordered connection path | `pluto.exe device doctor lab` | PowerShell, 120 cols | `02-device-doctor.png` | Every hop `ok` (or the one failed hop with its exact next step) | **pending** |
| 3 | Managed worktree | `up` by client context | `pluto.exe --device lab up` twice | PowerShell, 120 cols | `03-up-managed-worktree.png` | `created box ... for cipher/demo/pluto`, `worktree: .../.pluto/worktrees/cipher-demo-pluto`, `box ... running`; second run `already exists` | **pending** |
| 4 | Status without a box id | No-target lifecycle | `pluto.exe --device lab status` | PowerShell, 120 cols | `04-status-no-target.png` | project/branch, managed worktree path, state, `auto-pause: 5m`, `schedule: heartbeat ...` | **pending** |
| 5 | Watch: schedule wakes a paused box | Causal chain | `pluto.exe --device lab watch --interval 2s` (or `--once` after the wake) | 120 cols, `NO_COLOR=1`, `PLUTO_WATCH_WIDTH=120` | `05-watch-schedule-wake.png` | `schedule.fired` -> `queue.accepted` -> `box.wake.*` -> `job.started` -> `job.done`, one trace | **pending** |
| 6 | Schedule history | Durable occurrence | `pluto.exe --device lab schedule show cipher@demo/pluto heartbeat` | PowerShell, 120 cols | `06-schedule-history.png` | definition, `SOURCE scheduled` (or `manual`), run/job, state, reason | **pending** |
| 7 | Webhook delivery | Real GitHub intake | `pluto.exe --device lab webhook ls` and `pluto.exe --device lab events --kind webhook. --limit 20` | PowerShell, 120 cols | `07-webhook-delivery.png` | source, `/github/production`, `SECRET present`, last delivery, accepted/rejected, `webhook.received/verified/admitted` | **pending** |
| 8 | GitHub issue | The trigger | Browser: the issue page | Browser window | `08-github-issue.png` | issue number, body, `pluto:fix` label | **pending** |
| 9 | Draft pull request | Issue job result | `gh pr list --repo OWNER/cipher --head pluto/issue-<n> --json number,isDraft,url,labels` or the PR page | Browser or PowerShell, 120 cols | `09-draft-pr.png` | draft badge, `pluto:trusted`, `Closes #<n>`, the Pluto task/run/trace line | **pending** |
| 10 | PR test comment | Trusted `ready_for_review` run | `gh pr view <n> --repo OWNER/cipher --comments` or the PR page | Browser or PowerShell, 120 cols | `10-pr-comment.png` | one marked comment, `PASSED`, command, duration, head, task/run/trace | **pending** |
| 10b | PR comment updated | `synchronize` idempotency | Same as 10 after an empty commit | Browser or PowerShell, 120 cols | `10b-pr-comment-updated.png` | the same comment, a new head prefix and duration, still one marked comment | **pending** |
| 11 | Events trace | One causal chain | `pluto.exe --device lab events --trace <trc>` | PowerShell, 120 cols | `11-events-trace.png` | `webhook.*` -> `queue.accepted` -> `task.created` -> `job.*` with the same trace | **pending** |
| 12 | Overview | What is going on, where, when | `pluto.exe --device lab overview` (or `watch --once`) | 120 cols, `NO_COLOR=1`, `PLUTO_WATCH_WIDTH=120` | `12-overview.png` | host, boxes, queue, schedules, webhook sources, recent events | **pending** |
| 13 | Durability marker | Pause/wake keeps the disk | `attach`, write `demo-durability-marker`, `pause`, `up`, `attach`, `cat demo-durability-marker` | PowerShell, 120 cols | `13-durability-marker.png` | the marker after the wake, plus a few `demo-heartbeat.log` lines | **pending** |

## Optional shots

| Shot | Why | File | Status |
| --- | --- | --- | --- |
| GitHub Recent Deliveries | Proves GitHub reached the proxy, not just the handler | `07b-github-delivery.png` | **pending** |
| `pluto.exe --device lab job ls` | Shows the three declared jobs from the managed worktree | `04b-job-ls.png` | **pending** |
| `pluto.exe --device lab timeline <trc>` | The same chain as `events --trace`, one line per step | `11b-timeline.png` | **pending** |

## When a shot is recorded

Change only that row's status to `recorded` and add the date and commit to the
runbook's run record. A pending row is not a failure; it is the honest state
until a real run fills it.
