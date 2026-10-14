# Developer experience and comparables — patterns for a pluto-grade DX

Fetched 2026-10-09. Primary sources only: each tool's official docs, CLI reference, repo
README/release notes, design docs, and first-party benchmark pages. Quotes are short and
verbatim; each substantive claim carries its link. Evidence is tagged:

- **[docs]** — documented behaviour on the vendor's own pages.
- **[vendor claim]** — the vendor's own marketing/benchmark claim, not independently verified.
- **[repo]** — source or README.
- **[inference]** — this author's reading, not a vendor statement.

This document is the **DX** companion to `docs/research/cli-comparables.md` (#60). That note
answered *"what grammar do the best CLIs use?"* and fed ADR 0011 and `docs/m5-cli-overhaul.md`;
it is not repeated here. This note answers *"what makes the whole experience good — day one,
team, enterprise — and what should pluto copy?"* Where the CLI-comparables ground is relevant
(one binary, verb-plus-noun grammar, `--json`, prefix ids), it is referenced, not re-litigated.

pluto's aim, per `docs/VISION.md`, is "a mise-grade pass over the CLI: grammar, command
taxonomy, help, output, and a review surface." This note argues that mise's *DX* — not only its
grammar — is the model, and names the patterns that separate beloved tools from tolerated ones.

Vendor-reported 2026 state is flagged where it looks stale: **Earthly is no longer actively
maintained**, **BuildJet for GitHub Actions has shut down**, and **Gitpod has been replaced by
Ona, which has closed new-customer signup**. Those three are still catalogued because pluto can
learn from how they fell, but they are not live comparables.

---

## 1. Landscape map (2026)

Grouped by category, with the leader and what each is best at. This is the map the rest of the
document draws patterns from.

### 1.1 Dev environments / remote dev

| Tool | What it is best at | State (2026) |
|---|---|---|
| **Dev Containers spec** (`containers.dev`) | The portable *standard* for a dev environment: one `devcontainer.json` that "deterministically creates containers under the control of the user". | Live; the interop lingua franca. |
| **VS Code Dev Containers** | Turning that standard into a one-command, editor-native experience, with a recovery path when the build fails. | Live. |
| **GitHub Codespaces** | Zero-install first run: a devcontainer-shaped environment "hosted by GitHub" reachable from browser, VS Code, or `gh`. | Live, account-walled. |
| **Coder** | Self-hosted, enterprise-grade environments "defined with Terraform" and governed by admins. | Live; now also self-hosted agents. |
| **Daytona** | Agent sandboxes with VM/GPU/macOS options and snapshot/lifecycle APIs; `<90ms` dynamic builds. | Live; pivoted to agent sandboxes. |
| **DevPod** | Client-only, no backend: the devcontainer experience on your laptop or any remote/cloud machine. | Live (Loft Labs). |
| **Okteto** | "Real environments" for agents and developers on Kubernetes, with preview environments and a manifest. | Live; agentic pivot. |
| **Ona** (formerly Gitpod) | Background agents in reproducible Dev Container environments, with policy/SSO. | **New signup closed**; docs point to OpenAI Codex. |
| **JetBrains Gateway** | A thin client that connects JetBrains IDEs to a remote host. | Live; cited here via Coder/Ona IDE lists. |

### 1.2 Tool / task / env config and runners

| Tool | What it is best at |
|---|---|
| **mise** | One `mise.toml` for tools, env, and tasks — plus `mise doctor`. pluto's stated inspiration. |
| **Jetify Devbox** | A `devbox.json` isolated shell that "can create isolated environments right on your laptop, without an extra-layer of virtualization", portable to a devcontainer/Dockerfile. |
| **Flox** | Nix-grade reproducibility with a human CLI: a manifest, `flox activate`, services, `flox containerize`. |
| **Nix devShells** | Maximal reproducibility and composition, at the cost of a language to learn. |
| **just** | A humane command runner: "Errors are specific and informative, and syntax errors are reported along with their source context." |
| **Task** | Cross-platform YAML task runner with `task --list`, preconditions, and shareable remote Taskfiles. |
| **Make** | The universal baseline; a build system, not a task runner, with sharp edges. |
| **direnv** | Per-directory environment loading with an explicit trust gate: `.envrc` is "not allowed" until you `direnv allow .`. |

### 1.3 CI runners and build orchestration

| Tool | What it is best at |
|---|---|
| **GitHub Actions** | The ecosystem and the declarative grammar; workflow templates suggested from repo contents. |
| **Blacksmith** | "The fastest way to run your GitHub Actions. Change one line." Plus caching, CLI, SSH, test analytics. |
| **Depot** | Fast CI, container builds, runners, registry, and now agent sandboxes; data plane in your AWS. |
| **Namespace** | "Fast, observant drop-in replacement for GitHub Runners", plus managed Linux/macOS devboxes. |
| **RunsOn** | Self-hosted GitHub runners in *your* AWS account, one CloudFormation stack, "the label is a query". |
| **BuildJet** | (Was) "Change 1 line, get 2x faster and cheaper builds". | **Shut down** (banner: "BuildJet for GitHub Actions has been shut down"). |
| **Earthly** | Dockerfile+Makefile hybrid, repeatable builds, shared cache. | **No longer actively maintained.** |
| **Dagger** | Pipelines as real code with a portable DAG, caching, and tracing; "1.0-beta". |
| **act** | "Run your GitHub Actions locally" for fast feedback and local repro. |

### 1.4 Self-hosted CI

| Tool | What it is best at |
|---|---|
| **Gitea Actions** | GitHub-Actions-compatible CI built into the forge since 1.19; runner is a fork of `nektos/act`. |
| **Forgejo Actions** | The same idea in Forgejo (a Gitea hard fork), with timezone-aware schedules and OIDC. |
| **Woodpecker** | Lightweight "server + agent (+ autoscaler)"; SQLite by default; `woodpecker-cli exec` for local runs. |
| **Drone** | Harness-owned CI with Community Edition; provider-per-forge installs. | Legacy; largely superseded. |
| **Concourse** | "An open-source continuous thing-doer": resources/tasks/jobs, `fly` CLI, `fly intercept`, `fly execute`. |

### 1.5 Caching / build acceleration

| Tool | What it is best at |
|---|---|
| **Turborepo** | A "single `turbo.json`"; free remote cache, managed (Vercel) or self-hosted via an OpenAPI spec. |
| **Nx** | Local + remote cache, affected graph, distributed execution; `npx nx@latest connect`. |
| **Bazel** | Content-addressed remote cache with a dead-simple HTTP protocol (`/ac/`, `/cas/`); explicit read/write policy. |
| **sccache** | Compiler wrapper with local and many remote backends (S3, R2, Redis, GCS, Azure, GHA, WebDAV). |
| **ccache** | The classic C/C++ cache; multi-level, BLAKE3, statistics. |
| **Remote cache services** | Depot cache, Nx Cloud, and Bazel providers (BuildBuddy, EngFlow): shared cache as a managed/self-hosted product. |

### 1.6 Agent sandboxes / offload

| Tool | What it is best at |
|---|---|
| **E2B** | "Isolated sandboxes that let agents safely execute code"; pause saves filesystem *and memory*, kept indefinitely. |
| **Modal** | "Zero configuration … everything … is code. … no YAML in it."; `modal run file.py`. |
| **Vercel Sandbox** | Firecracker microVMs, "start in milliseconds", persistent by default, Active CPU pricing. |
| **Cloudflare Sandbox SDK** | Sandboxes inside a Worker: microVM containers or Dynamic Workers, network mediated by the Worker. |
| **Morph** | "Infinibranch": snapshot/branch/restore entire VMs <250ms; devboxes with wake-on-SSH/HTTP and tmux automation. |
| **Fly Machines** | "fast-launching VMs; they can be started and stopped at subsecond speeds"; REST API + `flyctl`. |
| **Northflank** | Services, jobs, sandboxes, GPUs, BYOC; publishes agent "Skills" and `llms.txt`. |

---

## 2. DX dimensions, with evidence

### 2.1 Time to first value

The strongest tools get a user to a *working environment* in one or two commands, and often
without an account.

- **E2B**: `npm i e2b` / `pip install e2b`, then `Sandbox.create()` and `commands.run(...)` "in a
  few lines" ([docs](https://e2b.dev/docs)). The only gate is `E2B_API_KEY`.
- **Modal**: `pip install modal`, `modal setup`, then `modal run path/to/file.py`. The docs'
  pitch is explicit: "there's zero configuration in Modal - everything … is code" ([docs](https://modal.com/docs/guide)).
- **Vercel Sandbox**: a one-liner `sandbox run echo "Hello Sandbox!"` ([docs](https://vercel.com/docs/vercel-sandbox)).
- **mise**: `curl -fsSL https://mise.run | sh`, then `mise exec node@24 -- node --version`;
  project setup is `mise use node@24` ([docs](https://mise.jdx.dev/getting-started.html)).
- **Flox**: `curl -fsSL https://get.flox.dev | sh`; "simplify the setup instructions in your
  README to a single command (`flox activate`)" ([docs](https://flox.dev/docs/)).
- **Namespace devboxes**: `curl -fsSL get.namespace.so/devbox/install.sh | bash`, `devbox login`,
  `devbox create --checkout github.com/you/repo`, `devbox session connect --session agent`
  ([docs](https://namespace.so/docs/devbox)).
- **Dev Containers**: not one command, but the editor path is short — open folder, pick a
  template, reload; "You only have to build a dev container the first time you open it; opening
  the folder after the first successful build will be much quicker"
  ([docs](https://code.visualstudio.com/docs/devcontainers/containers)).
- **pluto today**: the README quickstart is seven numbered steps and three prerequisites (KVM,
  rootless podman, e2fsprogs), an image build, `pluto install`, `pluto image import`, then a
  hand-written `.pluto.toml`. That is honest but long. **[inference]** Day-one value is pluto's
  single biggest DX gap.

### 2.2 Install and update

- One-liner installers are the norm: mise `mise.run`, Flox `get.flox.dev`, DevPod/Namespace
  `install.sh`; package managers are the fallback ("Homebrew, apt, dnf and other package
  managers" for [mise](https://mise.jdx.dev/getting-started.html); ccache/sccache are widely
  packaged).
- **Self-update** is a first-class command in mise (`self-update`) per the CLI-comparables
  inventory; `mise version` supports subcommand, `--version`, `-v`, `-V`
  ([cli-comparables §1](cli-comparables.md)).
- **Pinning**: mise records exact versions with `mise lock` ("to record exact versions, add a
  lockfile with `mise lock`") ([docs](https://mise.jdx.dev/getting-started.html)); dev containers
  pin via base images and Features; Turborepo treats cache artifacts as immutable.
- **The runner vendors make install the hook**: Blacksmith and BuildJet (when live) sold "change
  one line" (`runs-on: blacksmith-4vcpu-ubuntu-2404`) and RunsOn sells "one CloudFormation stack,
  ~10 min" ([Blacksmith](https://docs.blacksmith.sh/), [RunsOn](https://runs-on.com/)).
- **pluto today** installs via `go install …@v0.1.5` or a release tarball, and the guest
  agent/vsock helper are baked into the image ([README](../../README.md)). **[inference]** A
  `curl | sh` installer plus a checksum/self-update story is expected table stakes in 2026.

### 2.3 Configuration surface

- **One obvious file** wins. `devcontainer.json`, `mise.toml`, `devbox.json`, `turbo.json`,
  `.pluto.toml` are all single committed files.
- **Convention over configuration**: Turborepo "uses the `package.json` scripts you've already
  written … and a single `turbo.json`" ([docs](https://turborepo.com/docs)); Task reads
  `Taskfile.yml`; just reads `justfile`.
- **Templates/scaffolding beat blank files**: VS Code's "Add Dev Container Configuration Files…"
  lets you "pick a pre-defined container configuration from a list based on your folder's
  contents" ([docs](https://code.visualstudio.com/docs/devcontainers/containers)); GitHub
  Actions "analyzes your code and shows you workflow templates that might be useful"
  ([docs](https://docs.github.com/en/actions/quickstart)).
- **Discoverability inside the file**: dev containers give IntelliSense while editing
  `devcontainer.json` and a JSON schema; pluto already ships `pluto.schema.json` and a generated
  JSON schema for editors ([README](../../README.md)).
- **Progressive disclosure**: mise's getting-started is deliberately staged — "Steps 1 to 3 work
  without changing your shell configuration; step 4 adds optional shell activation"
  ([docs](https://mise.jdx.dev/getting-started.html)). A user learns `mise exec`, then `use`, then
  tasks, then activation.
- **Executable config needs a trust gate**: mise "loads it only once it is trusted" (`mise trust`,
  [docs](https://mise.jdx.dev/getting-started.html)); direnv prints ".envrc is not allowed" until
  `direnv allow .` ([docs](https://direnv.net/)); VS Code applies Workspace Trust to dev
  containers ([docs](https://code.visualstudio.com/docs/devcontainers/containers)).
- **pluto's `.pluto.toml`** is already "one obvious file", versioned with the code and hashed when
  applied (ADR 0007, [pitch](../pitch.md)). It is TOML, small, and has a schema. **[inference]**
  The missing piece is the scaffold/`init` and the in-editor examples, not the format.

### 2.4 Onboarding a teammate or second machine

- **Git is the distribution channel.** mise: "Commit `mise.toml`. Teammates and CI run
  `mise install` in the project to get the same tools"
  ([docs](https://mise.jdx.dev/getting-started.html)). Flox: the manifest is "checked into version
  control". dev containers: commit `devcontainer.json` and everyone gets the same environment
  ([docs](https://containers.dev/implementors/spec/)).
- **Explicit "join" command**: Namespace's `--checkout github.com/org/repo`; Concourse's
  `fly -t ci set-pipeline`; Coder's `coder` CLI.
- **Second machine = saved destination.** pluto already has this: a **device** is "a saved ssh
  destination in the client's registry: a nickname such as `neptuno`", used by
  `pluto --device <nickname>` ([GLOSSARY](../../GLOSSARY.md)). This is a genuine DX asset.
- **Credential sync**: DevPod advertises "git & docker credentials sync"; Coder models identity
  through SSO/RBAC; dev containers reuse the host credential manager or share SSH keys.
- **pluto's opportunity**: because the box contract is committed, teammate onboarding is
  `git clone && pluto up`; because of `device`, a second machine is `pluto device add` once.
  **[inference]** The gap is a guided `device add` and a first-run message that names the shared
  host.

### 2.5 Quality of feedback

This is the sharpest differentiator, and the one pluto is already deliberately good at.

- **`doctor` is the pattern.** `mise doctor` "Prints mise's version, whether shell activation and
  shims are set up, its directories, the config files, tools, and environment in use, and its
  settings, then lists any problems and warnings with a suggested fix. Exits with status 1 when
  it finds a problem." It has `--json` ([docs](https://mise.jdx.dev/cli/doctor.html)).
- **Actionable errors with context.** just: "Errors are specific and informative, and syntax
  errors are reported along with their source context"; "Wherever possible, errors are resolved
  statically" ([docs](https://just.systems/man/en/)).
- **A recovery path beats a clean failure.** VS Code offers "Reopen in Recovery Container" when a
  build fails so you can edit and retry
  ([docs](https://code.visualstudio.com/docs/devcontainers/containers)).
- **Preview/validation before harm.** Task preconditions can carry a `msg` ("Install Go to test
  and build this project.") ([docs](https://taskfile.dev/)). Renovate and Gitea validate/parse
  config at deploy, and celld-style systems reject malformed cron before running
  ([trigger-comparables §2](trigger-comparables.md)).
- **pluto is ahead here if it ships it.** ADR 0009 already fixes the `pluto:` / `next:` /
  `warning:` voice, file:line contract errors, and 0/1/2 + pass-through exit codes. **[inference]**
  What is missing is the `doctor` command that turns those facts into a single setup verdict — and
  the CLI has all the facts (KVM, userns, podman, slirp, daemon, image, contract staleness).

### 2.6 Perceived speed and latency

- **Warm vs cold is the story.** Dev Containers: the first build is slow, subsequent opens "much
  quicker". Mise: first `nix-shell`/install "may take a while to download all dependencies"; then
  it is instant. pluto's own model is exactly this — `provision` once per disk, `wake` on every
  start, auto-pause when idle (ADR 0007, [pitch §5.2](../pitch.md)).
- **Numbers are quoted when vendors have them**: Daytona container builds `<90ms`; Vercel Sandbox
  "start in milliseconds"; Fly Machines "subsecond"; Morph snapshot/branch/restore `<250ms`;
  RunsOn cache restores "1.8–2.3× faster" and warm pools "<6 seconds" (all
  [vendor claim] from the linked docs). RunsOn's benchmark page is explicit that it ran and sells
  one of the runners measured ([RunsOn](https://runs-on.com/)).
- **pluto already has the right frames**: M0 reported "time to first shell, pause/wake latency,
  provision duration" ([VISION](../VISION.md)), and the demo asserts the loop end to end. The DX
  work is to *surface* the frame the user cares about: this boot is a wake (seconds) or a
  provision (minutes), and why.

### 2.7 Observability

- **Logs are table stakes**; the differentiators are *attach* and *intercept*. Concourse's
  "`fly intercept` command lets you access a build's containers directly, simplifying
  troubleshooting" ([docs](https://concourse-ci.org/)) — the same instinct as pluto's
  "enter the machine that failed instead of reading logs"
  ([README](../../README.md)).
- **SSH into the thing** is recurring: dev containers, Namespace Devboxes ("reachable over SSH or
  from your local IDE"), Morph ("SSH when you want it"), Daytona (SSH access)
  ([docs](https://namespace.so/docs/devbox), [Morph](https://cloud.morph.so/docs/devboxes/development)).
- **History + metrics + CLI**: Blacksmith lists "Logs, metrics, monitors, SSH, and test analytics"
  and a CLI for "Logs, jobs, cache, and usage"
  ([docs](https://docs.blacksmith.sh/)). GitHub Actions shows a run/job/step log tree.
- **pluto already has the right verbs** — `status`, `logs --phase`, `jobs`, `attach` — and M5's
  `--json` plus noun managers (`pluto job history`, `pluto log`) make them scriptable and
  discoverable. **[inference]** The gap is `logs --follow` and an "attach to the failed job's box"
  affordance that closes the loop from history back to the machine.

### 2.8 Interop and migration

- **Import from the incumbent is the growth lever.** DevPod's entire position is "reuses the open
  DevContainer standard"; Flox can `flox containerize` and has CI integrations; Coder has a "Dev
  Containers integration"; Devbox converts to devcontainer/Dockerfile/remote. Turborepo is
  "incremental"; Nx can be added to an existing project.
- **Drop-in runner migration**: Blacksmith/BuildJet/Namespace/RunsOn all reduce to changing
  `runs-on:`. RunsOn: "The migration is one line: `runs-on: ubuntu-latest` becomes
  `runs-on: runs-on=…`", and "anything still on `ubuntu-latest` keeps running on GitHub"
  ([docs](https://runs-on.com/)).
- **pluto's natural import target is `devcontainer.json`** (for the image/tools/provision) and
  `mise.toml`/`.tool-versions` (for tools). **[inference]** A `pluto init --from devcontainer.json`
  that emits a first-draft `.pluto.toml` would save the single most tedious onboarding step.

### 2.9 Team and group features

- **Shared config via git** is universal (mise, dev containers, Turborepo, Nx, Woodpecker).
- **Shared cache** is the canonical team upgrade: Turborepo "Remote Caching … preventing
  duplicated work across your entire organization"; Nx remote cache plus distributed execution
  gives "about 50% fewer machine-minutes"; Bazel's remote cache is designed so "only your CI
  system" can write ([Turborepo](https://turborepo.com/docs/core-concepts/remote-caching),
  [Nx](https://nx.dev/docs/features/ci-features/remote-cache),
  [Bazel](https://bazel.build/remote/caching)).
- **Access/roles/secrets** are where control planes live: Coder RBAC + OIDC + audit (audit is
  "Premium") and a template hierarchy; Namespace "Authentication & Access", SOC 2, audit logs;
  Gitea runner registration at instance/org/repo scope; Woodpecker secrets.
- **pluto's box contract already has the seeds of this**: **environment layers** — "a reusable,
  content-addressed snapshot of a provisioned disk, keyed by project, declared setup fingerprint,
  base image, and trust class", with an opt-in `[provision].cache`, scrubbed before sharing
  ([GLOSSARY](../../GLOSSARY.md)) — and **trust classes** that keep untrusted PR layers out of
  trusted ones. That is a team cache designed to be self-hosted and policy-scoped. **[inference]**
  It is pluto's strongest latent team feature.

### 2.10 Extensibility without config bloat

- **Features as shareable units**: dev containers' "Features" are "self-contained, shareable
  units of installation code and development container configuration", distributed as OCI
  artifacts ([docs](https://code.visualstudio.com/docs/devcontainers/containers)).
- **Presets/extends**: Renovate's `extends`/`globalExtends` and Nx/Turborepo generators let teams
  share policy without forking config.
- **Small closed vocabularies:** Concourse "doesn't rely on a complex plugin system. Instead, it
  focuses on a single powerful abstraction: resources".
- **pluto's answer is already chosen**: `[tools]` (apt, declarative) plus `provision`/`wake`
  (shell, imperative) — "The declarative half of packaging; anything apt cannot install stays in
  provision" ([GLOSSARY](../../GLOSSARY.md)). **[inference]** Resist adding an ever-growing typed
  config; grow shell hooks and shareable environment layers instead.

### 2.11 Docs, examples, and copy-paste that works

- **Quickstarts are copy-paste, not prose.** Every leader opens with numbered steps and exact
  commands.
- **Machine-readable docs are the 2026 norm**: E2B publishes a docs MCP server and `llms.txt`;
  Task has an `llms.txt` and `llms-full.txt`; Northflank serves `.md` for every page and an
  `llms.txt`; Turborepo has `sitemap.md`/`llms.txt`/`agents.md`. pluto's docs are Markdown in the
  repo and already readable by agents. **[inference]** An `llms.txt` and a `docs/llms-full.txt`
  bundle would fit pluto's philosophy (self-hosted, no accounts) and help both humans and agents.

---

## 3. Pattern catalog

The recurring DX patterns that separate beloved tools from tolerated ones. Each names an exemplar
and says what pluto should copy. Patterns already owned by ADR 0009/0011 are marked **[own]**.

| # | Pattern | Exemplar (evidence) | What pluto copies |
|---|---|---|---|
| 1 | **Zero-account, zero-config first run** | E2B `Sandbox.create()`; Modal `modal run`; Vercel `sandbox run` ([docs](https://e2b.dev/docs), [Modal](https://modal.com/docs/guide)) | Nothing before `pluto --version` requires an account; `pluto up` on a dir with no contract should offer to scaffold, not fail. |
| 2 | **One obvious config, committed** | `devcontainer.json`, `mise.toml`, `turbo.json` | Keep `.pluto.toml` the single box contract; never add a second file for "advanced" users. **[own]** |
| 3 | **Progressive disclosure** | mise "Steps 1 to 3 … step 4 adds optional" ([docs](https://mise.jdx.dev/getting-started.html)) | Day one = `up`/`run`/`attach`; schedules, sessions, layers, devices come later and stay optional. |
| 4 | **Convention over configuration** | Turborepo reuses `package.json` scripts; `task` reads `Taskfile.yml` | Infer the project and branch from git; default the worktree target. **[own, ADR 0011]** |
| 5 | **`init` / scaffold that produces a working file** | VS Code "Add Dev Container Configuration Files…"; Actions templates ([docs](https://code.visualstudio.com/docs/devcontainers/containers)) | Add `pluto init`: detect the stack, write a minimal runnable `.pluto.toml`, commit it, print `next:`. |
| 6 | **`doctor` that names the fix** | `mise doctor` prints settings, problems, warnings, and a suggested fix, exits 1, has `--json` ([docs](https://mise.jdx.dev/cli/doctor.html)) | Add `pluto doctor`: one verdict for KVM/userns/podman/slirp/e2fsprogs/daemon/image/contract, machine-readable, ending in `next:`. |
| 7 | **Actionable errors with a next step** | just: "Errors are specific and informative, and syntax errors are reported along with their source context" ([docs](https://just.systems/man/en/)) | Keep and extend ADR 0009's `pluto:` / `next:` voice; every new command ships a test asserting a `next:` line. **[own]** |
| 8 | **Fast happy path / warm path** | dev container rebuilds are "much quicker"; pluto provision-once/wake-every-start | Surface *why* this boot is fast: `status` should distinguish wake from provision, and a due schedule's warm-up. |
| 9 | **Idempotent operations** | `up` creates-or-wakes; mise `use` installs if needed; Woodpecker/k8s-style reconcile | `up`, `run`, `device add` are safe to repeat; document idempotence explicitly. **[own for `up`]** |
| 10 | **Human + machine output** | mise `-J/--json`, `--name-only`, `--no-header`; gh `--json`; docker `-q` ([cli-comparables §1,§6,§7](cli-comparables.md)) | M5 `--json` on every read; `-q`/`--name-only`/`--no-header` for scripts. |
| 11 | **Great `--help` and completion** | grouped help, examples, `--version` alias; git `--help-all` ([cli-comparables](cli-comparables.md)) | Generate completion from `commandDocs`; keep examples per command. **[own, ADR 0011]** |
| 12 | **Print a durable handle on create** | Amp prints the thread URL and exits; E2B keeps paused sandboxes indefinitely; Daytona/Namespace name sandboxes ([cli-comparables §2](cli-comparables.md), [E2B](https://e2b.dev/docs)) | `pluto up` prints the box id (and worktree) and a "return with `pluto attach <id>`" line. |
| 13 | **Local repro of remote/CI work** | `act`, `dagger`, `woodpecker-cli exec`, Concourse `fly execute`/`intercept` ([act](https://github.com/nektos/act), [Woodpecker](https://woodpecker-ci.org/docs/usage/local-execution), [Concourse](https://concourse-ci.org/)) | `pluto run <job>` already runs the declared job locally-and-in-box; add `pluto schedule run` and `pluto logs --follow`. |
| 14 | **Import/migration path** | DevPod/Devbox/Coder consume dev containers; Turborepo "incremental" ([DevPod](https://devpod.sh/docs/what-is-devpod), [Devbox](https://www.jetify.com/docs/devbox/)) | `pluto init --from devcontainer.json` (and mise/`.tool-versions`) emits a draft contract. |
| 15 | **Sensible defaults** | Daytona `daytona-small` snapshot; dev containers pick from folder contents | Ship default image/size/auto-pause so a contract with only `[provision]` works. **[own]** |
| 16 | **Shared cache as the team charter** | Turborepo/Nx/Bazel remote cache; RunsOn S3 cache; pluto environment layers ([Turborepo](https://turborepo.com/docs/core-concepts/remote-caching), [GLOSSARY](../../GLOSSARY.md)) | Make environment layers a first-class, opt-in, trust-scoped team feature with `layer ls/prune`. |
| 17 | **Trust gate for executable config** | mise `trust`; direnv `allow`; VS Code Workspace Trust ([mise](https://mise.jdx.dev/getting-started.html), [direnv](https://direnv.net/)) | Surface the contract trust class and refuse to run an untrusted worktree's provision/wake without an explicit allow. |
| 18 | **Lifecycle transparency** | Daytona's state table; devcontainer lifecycle hooks (`onCreate`→`postAttach`) ([Daytona](https://www.daytona.io/docs/en/sandboxes/), [containers.dev](https://containers.dev/implementors/spec/)) | `status` shows the state machine (created/running/paused/failed) and the phase (provision/wake); name phases consistently with the glossary. |
| 19 | **Layered, lockable reproducibility** | dev container Features + image metadata; `mise lock`; Flox manifest ([containers.dev](https://containers.dev/implementors/spec/), [Flox](https://flox.dev/docs/)) | Keep M3's hash-locked pins; expose the applied contract hash and the image digest in `status`. **[own, M3]** |
| 20 | **Docs built for copy-paste and agents** | E2B docs MCP + `llms.txt`; Task `llms-full.txt`; Northflank `.md` per page ([E2B](https://e2b.dev/docs), [Task](https://taskfile.dev/), [Northflank](https://northflank.com/docs/)) | Add `docs/llms.txt` + a full-docs bundle; keep every quickstart runnable as-is. |

Two meta-patterns sit above these: **the quickstart is the product** (the first 60 seconds
decide adoption), and **the failure path is the product** (a tool is judged when it breaks).
pluto's ADR 0009 already invests in the second; M5 should invest equally in the first.

---

## 4. Individual → group → enterprise

The same tool must serve three audiences whose needs diverge predictably. This is what changes,
then how pluto's stated philosophy collides with — and can survive — the enterprise bar.

### 4.1 What changes

| Dimension | Individual | Group / team | Internal enterprise |
|---|---|---|---|
| **Consistency** | "works on my machine" is fine | committed contract shared in git; one environment definition | approved base images and templates; policy enforced centrally |
| **Onboarding** | `curl \| sh; pluto up` | `git clone && pluto up`; a saved **device** to a shared host | provisioned host(s), SSO/identity, a documented runbook |
| **Shared cache** | not needed; local environment layer | shared environment layers for the project; warm pools | multi-host layer store; read/write policy and poisoning defence |
| **Policy** | none | trusted default-branch; trust classes for PRs | templates/guardrails, allowed images, quotas, audit |
| **Access** | local SSH key | per-host SSH keys; a device registry | SSO/OIDC, roles, per-team hosts, secrets management |
| **Audit** | none | job history on the box | retained event/job history, exportable to their own logging |
| **Cost** | your machine, already paid for | a shared host is cheaper than N laptops | capacity, quotas, spot/pre-emptible, chargeback |

Compare the leaders' enterprise layers: Coder adds RBAC, OIDC, groups/roles, and audit ("Premium")
over Terraform templates ([docs](https://coder.com/docs/admin/security/audit-logs)); Namespace
offers "Authentication & Access", SOC 2, audit logs; Gitea scopes runners at
instance/org/repo level with registration tokens
([docs](https://docs.gitea.com/usage/actions/runner)); Woodpecker puts secrets, registries, and
forges first-class ([docs](https://woodpecker-ci.org/docs/administration/general)). The enterprise
DX pattern is *governance without per-developer toil*: central authors, self-service consumers.

### 4.2 The no-account / no-control-plane / no-telemetry tension

pluto's principles are explicit: "No accounts, no telemetry, no control plane. One host in v1"
([VISION](../VISION.md)). Teams and enterprises usually ask for the opposite: identity, shared
state, policy, and audit. The tension is real and worth naming honestly.

**The tension is mostly about where state lives, not whether it exists.** Comparable self-hostable
tools square the same circle by choosing a *local* substrate for identity and policy instead of a
vendor control plane:

- **The forge is the identity plane.** Gitea/Forgejo, Woodpecker, and Renovate do not invent
  accounts; they delegate auth to the git forge the org already runs, and scope access by
  org/repo. Gitea Actions is "built-in" to the forge; Woodpecker "will also use your forge to
  authenticate and report back the status" ([Gitea](https://docs.gitea.com/usage/actions/overview),
  [Woodpecker](https://woodpecker-ci.org/docs/administration/general)).
- **Config in git is the policy plane.** Renovate's `onboarding` PR, `extends`/`globalExtends`,
  and allowlists (`allowScripts` and `allowPlugins` default `false`;
  `allowedCommands`/`allowedUnsafeExecutions` gate execution) enforce policy *without* a control
  plane ([docs](https://docs.renovatebot.com/self-hosted-configuration/)).
- **Secrets come from the environment or the forge.** Gitea/Woodpecker secrets live in the
  self-hosted forge; Coder keeps LLM credentials "in the control plane" only because it *has* one.
- **Telemetry can be opt-in and disclosed.** RunsOn is a useful foil: it self-hosts the data plane
  in your AWS account but still runs "a license check … and a per-launch telemetry event … to
  `runs-on.com`" ([docs](https://runs-on.com/)). pluto's promise is stronger — and simpler to
  market — because there is *nothing to disclose*.
- **The control-plane path exists for those who want it.** Coder is the counter-example: a
  self-hosted control plane that buys enterprise features at the cost of an operator role (its
  install is a five-phase, cross-team exercise with a named platform owner,
  [docs](https://coder.com/docs/install)). pluto can point at Coder rather than become it.

**[inference]** pluto's viable enterprise story keeps the four principles and adds *additive,
self-hosted* team capability:

1. **Identity = the forge or an SSH CA.** pluto never owns accounts; a host trusts the SSH keys
   its operator places, and (later) a forge can assert identity for event work — mirroring the
   trusted default-branch policy already in the contract.
2. **Policy = committed contract + trust classes.** The same file that defines the box defines
   what untrusted code may see; there is no separate policy server.
3. **Shared cache = environment layers, scoped by trust class.** Opt-in, content-addressed,
   scrub-before-share — the Docker/Bazel model with a trust boundary.
4. **Audit = local, exportable records.** Job outcomes and events are already durable; an
   enterprise exports them to *its own* logging, so pluto still stores nothing centrally.
5. **No telemetry = a feature, stated plainly.** The enterprise price of "we cannot phone home"
   is that *they* run monitoring; that is a selling point for regulated buyers, not a bug.

The honest limit: without a control plane there is no cross-host fleet management, no central
RBAC, and no hosted UI. pluto should say so — as it does today — and let the forge, SSH, systemd,
and git fill those roles. That is the Gitea/Woodpecker stance, and it has a real enterprise
audience precisely because it never asks for a vendor account.

---

## 5. Anti-patterns to avoid

1. **Config explosion.** Renovate's self-hosted page lists well over 100 options, split into
   "self-hosted" vs "repository" config with warnings about putting the wrong one in the wrong
   file ([docs](https://docs.renovatebot.com/self-hosted-configuration/)). That power is earned
   for a dependency bot; for pluto it would be death. Keep `.pluto.toml` small and let shell
   hooks absorb the long tail. **[inference]**
2. **Leaky substrate abstraction.** The user should think "box", not Firecracker/vsock/rootfs.
   pluto's glossary already forbids the synonyms ("_Avoid_: sandbox, devbox, VM, environment").
   Tools that leak their substrate (Make's tabs, Docker's legacy alias debt) pay for it in
   docs and confusion. Keep the VMM, jailer, and image store behind the runner. **[own]**
3. **Slow onboarding.** Codespaces and DevPod win because the first environment is one command;
   pluto's seven-step quickstart with three host prerequisites is the clearest anti-pattern in
   its own README. Preflight (`doctor`) and `init` turn a wall into a guided path. **[inference]**
4. **Opaque failures.** Cargo documents a surprising `101`; docker prints bare flag errors; the
   fix is the `pluto:`/`next:` voice and file:line contract errors. Do not let new noun managers
   regress to bare `flag provided but not defined`. **[own, ADR 0011]**
5. **Too many ways to do one thing.** docker's `ps` vs `container ls` is the canonical cautionary
   tale in cli-comparables §6. Pick one shorthand for `run`/`attach` and never accrete both.
   **[own, ADR 0011]**
6. **Docs drift.** Examples that no longer run are worse than no examples. Generate help and
   completion from `commandDocs`, and assert README commands in tests the way ADR 0009 asserts
   `next:` lines. **[own direction]**
7. **The account wall.** Every agent-sandbox leader opens with an API key and a hosted account
   (E2B `E2B_API_KEY`, Modal account, Vercel token, Daytona sign-in). That is the exact wall
   pluto exists to not have. Do not add an account, a hosted control plane, or a phone-home
   check. **[own]**
8. **Cache poisoning / trust confusion.** Forgejo's `pull_request_target` docs read as a list of
   ways a shared cache and secrets leak to untrusted code
   ([docs](https://forgejo.org/docs/latest/user/actions/)). pluto's trust classes are the
   antidote; never let an untrusted PR publish to, or read from, a trusted environment layer.
   **[own]**
9. **Telemetry surprises.** RunsOn discloses two small outbound flows; pluto's promise is none,
   and it should stay none rather than become "opt-out with a disclaimer". **[own]**
10. **A product that dies mid-adoption.** Earthly is unmaintained and BuildJet is shut down;
    betting the team's workflow on a single vendor's config language is risky. pluto's
    self-hosted, git-native contract is the hedge. **[inference]**

---

## 6. Recommendations for pluto

Prioritized and concrete. Pattern numbers refer to §3; vocabulary follows `GLOSSARY.md`.

### 6.1 The day-one individual loop

Target: **three commands from install to a shell**, with no account and no hand-written config.

```sh
# 1. install (one-liner; checksum-verified)
curl -fsSL https://<pluto>install.sh | sh

# 2. check the host and start the host daemon — one verdict, one next step
pluto doctor            # KVM, userns, podman, slirp4netns, e2fsprogs, daemon, image
pluto install

# 3. scaffold the box contract from the current repo and boot
pluto init              # writes a minimal .pluto.toml, commits it, prints next:
pluto up                # creates the box, runs provision, wakes it
pluto attach            # an interactive shell in the box
```

What the user sees (the `pluto:` voice, ADR 0009):

```
pluto: created box 7f3a1c for /home/sid/src/app (branch main)
pluto: provisioning (first boot — this runs once per box disk)
pluto: box 7f3a1c is running in 42s
next: attach with 'pluto attach 7f3a1c'
next: run the declared jobs with 'pluto run'
```

Then the warm loop is the product: `pluto run test`, `pluto pause`, `pluto up` (wake only),
`pluto ls`, `pluto status`. `up` and `run` default to the current worktree (ADR 0011).
The point is that the *second* interaction is instant, and `status` says why ("woke in 0.8s;
provisioned 3d ago").

### 6.2 The team loop

Target: **a teammate is productive after `git clone` and one saved device**.

```sh
git clone <repo> && cd <repo>
pluto --device buildhost up     # or: pluto device add buildhost; then pluto up
pluto run test
pluto logs --job last --follow
pluto status                    # contract hash, image digest, layer in use
```

- The **box contract is the shared config** (pattern 2). Team conventions live in `.pluto.toml`
  in git, not in a dashboard.
- A **saved device** is the second-machine story (already in the glossary); M5 should add a
  guided `pluto device add` with a connection test and a `doctor`-style verdict.
- **Environment layers** are the shared cache (pattern 16): opt-in `[provision].cache`, keyed by
  project/fingerprint/image/trust class, surfaced as `pluto layer ls` and `pluto layer prune`.
  Trust classes keep untrusted PR layers isolated (anti-pattern 8).
- **Review surface**: M5's `--json` plus noun managers (`pluto job history`, `pluto box ls
  --json`) give the team a scriptable record; `status` already flags a contract that changed
  since the box applied it.

### 6.3 The internal-enterprise loop

Target: **self-hosted, no vendor account, policy in git, audit exportable**.

- **Host fleet, no control plane**: a host is a machine running the host daemon (systemd user
  service with linger); the network is the operator's (tailnet/WireGuard/port-forward, already in
  `docs/remote-access.md`). Later, `pluto host ls` and `pluto --host` make many hosts a collection
  without a central server.
- **Identity is not pluto's**: trust placed SSH keys (now) and forge-asserted identity for event
  work (later), mirroring the trusted default-branch policy. No accounts.
- **Policy is the contract + trust classes**: allowed images, resources, auto-pause, schedules,
  and what untrusted PR boxes may publish — all reviewed in git.
- **Audit is local and exportable**: `pluto jobs --json` / `pluto events --json` (M4) stream to
  the org's own logging. pluto stores nothing centrally, by design.
- **Air-gapped**: M3's hash-locked image (kernel, rootfs, Firecracker by hash) plus a local image
  store is the offline story; `doctor` should report image provenance and pin freshness.
- **Say the limits out loud**: single-host fleet semantics in v1, no central RBAC, no hosted UI.
  The comparable it can point to is Gitea/Woodpecker, not Coder.

### 6.4 Prioritized M5 / near-term features (pattern-mapped)

**P0 — do in M5, these unlock day one**

1. **`pluto doctor`** (pattern 6; also 3, 15). One command that checks the host prerequisites, the
   daemon, the imported image, and the current worktree's contract; ends with `next:` and supports
   `--json`. Reuses facts the CLI already gathers; closes the biggest gap.
2. **`pluto init`** (pattern 5; 14, 15). Detect the stack (existing `devcontainer.json`,
   `mise.toml`, `.tool-versions`, `package.json`, language files), write a minimal runnable
   `.pluto.toml`, commit it, and print `next: pluto up`. Include `pluto init --from
   devcontainer.json` as the interop path (pattern 14).
3. **`--json` on every read + `-q` / `--name-only` / `--no-header`** (pattern 10; decided in the
   M5 brief and cli-comparables). Stable field names echoing the domain (`id`, `project`, `branch`,
   `worktree`, `state`, `phases`, `jobs`, `sessions`).
4. **Print the durable handle** (pattern 12). On `up`/`attach`, print the box id, the worktree, and
   the exact command to return. Mirror Amp's "print the handle and exit" ergonomics.
5. **Shell completion generated from `commandDocs`** (pattern 11), plus `--version`/`-h` aliases
   already recorded in ADR 0011.
6. **Phase-aware `status`** (patterns 8, 18). Distinguish wake from provision, show the contract
   hash and image digest, and name the state in glossary terms (`running`/`paused`/`failed`).

**P1 — M5 or the first post-M5 release**

7. **Noun managers + one shorthand** (`pluto box|job|schedule|session`, keeping `pluto run`/
   `attach`) per the M5 brief and ADR 0011.
8. **`pluto logs --follow`** and **`pluto job history`** (patterns 13, 18); close the loop from a
   failed run to `pluto attach`/`pluto run` on the same box.
9. **`pluto schedule run`** (pattern 13) — locally fire a declared schedule's job, and
   `pluto doctor` reports when the next schedule and warm-up are due.
10. **Contract validation as a command** — `pluto contract check` with `file:line`, usable in the
    repo's own CI (pattern 6/7), building on the schema `pluto.schema.json`.
11. **Trust gate surfaced** (pattern 17): before running an untrusted worktree's provision/wake,
    require an explicit allow and show the trust class, as direnv/mise do.

**P2 — team/enterprise, after the coherence pass**

12. **Environment-layer management** (pattern 16): `pluto layer ls|prune`, prove the trust-class
    boundary, and document the shared-cache story.
13. **Host collection** (fleet, deferred per `docs/DEFERRED.md`): `pluto host ls`, `pluto --host`,
    and a `device add` that names a shared host.
14. **Export/import** a contract or box (pattern 14) and `pluto events --json` for enterprise
    audit export.
15. **`docs/llms.txt` + a full-docs bundle** (pattern 20), matching the 2026 norm and pluto's
    agent-agnostic posture.

### 6.5 What pluto should deliberately NOT copy

- **Terraform-as-templates** (Coder). Powerful for platform teams, wrong for pluto's individual
  day one; keeps the control-plane model pluto rejects. Interop with dev containers/`mise` is the
  right altitude.
- **Account/API-key-first onboarding** (E2B, Modal, Vercel, Daytona). It would break the stated
  philosophy and the single best thing about a self-hosted tool.
- **Vendor telemetry, even disclosed** (RunsOn's license check + per-launch event). pluto's
  "nothing to disclose" is a feature; do not trade it away.
- **Docker's two-spellings legacy aliases.** Choose one shorthand for `run`/`attach` and retire
  old spellings on a published schedule (cli-comparables §6).
- **A 100-option admin config** (Renovate). Keep `.pluto.toml` small; push the long tail into
  `provision`/`wake` shell and shareable environment layers.
- **Build-system semantics in the mechanism** (Make/Bazel/Turborepo/Nx). pluto's job is the
  *machine lifecycle*, not incremental builds or task graphs; a job is "a bounded command run in a
  box", and build caching belongs inside the box or in the team's own tools.
- **Config languages that can outlive the product** (Earthly's Earthfile, BuildJet's model). With
  Earthly unmaintained and BuildJet shut down, the lesson is to keep the contract boring, small,
  and git-native.
- **Substrate leakage.** Never surface Firecracker, vsock, the rootfs, the jailer, or the cgroup
  layout in user-facing output; the user owns a **box**, and it is entered with SSH.

---

## Sources

Primary sources fetched 2026-10-09. Grouped by category.

**Dev environments / remote dev**

- Dev Containers specification — <https://containers.dev/implementors/spec/>.
- VS Code Dev Containers — <https://code.visualstudio.com/docs/devcontainers/containers>.
- GitHub Codespaces overview — <https://docs.github.com/en/codespaces/overview>.
- Coder: About — <https://coder.com/docs>; Install phases — <https://coder.com/docs/install>;
  Templates — <https://coder.com/docs/admin/templates>; Audit Logs — <https://coder.com/docs/admin/security/audit-logs>.
- Daytona: Sandboxes — <https://www.daytona.io/docs/en/sandboxes/>.
- DevPod — <https://devpod.sh/docs/what-is-devpod>.
- Okteto docs — <https://www.okteto.com/docs/>.
- Ona (formerly Gitpod) overview — <https://ona.com/docs>.
- JetBrains Gateway (product page JS-only; referenced via Coder IDE support) —
  <https://www.jetbrains.com/remote-development/gateway/> and <https://coder.com/docs>.

**Tool / task / env config and runners**

- mise: Getting started — <https://mise.jdx.dev/getting-started.html>; `mise doctor` —
  <https://mise.jdx.dev/cli/doctor.html>.
- Jetify Devbox — <https://www.jetify.com/docs/devbox/>.
- Flox — <https://flox.dev/docs/>.
- Nix devShells — <https://nix.dev/tutorials/first-steps/declarative-shell>.
- just — <https://just.systems/man/en/>.
- Task — <https://taskfile.dev/>.
- GNU Make manual — <https://www.gnu.org/software/make/manual/make.html>.
- direnv — <https://direnv.net/>.

**CI runners and build orchestration**

- GitHub Actions quickstart — <https://docs.github.com/en/actions/quickstart>.
- Blacksmith docs — <https://docs.blacksmith.sh/>.
- Depot docs — <https://depot.dev/docs>.
- Namespace docs — <https://namespace.so/docs>; Devboxes — <https://namespace.so/docs/devbox>.
- RunsOn — <https://runs-on.com/>.
- BuildJet (shut down) — <https://buildjet.com/>.
- Earthly (unmaintained) — <https://docs.earthly.dev/>.
- Dagger — <https://docs.dagger.io/>.
- act — <https://github.com/nektos/act>.

**Self-hosted CI**

- Gitea Actions overview — <https://docs.gitea.com/usage/actions/overview>; Runner —
  <https://docs.gitea.com/usage/actions/runner>; Compared to GitHub Actions —
  <https://docs.gitea.com/usage/actions/comparison>.
- Forgejo Actions reference — <https://forgejo.org/docs/latest/user/actions/reference/>.
- Woodpecker: Intro — <https://woodpecker-ci.org/docs/intro>; Local execution —
  <https://woodpecker-ci.org/docs/usage/local-execution>; Administration —
  <https://woodpecker-ci.org/docs/administration/general>.
- Drone — <https://docs.drone.io/>.
- Concourse — <https://concourse-ci.org/>.

**Caching / build acceleration**

- Turborepo: Introduction — <https://turborepo.com/docs>; Remote Caching —
  <https://turborepo.com/docs/core-concepts/remote-caching>.
- Nx: Introduction — <https://nx.dev/getting-started/intro>; Remote caching —
  <https://nx.dev/docs/features/ci-features/remote-cache>.
- Bazel remote caching — <https://bazel.build/remote/caching>.
- sccache — <https://github.com/mozilla/sccache>.
- ccache — <https://ccache.dev/>.

**Agent sandboxes / offload**

- E2B — <https://e2b.dev/docs>.
- Modal — <https://modal.com/docs/guide>.
- Vercel Sandbox — <https://vercel.com/docs/vercel-sandbox>.
- Cloudflare Sandboxes — <https://developers.cloudflare.com/sandbox/>.
- Morph Cloud (Infinibranch, Devboxes) — <https://cloud.morph.so/docs/developers>,
  <https://cloud.morph.so/docs/devboxes/development>.
- Fly Machines — <https://fly.io/docs/machines/>.
- Northflank docs — <https://northflank.com/docs/>.

**Self-host / enterprise patterns and in-repo inputs**

- Renovate self-hosted configuration — <https://docs.renovatebot.com/self-hosted-configuration/>.
- In-repo: `GLOSSARY.md`, `docs/VISION.md`, `docs/pitch.md`, `docs/m5-cli-overhaul.md`,
  `docs/research/cli-comparables.md`, `docs/research/trigger-comparables.md`, `README.md`,
  `docs/adr/0007-box-contract.md`, `docs/adr/0009-cli-conventions.md`, `docs/adr/0011-cli-grammar.md`.
