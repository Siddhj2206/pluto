# pluto

**pluto gives every git worktree a durable work machine: a microVM box that
wakes for work, sleeps when idle, and can be entered with SSH — on hardware
you own.**

Long work — agents, builds, tests — should not die when the laptop sleeps.
Cloud sandboxes solve that by owning your machine and your account; pluto
solves it on your own host, with no accounts, no control plane, and no
telemetry. The box belongs to the branch: it keeps its disk between sessions,
wakes on a schedule, and is one `pluto attach` away.

- **Agent runs that outlive the laptop.** An agent runs in the branch's box;
  close the laptop; come back and attach to the machine, not a chat log.
- **Scheduled work with a place to land.** Nightly builds, tests, migrations,
  and agent sweeps wake on a schedule; when something fails you enter the
  machine that failed.
- **Warm branch environments.** Dependencies, services, and databases stay
  installed between sessions.

pluto is agent-agnostic: an agent is a declared job, a service, or an ad-hoc
`pluto run --`. The vision — and what pluto does not claim — is in
[docs/VISION.md](docs/VISION.md).

## How it works

One host daemon owns the boxes and talks to the CLI over a unix socket. Each
box is a Firecracker microVM on local disk, reached over SSH (through vsock,
mediated by the host). A repository declares its box in `.pluto.toml`:
image, provision, wake, services, jobs, and schedules. The daemon applies the
contract when a box boots.

The field-by-field reference is [docs/contract.md](docs/contract.md); the
decisions behind it live in [docs/adr/](docs/adr/). From a second machine,
`pluto --device` runs the same CLI over SSH — recipes are in
[docs/remote-access.md](docs/remote-access.md).

## Quickstart

There are no releases yet; until they exist, build from source. Boxes are
Firecracker microVMs, so the host must be Linux x86_64.

### 1. Prerequisites

- Linux x86_64 with a writable `/dev/kvm` and unprivileged user namespaces
- Rootless podman (with subuid/subgid entries), `slirp4netns`, and e2fsprogs
- `git` with a configured identity, `ssh`, `curl`, `tar`, and `sha256sum`
- The Go toolchain from [go.mod](go.mod)

The full list, with what each tool is for, is in
[images/README.md](images/README.md). Nothing in the quickstart needs root.

### 2. Clone and build the CLI

```sh
git clone https://github.com/Siddhj2206/pluto
cd pluto
go build -o pluto ./cmd/pluto
export PATH="$PWD:$PATH"   # so the rest of the quickstart can say 'pluto'
pluto version
```

The guest agent and vsock helper are built into the image in the next step.

### 3. Build the base image

```sh
images/build.sh
```

This downloads the kernel and Firecracker, builds the rootfs and guest agent
with rootless podman, and writes `images/out/{vmlinuz,rootfs.img,manifest.json}`
(about 3 GB under `images/out`).

### 4. Install the daemon and import the image

```sh
pluto install              # systemd user service, started now and at login
pluto image import images/out
pluto image ls
```

`pluto install` runs the daemon as a systemd user service with linger. To run
it in the foreground instead, keep `pluto daemon` running in another terminal;
the commands below talk to whichever daemon is listening. Boxes boot the
newest imported image, so an existing box never changes under you.

### 5. Your first box

Every box belongs to a git worktree, so start a project with a `.pluto.toml`
and a commit. [docs/contract.md](docs/contract.md) is the field-by-field
reference; this is the small version:

```sh
mkdir -p ~/src/hello && cd ~/src/hello
git init -q
git config user.email you@example.com
git config user.name "You"

cat > .pluto.toml <<'TOML'
[box]
auto_pause = "1h"

[provision]
command = "echo provisioned $(date -u) > .provisioned"

[wake]
command = "echo woke $(date -u) >> .provisioned"

[jobs.hello]
description = "say hello from the box"
command = "echo hello from $(hostname) in $(pwd)"
TOML

git add .pluto.toml && git commit -qm "add the pluto contract"
```

`provision` runs once for the life of the box's disk; `wake` runs on every
start. Now create and boot it:

```sh
pluto up
pluto status ~/src/hello   # state, phases, contract staleness, latest job
```

The first boot runs provision, so the box may report work in progress for a
moment; `pluto logs ~/src/hello --phase provision` shows the output.

### 6. Run, attach, pause, wake

```sh
pluto run                   # list the worktree's declared jobs
pluto run hello             # run one in the box
pluto run -- uname -a       # run a one-off command

pluto attach                # an interactive shell in the box (wakes it first)

pluto pause ~/src/hello     # stop it cleanly; the disk stays on the host
pluto up                    # wake it again — wake runs, provision does not

pluto ls                    # ids for every box on this host
```

Box commands take a worktree path or a box id; `pluto up` and `pluto run`
default to the current worktree. `pluto jobs` lists recent runs and
`pluto logs <box> --job last` reads the newest one.

### 7. A nightly schedule

Add a job and a schedule to `.pluto.toml`:

```toml
[jobs.nightly]
description = "record the nightly run"
command = "date -u > last-nightly.txt"

[[schedule]]
name = "nightly"
cron = "0 2 * * *"          # every day at 02:00 UTC
job = "nightly"
```

Commit the change, then apply it on the next boot:

```sh
git add .pluto.toml && git commit -qm "schedule the nightly run"
pluto pause ~/src/hello
pluto up
pluto jobs ~/src/hello      # after 02:00 UTC: the recorded run
pluto logs ~/src/hello --job last
```

Schedules are stored with the box record, so they survive daemon restarts and
host reboots. A trigger wakes a paused box, runs its job, and records the
outcome; a schedule without a `job` is a warm-up. Like the rest of the
contract, an edit applies on the next boot — `pluto status` flags a worktree
whose contract changed since the box applied it.

### 8. Where next

- [docs/contract.md](docs/contract.md) — every field of `.pluto.toml`, with
  examples, plus the generated JSON schema for editors.
- [docs/remote-access.md](docs/remote-access.md) — `pluto device`, tailnet /
  WireGuard / port-forward recipes, and `ssh -L` into an in-box web UI.
- [docs/VISION.md](docs/VISION.md) — the claim and the non-claims.
- [docs/DEFERRED.md](docs/DEFERRED.md) — what is deliberately parked.
- [docs/testing.md](docs/testing.md) — gofmt/vet/unit tests, and the host-only
  box tests that need KVM.
- [scripts/e2e-m1.sh](scripts/e2e-m1.sh) — the real-host end-to-end run of
  the M1 demo: schedule, job history, auto-pause, attach, and `--device`.
