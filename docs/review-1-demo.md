# Review 1 — live demo runbook

**Scope guardrail.** Review 1 shows only the **box**, the **microVM**, and the
**lifecycle** — and the live demo is just three beats: *make a box → run a build
in it → show the box exists*. Scheduling is talked through, not demoed. Do not
open the new-UX CLI, the event/issue boxes, or the remote-device material; those
belong to later reviews.

Companion explainer: [review-1-explainer.md](review-1-explainer.md).

---

## What is set up right now

| Thing | State |
| --- | --- |
| `pluto` on PATH | `~/.local/bin/pluto` — **master / old UX**, `v0.1.5-43-gbf14e33` |
| new-UX (M5) build | parked **off PATH** at `~/.local/share/pluto-bins/pluto-m5` |
| host daemon | active (systemd user service), running the master binary |
| demo project | `~/Documents/Projects/siddhant.foo` (Astro site, bun + mise) |
| demo box | `6fac3da0` (`siddhant.foo/master`) — **running**, provision done in 46 s, wake 182 ms, 2 services |

The imported base image is on the older **6.1.186** kernel, so `uname -a` in the
box reports `6.1.186`. The point is that the guest kernel is *not the host's*;
the exact number is whatever image is imported.

> **Do not project `pluto status` raw.** It prints the box's git remote, and this
> box's remote currently has an embedded GitHub token in it. Use `pluto ls` for
> "the box exists", or pipe status through
> `sed -E 's#(x-access-token:)[^@]+#\1<redacted>#g'`.

---

## The demo

All commands run from `~/Documents/Projects/siddhant.foo`.

### Beat 1 — make a box

```sh
pluto up      # creates the box if absent, wakes it if paused (idempotent)
pluto ls      # the box exists, keyed to a worktree
```

If the box already exists, `up` reports it and ensures it is running — that is
the same verb you would use on a brand-new worktree.

### Beat 2 — run a build in it

```sh
pluto run build          # the declared [jobs.build] runs inside the box
pluto jobs .             # history: state, exit code, duration
pluto logs . --job last  # the recorded build output
```

The build runs `mise run build` (Astro, ~2 s) on the box's own disk — not on the
host.

### Beat 3 — the box exists right now

```sh
pluto ls
```

One box, `siddhant.foo/master`, state `running`, provision `done`.

### Optional one-liner — prove it is a machine, not a container

```sh
pluto run -- uname -a     # the guest kernel, distinct from the host's
```

---

## If I want to show creation, not just existence

The box already exists, so `pluto up` is idempotent. To show a box being made
from nothing, destroy it and create it again (provision re-runs, ~45 s):

```sh
pluto destroy 6fac3da0 --yes   # removes the record, its disk, and its job history
pluto up                       # fresh box: pin image, reflink disk, boot, provision
pluto ls
```

---

## Controls

### Keep it from auto-pausing mid-demo

The contract sets `auto_pause = "15m"` (idle and unattached). Running any command
resets the clock. To disable it for the session, set `auto_pause = "off"` in
`.pluto.toml`, then `pluto pause . && pluto up .`.

### Daemon

```sh
systemctl --user is-active pluto.service     # expect: active
systemctl --user restart pluto.service       # restart on the master binary
```

The unit runs `ExecStart=/var/home/sid/.local/bin/pluto daemon` (the original
unit carried M5-only webhook flags the master daemon rejects; it was backed up at
`pluto.service.m5-backup`).

### If a box misbehaves

```sh
pluto status <box>                       # avoid projecting: contains the remote token
pluto logs <box> --phase provision
pluto logs <box> --phase wake
pluto logs <box> --service dev
pluto pause <box> && pluto up <box>
```

### Internal: restore the new-UX binary later (review 3)

```sh
cp ~/.local/share/pluto-bins/pluto-m5 ~/.local/bin/pluto
systemctl --user restart pluto.service
```
