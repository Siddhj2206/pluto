# The box contract lives in the repo (`.pluto.toml`)

Every box is configured by a repo-committed `.pluto.toml`. It separates expensive, once-per-box work from cheap, per-wake repair, declares long-lived services, and declares schedules:

```toml
[box]
image = "ubuntu-24.04"              # base artifact
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }

[provision]                          # once per box; result is the durable disk
command = ".pluto/provision.sh"
timeout = "20m"

[wake]                               # every boot; short and idempotent
command = ".pluto/wake.sh"
timeout = "30s"

[services.dev]                       # supervised; restarted every wake
command = "pnpm dev"
port = 3000

[[schedule]]                         # M1: wake + optional job
name = "nightly"
cron = "0 2 * * *"
command = "pnpm test"
```

The rules that make it work:

- **provision** runs once per box, may install anything, and its leftover processes are discarded — anything long-lived must be a **service**.
- **wake** runs on every start, is timeboxed, and never installs; a failing or slow wake does not block the box and is surfaced in `pluto status` / `pluto logs`.
- A failed provision still boots the box, marked failed, so the machine itself is the debugging surface.
- Hooks run as the box user in the worktree; agents are not special — an agent is a job (`pluto run -- opencode …`) or a service.
- Schedules follow the durable-alarm semantics in ADR 0003; they land in M1.

This is the prepaid-setup contract that makes "zero setup" real: expensive work happens once and lives on the disk, while waking a box stays cheap.

## Considered options

- **devcontainer.json only**: container semantics without a boot/resume split; mapped later as a compatibility layer instead.
- **Per-box config on the daemon**: not versioned with the code, lost on `git clone`.
- **cloud-init / NoCloud user-data**: provisioning infrastructure, not a repo contract.

## Consequences

- Repos adopt one new file; devcontainer compatibility becomes a translation, not a requirement.
- The contract is cheap to parse but is not a build system; image building stays outside it.
