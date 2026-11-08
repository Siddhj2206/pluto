# Pluto: product

What Pluto does, for whom, what's built, and what's next. This is the working list: milestones, to-dos and open questions live here and are edited in place. The why is in [VISION.md](VISION.md). Decisions that last go into [ADRs](adr/). The dated log stays in [#175](https://github.com/Siddhj2206/pluto/issues/175).

## What it is

**Your team's own build machine.** Pluto runs the work behind the code (CI jobs, builds, tests, dev servers, scheduled and overnight jobs) on machines you own, and everyone on the team can watch it and reach it from any device. It's a base that people and teams build their own workflows on, not a collaborative editor and not an AI product. Agents are one workload among many.

**What it has to beat:** Tailscale plus the `actions/runner` binary, set up by hand. Nothing else combines a durable box per repo, branch previews, visible runs and fresh CI runners on one machine without Kubernetes. The gap is how the pieces fit together, so the developer experience is the product: setup time, the box lifecycle, and one obvious command for each job.

## Who it's for

**Now:** the maintainer, on a laptop with no server. Runs Quill's CI there, and wants heavy builds (BuildStream, bootc images) to run overnight and be readable in the morning.

**Next:** someone with a second device who wants to use the same machine from anywhere. Then a team on one shared server: one person runs `pluto run dev` on a branch and a teammate opens it; several people watch a build's or test's log live. A pool of machines comes later, only when a real team needs it.

## What it does today

On the `rewrite` branch. The user guide is [tasks.md](tasks.md).

- **CI for GitHub Actions.** `pluto ci connect github` and `pluto host start`. The official runner runs in a fresh container per job, and `runs-on` is the only workflow change. A failed job's box can be entered. Fork PRs are refused by a pre-job hook. The host polls GitHub every 15 seconds.
- **Tasks.** Named lists of shell steps in `.pluto/`, run with `pluto run`, in one durable box per repo. `/workspace` and `/root` survive runs and image changes.
- **Schedules.** Cron on a task, read from the committed branch, in local time. Missed slots collapse into one run with a note. Overlapping slots are skipped. On mains power, the host asks the machine not to sleep near a run.
- **Secrets.** Per repo and per host, never shown again after being set. Stored unencrypted in a user-only file.
- **Privilege.** A repo asks with `requires: [privileged]`, and the host grants it with `pluto box grant --privileged`. Rootless engines only.
- **CLI.** Daily verbs on top (`run`, `exec`, `history`, `logs`, `cancel`, `enter`), managed nouns below (`task`, `schedule`, `box`, `secret`, `ci`, `host`). See [ADR 0009](adr/0009-cli-shape.md).

## Decided, not built yet

- **Zero-setup route between devices:** Tailcat, provisionally ([ADR 0004](adr/0004-tailcat-is-the-zero-setup-route.md)).
- **Joining:** a long one-time token, pasted or scanned, on every route ([ADR 0005](adr/0005-join-with-a-long-token.md)).
- **macOS and Windows hosts:** through a Linux VM that Pluto manages. Clients on all three.
- **Other git hosts:** Forgejo, GitLab and plain git after GitHub.

## Later, generic

Each of these serves several workloads (CI, deploys, previews, agents), and none is justified by one alone. None is built until a real need pulls it in.

- **Previews.** `pluto run dev` gives a URL teammates can open. A snapshot of the working tree or a pushed branch, not a live mirror of the editor.
- **Shared live logs** for work outside CI: overnight builds, tests started from a laptop, dev servers. GitHub already shows CI logs to the repo's members.
- **Task inputs and event triggers.** An event (issue labelled, PR opened, a POST to a URL) starts a task with inputs. Outsiders can't trigger work, by the same rule that refuses fork PRs: issue text from a stranger reaching a task that holds a push token is a prompt-injection hole.
- **Egress allowlists** for boxes. Pluto has no egress control today.

## Not doing

Not a terminal or multiplexer, a collaborative code environment, a git host replacement, a hosted service, Kubernetes, or a place for untrusted code. No full 1:1 Actions compatibility. No custom layer store, no result caching. No secret sync between hosts. No SSH for onboarding or normal use. No live sync of a developer's editor into a box. No building on mise's task model: none of the current repos use mise, it has no hook for running tasks elsewhere, and its config makes no stability promise. No vendor-specific agent integrations (Copilot, Claude Code or Cursor self-hosted workers): their control plane stays in the vendor's cloud, and each is a separate beta API to track.

## Milestones

| # | Usable result | Done when | Status |
|---|---|---|---|
| M1 | Quill's CI runs on my laptop | A real workflow goes green with only `runs-on` changed; a failed job's box can be entered; a fork PR is refused | Built. Fork PR test still owed |
| M2 | Overnight BuildStream builds on my laptop | A scheduled `bst build` runs overnight, the next run is measurably faster because its cache survived, and I can read the result in the morning, including an honest report if the laptop slept | Built. Acceptance checks deferred |
| M3 | Heavy builds get faster | finpilot's bootc build and the BuildStream build measured against `ubuntu-latest`, using durable boxes. Small repos like Quill only report their numbers | **In progress** |
| M4 | Works from a second device | Pair with no SSH, follow logs through a network change, enter a box, forward a dev server. Runs keep going while no client is connected, and a restarted host reconciles its records with the boxes that actually exist | Waiting on a second machine |
| M5 | A second person uses it | Someone who didn't build it gets a green job without help, opens a teammate's preview, and follows a teammate's build live | |

Later, only when a real need pulls it in: a pool of machines, TUI, GUI, phone, macOS and Windows hosts, other git hosts.

## Now

- **M3 result, finpilot (2026-11-07).** As a Pluto task in a durable privileged box: cold 12m00s, warm with no changes **36 s**, warm after a late-layer change **59 s**. GitHub's `ubuntu-24.04` takes 20–23 min, though that includes push and signing, so only the warm numbers are a fair win. A fresh CI box would be the cold case every time. This settled the box question: [ADR 0010](adr/0010-more-boxes-not-shared-caches.md).
- **Spike findings, all fixed:** ~~image `VOLUME`s lost when the box is replaced~~, ~~`--yes` rejected~~ and ~~repo name case~~ (`ca83891`); ~~no per-step timestamps in logs~~. In finpilot itself: `quay.io/podman/stable` needs native overlay set up, or layer commits stall.

- **Next code work, in order (2026-11-08):**
  1. **Finish "why it isn't running":** show the reason in `task list`, and give each reason the command to run next. *Done 2026-11-08.*
  2. **GitHub polling before M5:** conditional requests (`ETag`) and rate-limit handling. See "No webhooks; poll smarter" below.
  3. **Reconcile on restart:** keep each box's wanted spec and bring real boxes in line. See "Desired state, then reconcile" below. Groundwork for M4.
  4. **M4, second device:** pairing over Tailcat with a join token. Waits on the second machine.

## Decisions to revisit

| Decision | What reopens it | When |
|---|---|---|
| ~~One durable box per repo, one run at a time~~ | **Settled 2026-11-07 by [ADR 0010](adr/0010-more-boxes-not-shared-caches.md):** more durable boxes per repo (cap 2) when parallel runs are needed; built at M4 or M5 | Done |
| ~~No separate cache feature~~ | **Settled by ADR 0010:** still none. Shared cache volumes are unsafe for podman and BuildStream | Done |
| ~~Webhooks through the relay route~~ | **Settled 2026-11-07: dropped.** Tailcat has no HTTP ingress, and every stable public URL needs an account or a public host. See To look into | Done |
| No `include:`, no task inputs ([ADR 0007](adr/0007-config-lives-in-dot-pluto.md)) | Event triggers need inputs; reuse needs a second repo copying a task | When event triggers are built |
| Secrets unencrypted, one host only | Teams sharing a host; release signing keys | Team milestone |
| All work runs in Linux containers | A desktop app needs macOS builds and notarisation | When a desktop app is real |

## Owner's to-do

- [ ] **M1 fork PR test.** Open a PR to Quill from a second GitHub account, based on `ci/baseline`. Expect a failed check with Pluto's refusal message and no steps run. Fork refusal stays out of the docs as a claim until this passes.
- [ ] **Job through the connected token.** Push to Quill's `ci/baseline` and check that `pluto history` shows it passing.
- [ ] **M2 acceptance.** One green Frameless build by hand. The last failure was Savannah returning HTTP 500 for `m17n-lib`, not Pluto. Then push the `demo` branch, run `pluto schedule enable --branch demo`, let it run overnight, and compare times in the morning.
- [ ] **Suspend test.** Close the lid during a run and check that the morning report says what happened.
- [ ] **Tailcat cross-network test.** Needs the second machine. It must pass before M4 ships on that route.
- [ ] **Revoke the old `GITHUB_TOKEN`** from the deleted `~/.config/pluto/webhook.env`.
- [ ] **Talk to people** who run self-hosted runners today.

## To look into

- **mise for tools in the box.** If a repo has a `mise.toml`, Pluto runs `mise install` when it sets up the box, and the tools stay in the durable `/root`. Opt-in, and Pluto never parses mise's config. Lets the default image stay slim.

- **No webhooks; poll smarter.** Checked 2026-11-07. Tailcat can't give GitHub a public URL, and every stable one (Funnel, named Cloudflare tunnels, ngrok) needs an account. smee.io and `gh webhook forward` are for development only, by their own docs. So:
  - **CI pickup:** keep polling, or keep one idle runner registered in a waiting fresh box. GitHub then hands it the job over the runner's own outbound connection, with no polling and near-zero start delay, at the cost of an idle container. Worth a spike when start delay or API use starts to hurt.
  - **Event triggers (issue labelled and so on):** poll with `ETag` and `since`. Latency is the poll interval. Instant delivery means accepting an account-based public URL as an opt-in, explicitly.
  - **Before M5, either way:** conditional requests (`ETag`; a 304 doesn't count against the limit) and rate-limit handling. Neither exists today. Polling uses 2 requests per idle repo every 15 s, so about 10 repos on one token hit the 5,000/hour limit.

- **Why it isn't running (from Borg).** Every run that is queued, skipped or blocked carries one line saying why and what to run next. Built for queued runs (box busy, slot full, behind CI), each with the command that shows more, in `history`, `task list`, `run` and `ci list`. Skipped runs, collapsed slots, missing grants and sleep have a reason or note but no next command yet; add one when a real case needs it. Borg's "why pending?" was among its most-used features.

- **Two priority bands (from Borg).** Built: work someone is waiting on goes before scheduled runs, and scheduled runs hold back while a CI job is queued, until they have waited 30 minutes. No preemption.

- **Desired state, then reconcile (from Borg and Kubernetes).** The host keeps each box's wanted spec (image, grants, volumes) and brings the real container in line at start and after changes, instead of patching drift case by case. Same for runs: on restart, records are checked against the containers that exist.

## Target bars

- First CI job in under 10 minutes (not yet measured).
- A network switch during a run loses no output and needs no recovery command.
- Every failure says what went wrong and what to run next.
