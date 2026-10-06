# Research: declarative project packaging for boxes (#69)

**Date:** 2026-10-06
**Parent:** [#66 — M3: platform refresh](https://github.com/Siddhj2206/pluto/issues/66)
**Ticket:** [#69 — Research: declarative project packaging for boxes](https://github.com/Siddhj2206/pluto/issues/69)

## Goal

Recommend a declarative shape for declaring a project's tools and dependencies in a
box, so users do not hand-write `provision` commands. The shape must:

- Compose with the existing `[provision]` phase (ADR 0007), not replace it.
- Stay agent-agnostic (no tie to any specific agent or tool).
- Work on Ubuntu/apt (the M3 platform refresh base).
- Fit in `.pluto.toml` (TOML), parse through `internal/contract`, and generate
  into `pluto.schema.json`.

## Approaches compared

### 1. mise (`mise.toml`)

**Shape:** TOML with a `[tools]` table. Each key is a tool name; the value is a
version request string or an inline table of options.

```toml
[tools]
node = "24"
python = "3.13"
```

**How it works:** `mise install` resolves version requests via backends (asdf
plugins, vfox, aqua, GitHub, HTTP, etc.), installs tools into a per-project
directory, and puts them on `PATH`. `mise.lock` records resolved versions.
`mise exec -- <cmd>` runs a command with the project's tools active
([mise docs](https://mise.jdx.dev/dev-tools/), accessed 2026-10-06).

**Strengths:**
- TOML-native, same format family as `.pluto.toml`.
- Version requests ("24", "latest", "ref:master") are concise.
- `mise.lock` gives reproducibility without replacing version requests.
- Agent-agnostic: any process can use `mise exec` or activate the environment.
- Large registry of pre-configured tool backends.

**Weaknesses for pluto:**
- Requires mise to be installed in the box (or on the host and driven over vsock).
- Tool installation is dynamic: `mise install` fetches from the network at
  provision time, which conflicts with the M3 goal of a reproducible, hash-locked
  image with snapshot-pinned apt.
- Version requests float unless `mise.lock` is committed; resolving them at
  provision time introduces non-determinism.
- Backends are mise-specific; the same tool name can resolve differently across
  mise versions.

### 2. `devcontainer.json`

**Shape:** JSONC with `features` (shareable install units), lifecycle commands
(`onCreateCommand`, `postCreateCommand`, `postStartCommand`), and
`hostRequirements` for resources.

```jsonc
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": {
    "ghcr.io/devcontainers/features/node:1": { "version": "22" }
  },
  "hostRequirements": { "cpus": 4, "memory": "8gb" }
}
```

**How it works:** A supporting tool (VS Code, devcontainer CLI) reads the JSON,
pulls/builds the image, installs features, and runs lifecycle commands inside the
container ([devcontainer spec](https://devcontainers.github.io/implementors/spec/),
[metadata reference](https://devcontainers.github.io/implementors/json_reference/),
accessed 2026-10-06).

**Strengths:**
- `features` are a declarative, shareable way to install tools.
- Lifecycle commands map cleanly onto provision/wake semantics.
- `hostRequirements` is a declarative resource declaration.

**Weaknesses for pluto:**
- JSONC, not TOML — a second config format in the repo.
- Tied to the container ecosystem (Docker/OCI); pluto boxes are Firecracker
  microVMs, not containers.
- Features are OCI artifacts (`ghcr.io/...`); pulling them at provision time
  introduces the same non-determinism as mise.
- No apt-native story: features install tools into the container filesystem,
  not via apt.
- The spec is large and tool-specific (`customizations`, `portsAttributes`,
  `userEnvProbe`); most of it is irrelevant to a headless box.

### 3. Nix / flakes (`flake.nix`)

**Shape:** Nix language. A `flake.nix` declares `inputs` (dependencies) and
`outputs` (packages, devShells). `nix develop` starts a shell with the dev
environment; `flake.lock` pins all inputs.

```nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }: {
    devShells.x86_64-linux.default = nixpkgs.legacyPackages.x86_64-linux.mkShell {
      packages = with nixpkgs.legacyPackages.x86_64-linux; [ nodejs_22 python313 ];
    };
  };
}
```

**How it works:** Nix evaluates the flake, builds/fetches dependencies into the
content-addressed Nix store (`/nix/store`), and `nix develop` starts a shell
with those packages on `PATH` ([nix manual](https://nixos.org/manual/nix/stable/command-ref/new-cli/nix3-develop),
[nix flake manual](https://nixos.org/manual/nix/stable/command-ref/new-cli/nix3-flake),
accessed 2026-10-06).

**Strengths:**
- Fully reproducible: `flake.lock` pins every input by hash.
- Content-addressed store means identical inputs yield identical outputs.
- `devShells` is a declarative, composable shape.
- Works on any Linux distro (including Ubuntu) without being NixOS.

**Weaknesses for pluto:**
- Nix language is a full programming language — far more expressive than needed
  for "install these apt packages."
- Requires Nix to be installed in the box (multi-user or single-user install).
- The Nix store is large (hundreds of MB even for small environments) and
  lives outside the apt filesystem, complicating the image model.
- `nix develop` starts an interactive shell; driving it from a provision hook
  is awkward.
- Overkill for the M3 goal: the platform refresh chose Ubuntu/apt specifically
  to keep the packaging story familiar.

### 4. devbox (`devbox.json`)

**Shape:** JSON with a `packages` array (Nix package names with optional
versions), `env`, `shell.init_hook`, and `shell.scripts`.

```json
{
  "packages": ["nodejs@22", "python@3.13"],
  "env": { "NODE_ENV": "development" },
  "shell": {
    "init_hook": ["corepack enable"],
    "scripts": { "dev": "node server.js" }
  }
}
```

**How it works:** `devbox install` resolves packages via Nix, installs them
into an isolated profile, and `devbox shell` or `devbox run` activates the
environment. `devbox.lock` pins resolved versions
([devbox docs](https://www.jetify.com/docs/devbox/configuration/index.md),
[devbox GitHub](https://github.com/jetify-com/devbox), accessed 2026-10-06).

**Strengths:**
- Simpler than raw Nix: `packages` is a flat list, no Nix language needed.
- `devbox generate devcontainer` can emit a `devcontainer.json` for VS Code
  users, giving one declaration two surfaces.
- Plugins provide per-package setup (e.g., `postinstall` equivalents).
- Works on Linux and macOS.

**Weaknesses for pluto:**
- JSON, not TOML.
- Built on Nix: same store-size and Nix-dependency concerns as raw Nix.
- `devbox shell` is interactive; `devbox run` is the non-interactive path but
  still requires the devbox CLI in the box.
- Packages come from Nixpkgs, not apt: the same tool may have a different
  version or patch level than the apt package, which can confuse users who
  expect `apt` semantics.
- `devbox.lock` is Nix-specific; reproducibility depends on the Nixpkgs
  channel staying available.

### 5. Plain `provision` (status quo)

**Shape:** A `[provision]` section in `.pluto.toml` with a `command` (string
or argv array), optional `dir`, `env`, and `timeout`.

```toml
[provision]
command = "apt-get update && apt-get install -y build-essential curl"
timeout = "20m"
```

**How it works:** The daemon parses the contract on the host, sends it to the
guest agent, and the agent runs the command once per box via a systemd user
unit. A failed provision still boots the box, marked failed
([docs/contract.md](../../contract.md), ADR 0007).

**Strengths:**
- Simple, explicit, and already shipped.
- No external tooling: uses the box's own shell and apt.
- Composes with the box contract's hash-staleness tracking.
- Agent-agnostic: the command is just a shell command.

**Weaknesses:**
- Imperative: the user hand-writes the install command.
- No version pinning: `apt-get install -y nodejs` installs whatever apt
  resolves, which can drift.
- No reproducibility guarantee: the same `.pluto.toml` can produce different
  boxes over time as apt indexes update.
- No declarative query: `pluto status` cannot report "this box needs Node 22."

## Recommended contract shape

### Proposal: a `[tools]` section in `.pluto.toml`

Add an optional `[tools]` table to the contract. It declares what the project
needs; the provision phase installs them. The two compose: `[tools]` is the
declarative "what," `[provision]` is the imperative "how" for anything that
cannot be declared.

```toml
[tools]
# apt packages to install during provision (names only, or name with version)
packages = ["build-essential", "curl", "git", "pkg-config"]

# Optional: pin exact versions (apt pinning)
# packages = [
#   { name = "nodejs", version = "22.11.0" },
# ]

[provision]
command = "make setup"
timeout = "20m"
```

**Semantics:**

1. **`[tools] packages`** is a list of apt package names (strings) or tables
   with `name` and optional `version`. When present, the contract generates a
   provision preamble that runs `apt-get update && apt-get install -y <packages>`
   before the user's `[provision] command`.
2. **`[provision]` is unchanged.** If both sections are present, tools are
   installed first, then the provision command runs. If only `[provision]` is
   present, behavior is identical to today. If only `[tools]` is present, the
   generated apt install is the entire provision.
3. **No external tooling required.** The box uses its own apt. No mise, Nix,
   devbox, or devcontainer CLI is needed in the box.
4. **Agent-agnostic.** The declaration is data; any agent or human can read it
   and know what the box contains.
5. **Schema-generated.** The new section is a Go struct in `internal/contract`
   with `toml` tags, so `pluto.schema.json` picks it up via `go generate`.

**Why this shape:**

- **TOML-native.** Same format as the rest of `.pluto.toml`; no second config
  language.
- **apt-native.** Uses the box's own package manager, matching the M3 choice of
  Ubuntu 24.04 and the reproducibility goal (snapshot-pinned apt indexes).
- **Composes with provision.** The user can declare packages and still hand-write
  a provision command for anything that is not an apt package.
- **Minimal surface.** One key (`packages`) covers the common case; version
  pinning is an optional extension.
- **No new dependencies.** No mise, Nix, or devbox in the box; no OCI pulls at
  provision time.
- **Fits the existing parser.** `internal/contract` already rejects unknown
  keys, so a typo in `[tools]` fails at load time, not at boot.

**What this shape does not do (yet):**

- No version resolution or floating-version semantics (no "install latest 22.x").
  Apt pinning is exact-version only.
- No non-apt tools (e.g., `cargo install`, `npm install -g`). Those stay in
  `[provision]`.
- No per-tool `postinstall` hooks (mise's `postinstall`, devbox's
  `init_hook`). Those stay in `[provision]`.
- No tool-level `depends` ordering. Apt handles dependency resolution.

### Alternative considered: `[tools]` with mise-style version requests

```toml
[tools]
node = "22"
python = "3.13"
```

This was rejected because it implies mise-style version resolution (backends,
`mise.lock`, floating versions), which conflicts with the M3 reproducibility
goal and requires mise in the box. If the project later wants mise-style tool
management, it can be added as a separate `[mise]` section or a `backend` key
on `[tools]`, but the initial shape should be apt-native.

## Migration / compatibility stance

**Existing `.pluto.toml` files continue to work unchanged.**

- The `[tools]` section is optional. A contract without it parses exactly as
  before.
- The `[provision]` section is unchanged. Its `command`, `dir`, `env`, and
  `timeout` keys keep their current semantics.
- The contract hash (`internal/contract/hash.go`) covers the parsed values,
  so adding `[tools]` to an existing contract changes the hash and `pluto
  status` reports staleness — the same behavior as any other contract edit.
- No existing key is renamed, removed, or retyped.

**Adoption path:**

1. Land the `[tools]` section in `internal/contract` and regenerate
   `pluto.schema.json`.
2. Users add `packages = [...]` to their `.pluto.toml` and (optionally) move
   their `apt-get install` line out of `[provision] command`.
3. Existing contracts with `apt-get install` in `[provision] command` keep
   working; there is no deprecation or forced migration.

**Future extensions (not in this change):**

- `version` pinning on packages (exact apt versions).
- A `backend` key to opt into mise-style resolution for non-apt tools.
- Per-package `postinstall` commands.

## Uncertainties

1. **apt version pinning syntax.** The exact TOML shape for pinned versions
   (`{ name = "nodejs", version = "22.11.0" }` vs `nodejs = "22.11.0"`) needs
   a concrete proposal and test. The inline-table form is more extensible but
   more verbose; the string form is simpler but less structured.

2. **Interaction with snapshot-pinned apt.** The M3 image builder resolves apt
   against a dated `snapshot.ubuntu.com` index. If `[tools]` declares a package
   version that is not in the snapshot, provision fails. The error message
   should be clear, but the exact behavior (fail vs. warn vs. fall back to the
   snapshot version) is undecided.

3. **Idempotency.** `apt-get install -y <packages>` is idempotent (installing
   an already-installed package is a no-op), but `apt-get update` refreshes
   the index. If the box is re-provisioned (e.g., after a contract change),
   the index may have moved. Whether to run `apt-get update` on every
   provision or only on first boot is undecided.

4. **Non-apt tools.** Some projects need tools that are not in apt (e.g.,
   `rustup`, `cargo install`). The recommended shape does not cover these;
   they stay in `[provision]`. Whether a future `[tools]` extension should
   cover them (and how) is out of scope for this note.

5. **devcontainer.json compatibility.** ADR 0007 mentions devcontainer
   compatibility as a future translation layer. This note does not propose one;
   if a translation is added later, `[tools] packages` maps naturally to
   `features` or `onCreateCommand`, but the exact mapping is undecided.

6. **mise as an optional backend.** If the project later wants mise-style tool
   management (e.g., for tools not in apt), the `[tools]` section could grow a
   `backend = "mise"` key. Whether this is desirable, and how it interacts
   with the apt-native default, is undecided.

## Sources

| Source | URL | Accessed |
|---|---|---|
| mise dev tools | https://mise.jdx.dev/dev-tools/ | 2026-10-06 |
| mise configuration | https://mise.jdx.dev/configuration.html | 2026-10-06 |
| devcontainer.json metadata reference | https://devcontainers.github.io/implementors/json_reference/ | 2026-10-06 |
| devcontainer spec | https://devcontainers.github.io/implementors/spec/ | 2026-10-06 |
| Nix `nix develop` manual | https://nixos.org/manual/nix/stable/command-ref/new-cli/nix3-develop | 2026-10-06 |
| Nix `nix flake` manual | https://nixos.org/manual/nix/stable/command-ref/new-cli/nix3-flake | 2026-10-06 |
| devbox docs | https://www.jetify.com/docs/devbox/configuration/index.md | 2026-10-06 |
| devbox GitHub repo | https://github.com/jetify-com/devbox | 2026-10-06 |
| pluto ADR 0007 (box contract) | docs/adr/0007-box-contract.md (repo) | 2026-10-06 |
| pluto contract reference | docs/contract.md (repo) | 2026-10-06 |
| pluto contract parser | internal/contract/contract.go (repo) | 2026-10-06 |
| pluto schema generator | internal/schema/schema.go (repo) | 2026-10-06 |
