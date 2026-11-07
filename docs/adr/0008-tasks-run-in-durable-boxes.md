# Tasks run in durable boxes

**Amended by [ADR 0010](0010-more-boxes-not-shared-caches.md):** a repo can get more than one durable box for parallel runs, and an image's own `VOLUME`s are kept like `/root`.

This replaces ADR 0003. A task runs in a durable box, and each repo has one box by default. The box has one image and runs one thing at a time. Its `/workspace` and home directory live in volumes, so they survive a new image, and anything else in the box is lost when the image changes. The image only changes when someone edits it or runs `pluto box reset`, so a build never shifts overnight on its own. CI stays as it is, with a fresh box for each job (ADR 0002).

We changed this because of real workloads. BuildStream builds in `fsdk-containers` depend on keeping `~/.cache/buildstream` between runs, and GitHub Actions already gives a clean place to check that something builds from nothing. With volumes that survive, tasks don't need a separate cache feature.

Which box a run uses is decided in one place in the code, so one box per task can be tried without touching anything else. Dev servers in M4 are the likely reason to try it.

## Considered options

- **Fresh boxes with caches kept outside (ADR 0003).** More reproducible, but it needs a cache feature to get back what a durable box already has, and it repeats what Actions provides.
- **One box per task.** Allows different images for each task, but keeps a separate copy of the code and caches for each one.
- **Moonshot's lock policies (old ADR 0018).** They let runs share a box at the same time, which nothing needs yet.
- **A custom store of swappable layers (old ADR 0017).** Podman already shares base image layers on disk, and recreating a container while keeping its volumes gives the same update path. Swapping individual packages doesn't work, because layers are an ordered stack and packages all change the same package database.

## Consequences

- A build can pass only because of files left by an earlier run. `pluto box reset` clears the box, and CI remains the clean check.
- Durable boxes grow, so `pluto box list` has to show their size.
- Pluto doesn't cache results. Every run executes every step, and tools like BuildStream decide what to skip.

**Revisit when:** leftover state causes a wrong result that users actually hit, or dev servers show that one box per repo is the wrong default.
