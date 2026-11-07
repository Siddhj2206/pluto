# Parallel runs get more durable boxes, not a shared cache

This amends ADR 0008. M3 settled why state is kept: finpilot's bootc build took 12 min in a new box, 36 s warm with no changes and 59 s after a late change. GitHub's runner takes 20 to 23 min. A box that starts empty is the cold case every time, so warmth has to live somewhere that lasts.

It lives in durable boxes, and only there. When a repo needs to run two things at once, it gets a second durable box, not a cache shared between boxes. Each box warms up once, the first time it's used, and stays warm after that, because boxes are kept. The cold cost is paid once per box and not once per run.

- **Today stays as it is.** A repo has one box, which runs one thing at a time. The single-user case doesn't change.
- **Later, a repo can have more than one box.** A run that finds the repo's box busy goes to another of the repo's boxes, up to a cap the host sets, 2 by default. A run goes to the idle box that last ran the same task, then to any idle box, then to a new box if the cap allows it. Otherwise it waits in the queue. Every box of a repo uses the repo's `image` and `requires`. A dev server is a run like any other: it keeps its box until it's cancelled, and other runs go to another box. This is built when a dev server in M4 blocks a run the owner wanted, or for M5, whichever comes first. `boxFor` stays the one place that decides.
- **An image's own `VOLUME`s are kept.** When Pluto creates a box, it mounts a named volume, owned by the box, at each path the image declares, except `/workspace` and `/root` themselves (already volumes) and the root or kernel paths `/`, `/proc`, `/sys` and `/dev`, which are skipped with a note on the run. Paths inside `/workspace` or `/root` get their own volume. These volumes survive a new image the way `/root` does, count in `pluto box list`, and are deleted by `pluto box reset`. If a new image no longer declares a path, its volume is kept but not mounted until a reset, so changing the image back loses nothing. Without this, `quay.io/podman/stable` puts its image store at `/var/lib/containers` in an anonymous volume, and it's lost silently when the box is replaced. That breaks the promise ADR 0008 makes. Removing a box also removes any anonymous volumes it has, so CI boxes don't leak them.
- **CI gets no shared warm state.** A job still gets a fresh box (ADR 0002). Heavy builds that need warmth run as tasks, and tools can still use their own remote caches, as finpilot does with `--cache-from`. CI boxes can't be privileged today anyway, so finpilot's build couldn't run in Pluto CI yet.

## Why not share one cache volume between boxes

A tool's cache can be written by two boxes at once only if the tool allows several writers that don't share any other state. The two caches that matter here don't allow that:

- **Podman's image store.** Each box has its own `/run`, so two boxes would share one graphroot with two runroots, which containers/storage doesn't support. Mounted-layer counts live in the runroot, so one box's prune or cleanup after a crash can remove a layer that the other box has mounted. Tags are shared too: if both boxes build `localhost/finpilot:latest`, the last one to finish wins, and the other box tests or pushes the wrong image.
- **BuildStream's CAS.** Each `bst` starts its own `buildbox-casd`, which keeps its own count of the cache size and expires objects on its own. Two of them on one directory can each delete objects that the other is still using. We haven't tested this, but BuildStream's supported way to share a cache is a remote cache server, not a shared directory.

Caches that are built for several writers, such as Go's module cache or ccache, could be shared safely, but no real repo needs that yet.

## Considered options

- **A box per branch.** The number of branches has no limit, every new branch starts cold, and most of the warm state that matters (base images, BuildStream's CAS) doesn't depend on the branch.
- **A box per run.** Always cold. That's CI, and GitHub already provides it.
- **A box per person.** Two people's working trees don't collide, but one person's dev server still blocks their own tests, and it's unclear whose box a schedule should use.
- **Parallel runs in the repo's one box.** Each run copies a working tree into the same `/workspace`, so two people or two branches would overwrite each other's files. Giving each run its own copy of the workspace loses incremental build state, and either way this needs the lock policies that ADRs 0003 and 0008 turned down.
- **A cache volume shared by all of a repo's boxes.** See above.
- **Copying a new box's home from an idle box of the same repo.** This makes extra boxes warm from the start, and on btrfs a reflink copy could take seconds. It needs a spike first: copying inside the engine's user namespace, keeping overlay whiteouts and xattrs, and never copying from a box that's running. It stays possible if a cold second box turns out to hurt.
- **A read-only image store for CI that only the host fills** (containers/storage's `additionalimagestores`). Jobs couldn't write to it, so they couldn't poison it. It would save the pull of the base image, about 5 to 6 min of finpilot's 12 min cold run. It stays possible.
- **A writable cache shared by CI jobs.** Any job could plant layers or artifacts that a later job on the default branch builds from. Refusing fork PRs doesn't fix this, because a branch in the same repo is still less trusted than the default branch. GitHub's own cache is scoped by branch for this reason. Rejected.
- **Only a warning for an image's `VOLUME`s.** The fix costs about the same, and it's easy to miss a warning before a cache disappears.

## Consequences

- With several boxes, the repo's disk use grows with the cap. `pluto box list` shows each box and its size, and `pluto box reset` clears all of them.
- A run's box is chosen when it starts, not when it's queued. Today it's chosen when the run is queued, which is fine while a repo has only one box.
- `pluto enter` with no run opens the box that was used most recently.
- `docs/tasks.md` should show the storage settings that keep podman's store under `/root` with the kernel's overlay driver. finpilot's default fuse-overlayfs took over 14 min for one layer commit.

**Revisit when:** a second box of a repo is cold often enough that people notice (measure its first run against a warm box), a real CI job spends most of its time pulling base images, or two boxes of one repo really do need to see each other's writes.
