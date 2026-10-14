# pluto

**pluto gives every workspace a durable work machine: a container box that
wakes for work, sleeps when idle, and can be entered with a shared terminal —
on hardware you own.**

Long work — agents, builds, tests — should not die when the laptop sleeps.
Cloud sandboxes solve that by owning your machine and your account; pluto
solves it on your own host, with no accounts, no control plane, and no
telemetry. A workspace has one box: it keeps its layers and durable volumes
between sessions, wakes when work arrives, and is one `pluto attach` away.

- **Runs that outlive the laptop.** Work runs in the workspace's box; close the
  laptop; come back and attach to the machine, not a chat log.
- **Warm environments.** A provisioned base layer and a package layer keyed on
  your lockfiles are reused by every box, so setup is a cache hit.
- **Work you can read after it finishes.** Every run keeps a structured record
  and per-step logs; `pluto ls runs`, `pluto show`, and `pluto logs` surface
  them.

## How it works

One host daemon owns the boxes and talks to the CLI over a unix socket. A box
is a container whose root is composed from content-addressed layers, with
durable volumes outside that stack. A project declares its box, provision,
wake, tasks, and schedules in a single `pluto.yml`.

The field-by-field reference is [docs/pluto-yml.md](docs/pluto-yml.md); the
decisions behind it live in [docs/adr/](docs/adr/). From a second machine,
`pluto --device` runs the same CLI over SSH — recipes are in
[docs/remote-access.md](docs/remote-access.md).

## Quickstart

Boxes are containers, so the host needs a container engine and a git checkout.

### 1. Prerequisites

- Linux with a rootless container engine (`podman` or `docker`) on `PATH`
- `git` with a configured identity
- The Go toolchain from [go.mod](go.mod)

### 2. Install the CLI

With Go (1.27 or newer):

```sh
go install github.com/Siddhj2206/pluto/cmd/pluto@latest
```

Or build from a checkout: `go build -o pluto ./cmd/pluto`.

### 3. Run the daemon

```sh
pluto daemon
```

Keep it running in another terminal; the commands below talk to it over the
unix socket.

### 4. Your first box

Start a project with a `pluto.yml`. [docs/pluto-yml.md](docs/pluto-yml.md) is
the field-by-field reference; this is the small version:

```sh
mkdir -p ~/src/hello && cd ~/src/hello
git init -q
git config user.email you@example.com
git config user.name "You"

cat > pluto.yml <<'YAML'
version: 1

provision:
  base: ubuntu:24.04

wake: echo woke >> .provisioned

tasks:
  hello:
    run: echo hello from $(hostname)
    lock: exclusive
YAML

git add pluto.yml && git commit -qm "add the pluto contract"
```

`provision` builds the reusable layers once; `wake` runs on every start. Now
create and start the box:

```sh
pluto box create --workspace ~/src/hello
pluto box status ~/src/hello   # workspace, state, layers, volumes
```

### 5. Run, attach, step down

```sh
pluto run hello             # run a declared task in the box

pluto attach                # a shared terminal in the box (wakes it first)
pluto attach --watch        # ... read-only

pluto box ls                # every box on this host
pluto box rm ~/src/hello --yes
```

A box freezes after a short idle (memory preserved) and stops after a longer
idle (volumes kept); the next work wakes it. Every run keeps its record and
step logs: `pluto ls runs`, `pluto show <run>`, `pluto logs <run> --follow`.

### 6. Where next

- [docs/pluto-yml.md](docs/pluto-yml.md) — the contract, with examples, plus
  the generated JSON schema for editors.
- [docs/runs.md](docs/runs.md) — run records, step logs, JSON, and retention.
- [docs/remote-access.md](docs/remote-access.md) — `pluto device` and ssh
  recipes.
- [docs/VISION.md](docs/VISION.md) — the claim and the non-claims.
- [docs/testing.md](docs/testing.md) — gofmt/vet/unit tests.
- [docs/releasing.md](docs/releasing.md) — how a tagged release is cut.
- [docs/dependency-updates.md](docs/dependency-updates.md) — self-hosted
  Renovate.
