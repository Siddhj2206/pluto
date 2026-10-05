# The box contract lives in the repo (`.pluto.toml`)

Every box is configured by a repo-committed `.pluto.toml`. It separates expensive, once-per-box work from cheap, per-wake repair, declares long-lived services, names the work a box can run, and schedules recurring runs:

```toml
[box]
image = "ubuntu-24.04"
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }
auto_pause = "1h"

[env]
NODE_ENV = "development"

[provision]
command = ".pluto/provision.sh"
timeout = "20m"

[wake]
command = ".pluto/wake.sh"
timeout = "30s"

[jobs.dev]
description = "start the dev server"
command = ["pnpm", "dev"]
dir = "web"

[jobs.test]
description = "run the test suite"
command = "pnpm test"
timeout = "30m"

[services.web]
description = "web UI"
command = "pnpm dev"
port = 3000

[[schedule]]
name = "nightly"
cron = "0 2 * * *"
job = "test"
```

The rules that make it work:

- **provision** runs once per box, may install anything, and its leftover processes are discarded — anything long-lived must be a **service**.
- **wake** runs on every start, is timeboxed, and never installs; a failing or slow wake does not block the box and is surfaced in `pluto status` / `pluto logs`.
- **auto_pause** is the idle window (default 1h, per-box override, `"off"` disables): a box pauses once no client is attached and no job is running for that long. The daemon reads the live session count from the agent; it never pauses on a guess.
- **jobs** are named, bounded commands declared under `[jobs.<name>]`. No-arg `pluto run` lists them with their descriptions, `pluto run <name>` runs one, and a schedule references one by name. Names match the existing service rule, `^[A-Za-z0-9][A-Za-z0-9_-]*$`. `pluto jobs` stays the run-history command; `[jobs.<name>]` declares what can run.
- **env** is flat: `KEY = "value"` strings only. A top-level `[env]` applies to provision, wake, services, jobs, and ad-hoc `pluto run --`; every entity may carry its own `env` table, merged per key over the top level. There is no interpolation — a shell-string command gets the values through the shell, an argv command receives them literally. `PLUTO_` is reserved; M1 sets `PLUTO_WORKTREE` inside the box and no other built-ins.
- **command** takes two shapes on every entity: a string runs via `/bin/sh -c`, an array is exec'd directly with no shell. The key is always `command` (not mise's `run`). Commands that reference repo files must point at committed files, because those are what the box gets (ADR 0008).
- **dir** is optional on every entity and defaults to the in-box worktree root; a relative value resolves against it.
- **description** is optional on `[jobs.<name>]` and `[services.<name>]` only: a job's description appears in the no-arg `pluto run` listing and in unknown-job errors, a service's appends to its `pluto status` row. Schedules and phases skip it until they have a surface.
- **timeout** is optional on `[jobs.<name>]` and unlimited by default, enforced with the existing unit timeout (`TimeoutStartSec`); a trip records a failed job.
- **schedules** follow the durable-alarm semantics in ADR 0003: `[[schedule]]` entries carry `name`, `cron`, and `job` naming a declared job; a schedule without `job` is a warm-up that only ensures the box is running. Schedules never carry inline commands.
- A failed provision still boots the box, marked failed, so the machine itself is the debugging surface.
- Hooks run as the box user in the worktree; agents are not special — an agent is a declared job, an ad-hoc run (`pluto run -- opencode …`), or a service.

The schema is generated, not hand-written: `pluto.schema.json` at the repo root is derived from the `internal/contract` Go types with a checked-in generation command, committed as an artifact, and verified by a CI step that fails when regeneration leaves the tree dirty. Editors find it with `#:schema https://raw.githubusercontent.com/Siddhj2206/pluto/main/pluto.schema.json`. The artifact lands with the v2 implementation (#36).

This is the prepaid-setup contract that makes "zero setup" real: expensive work happens once and lives on the disk, while waking a box stays cheap.

## Considered options

- **devcontainer.json only**: container semantics without a boot/resume split; mapped later as a compatibility layer instead.
- **Per-box config on the daemon**: not versioned with the code, lost on `git clone`.
- **cloud-init / NoCloud user-data**: provisioning infrastructure, not a repo contract.
- **mise-style `[tasks]`**: the project already calls bounded, recorded work a job (`pluto jobs`, job history); a second noun would split one vocabulary.
- **`[[jobs]]` array entries**: a map gives every job a unique name to reference from `pluto run` and `[[schedule]] job =`, and duplicate names fail at parse time.
- **Structured `[env]` entries** (values with metadata): no M1 consumer; flat strings keep the file readable and the per-key merge rule trivial.
- **String-only commands**: an argv array avoids a shell layer and its quoting bugs whenever a caller does not need one, so both shapes earn their keep.
- **A hand-written JSON schema**: drift between the schema and `internal/contract` is inevitable; generation from the Go types keeps one source of truth.

## Consequences

- Repos adopt one new file; devcontainer compatibility becomes a translation, not a requirement.
- The contract is cheap to parse but is not a build system; image building stays outside it.
- Job names become shared vocabulary: `pluto run <name>`, `[[schedule]] job =`, and job history all speak the same names.
- Contract staleness follows the parsed values, not the bytes: the box records a hash of the contract it applied at handoff, and `pluto status` flags a worktree whose contract now parses to anything else. Comments, whitespace, and key order never trigger it; reordering repeated sections, or respelling a value (`"1h"` vs `"60m"`), does.
- The schema cannot drift from the parser without CI failing; the checked-in artifact is the editor-facing half.
- `env` values are literal and merged per key, so one value cannot reference another; interpolation can be added by decision when a use appears.
- M1 sets only `PLUTO_WORKTREE`; the reserved `PLUTO_` prefix leaves room for built-ins without claiming the user's namespace.
