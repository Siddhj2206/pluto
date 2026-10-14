# The `pluto.yml` contract

A project declares its box in `pluto.yml`, the single entry point for box
settings and durable work (#130, #139). It is the only supported Pluto
contract; this document describes the whole of it.

Editors can point at the generated schema:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Siddhj2206/pluto/master/pluto.yml.schema.json
```

## Shape

```yaml
version: 1

provision:
  base: ubuntu:24.04              # OCI ref | { dockerfile: ./x } | { run: "..." }
  packages: [build-essential, nodejs]
  watch: [Cargo.lock, pnpm-lock.yaml]   # hashed into the package-layer key
  run: |                                # optional, on top of base + packages
    corepack enable

wake: ./.pluto/wake.yml            # inline: { run: "..." }, or a typed phase file

docker: false                     # box may run its own container engine

secrets:                          # declared references; auto-registered with the masker
  REGISTRY_TOKEN: { env: REGISTRY_TOKEN }
mask: ["ghp_", "AKIA"]            # extra literals/prefixes

tasks:
  dev:   { run: ./scripts/dev.sh,              lock: shared }
  build: { run: ["./scripts/build.sh", "make test"], lock: exclusive }
  db:    { service: true, run: "postgres -D /workspace/.pg" }

schedules:
  nightly: { task: build, cron: "0 3 * * *" }
```

## Typed file references

Any of the nodes `provision`, `wake`, and a task's work may be written inline
or as a path to a typed YAML file with the same shape. A reference is a bare
string where the node's value would otherwise be a mapping; the path resolves
against the directory of the file that names it. By convention these files live
under `.pluto/` in the repo (`.pluto/provision.yml`, `.pluto/wake.yml`, …), so
the contract root stays uncluttered.

```yaml
# pluto.yml
provision: ./.pluto/provision.yml
wake: ./.pluto/wake.yml
tasks:
  dev: ./.pluto/tasks/dev.yml
```

```yaml
# .pluto/provision.yml
base: { dockerfile: ./.pluto/Dockerfile }
packages: [nodejs]
```

One schema validates every form. Inline and referenced contracts compile to one
internal execution model (`contract.Project`), and a contract written inline
and the same contract split across files hash identically.

A reference always means a mapping. A bare string at `tasks.<name>` is a file
path, never a command shorthand; put the command in the referenced file's
`run`, or inline the task as a mapping.

## Fields

- **`version`** — required, must be `1`.
- **`provision`** — how the box's layers are built (once per content change).
  - **`base`** — required. An OCI reference such as `ubuntu:24.04`; or
    `{ dockerfile: <path> }`; or `{ run: <command> }` inline steps.
  - **`packages`** — project packages layered on top of the base.
  - **`watch`** — lockfiles whose contents key the package layer.
  - **`run`** — optional command run on top of base + packages.
- **`wake`** — the phase that runs on every box start. `{ run: <command> }`. Keep
  it short and idempotent.
- **`docker`** — `true` lets the box run its own container engine, isolated from
  the host's.
- **`secrets`** — named references, each `{ env: <HOST_VAR> }` or
  `{ file: <path> }`. Resolved values are registered with the masker.
  Names are valid environment keys and may not start with `PLUTO_`.
- **`mask`** — extra literal prefixes (tokens, key prefixes) masked from output.
  A prefix is redacted together with the run of token characters that follows
  it — ASCII letters, digits, `_`, and `-` — so `mask: ["ghp_"]` hides
  `ghp_abc123` whole. A `secrets:` value is redacted literally instead.
- **`tasks`** — named, durable work. Each task is `{ run: <command> }` plus:
  - **`lock`** — `shared` (default) or `exclusive`. `shared` runs alongside other
    shared work and attach sessions; `exclusive` runs alone and waits for shared
    work to drain (ADR 0018).
  - **`service`** — `true` for a long-lived task that stays running for the box's
    life and is restarted on every wake. `service` and `lock` are mutually
    exclusive.
- **`schedules`** — recurring triggers. Each names a declared `task` and a
  five-field `cron` expression (minute resolution, UTC). Occurrences coalesce
  into a single pending run.

A **command** is either a string, run via `/bin/sh -c`, or an array of strings,
exec'd directly.

## Errors

A contract that fails to load is an error before any box boots. Errors name the
offending field (`pluto.yml: tasks.build.lock: "solo" is not shared or
exclusive`) or the referenced file, including when a reference is missing
(`pluto.yml:3: provision: open .../provision.yml: no such file or directory`)
or malformed (`provision.yml: line 2: ...`).

## Scaffolding

`pluto init` writes a minimal `pluto.yml` that loads and validates, and never
overwrites an existing contract.
