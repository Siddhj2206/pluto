# pluto: vision

**Pluto turns machines you own into a place to run CI, builds, dev servers and scheduled tasks, and using them from any device feels local.**

Status: rewrite in progress, with no product code yet. Live state and milestones are tracked in [#175](https://github.com/Siddhj2206/pluto/issues/175).

## Who it's for

Individuals and small teams who self-host. The first real user is the maintainer, on a laptop with no server. The first team shape is one shared server, and ideally a pool of machines later.

## What it does

- **First job:** self-hosted CI, starting with GitHub Actions. The git host keeps its workflows. Pluto runs the official runner in a fresh container for each job, and `runs-on` is the only workflow edit.
- **Next:** scheduled tasks, builds and dev servers on the same engine.
- **The reason to choose it:** remote use and developer experience.

## What it isn't

A terminal or multiplexer, a git host replacement, a hosted service, Kubernetes, or a place for untrusted code. Entering a box is a debugging tool. Full 1:1 Actions compatibility is not a goal.

## Where it runs

All work runs in Linux containers. Linux hosts run natively; macOS and Windows hosts come later through a Linux VM that Pluto manages. Clients come to all three.

## Promises

- You don't need an account, and there's no telemetry.
- SSH isn't needed for onboarding or normal use.
- Other git hosts must remain possible.

## How decisions get made

1. Every feature traces to a real user and a real moment of need.
2. Each milestone ends in something usable by itself.
3. Every new ADR ends with a **Revisit when** line naming the evidence that would reopen it. A new tool or an exciting idea alone doesn't count.
4. Cutting is normal.
