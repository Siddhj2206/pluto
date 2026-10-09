# M5 UX research: humans and agents

First-party product documentation reviewed 2026-10-06. This note extracts interaction patterns that could inform Pluto's M5 product surface; it is not a feature parity checklist.

## Patterns worth borrowing

### mise: discoverability without a separate control plane

mise tasks are named project commands. A minimal task can be declared in the project config, run with `mise run <name>`, listed with `mise tasks ls`, and inspected with `mise tasks info`. The same runner supports short TOML declarations and longer script files, so projects can start simple and grow without changing the invocation model. Shell activation is not required to run tasks. [Task overview](https://mise.jdx.dev/tasks/)

**For Pluto:** give declared work one stable name from configuration through manual launch, automation, logs, and API calls. Make discovery and inspection built-in (`list`, `show`/`inspect`), and let a tiny declaration mature into a richer script or workflow. Keep the first run usable without shell setup.

### Amp Orbs: durable work identity across devices and sleep

Amp's durable user-facing object is a thread, while the orb is its execution environment. `amp -ox` creates a remote thread, prints its URL, then exits while work continues; users can inspect or continue the same thread from another device. Sleeping orbs resume with conversation, files, and services intact. Capacity limits queue new starts and show an estimated wait instead of failing immediately. [Orbs overview](https://ampcode.com/docs/orbs) · [Spawning Orbs](https://ampcode.com/docs/cli/spawning-orbs)

Amp separates repeatable environment setup (`.agents/setup`), quick wake repair (`.agents/resume`), and managed long-running services (`.amp/services.yaml`). The docs require hooks to be safe to run more than once, and point users to service logs when readiness fails. [Customizing Orbs](https://ampcode.com/docs/orbs/customizing) · [Portals and services](https://ampcode.com/docs/orbs/portals)

For scheduled or external work, Amp can save a schedule on a thread and wake it later, or expose a webhook from an orb plugin to receive external events. [Automations](https://ampcode.com/docs/orbs/automations) · [Event-driven Orbs](https://ampcode.com/docs/orbs/event-driven)

**For Pluto:** expose a durable work/run URL or ID immediately, decouple submission from waiting, and make attach/resume/review work from any client. Explain queued capacity with a wait estimate. Keep project bootstrap repeatable and separate from services that must stay running. Treat schedules and webhooks as ways to wake a durable work record, preserving history and context.

### GitHub Actions: operations remain inspectable and recoverable

Actions presents each workflow execution as a run with a live job graph, per-step logs, elapsed times, and downloadable artifacts. Failed steps expand to show their logs; log lines can be linked directly. Runs can be rerun wholesale, for failed jobs, or for a selected job, with the original commit/ref retained. Manual runs expose the selected branch and declared inputs before submission. [Monitor workflows](https://docs.github.com/en/actions/how-tos/monitor-workflows) · [Run logs](https://docs.github.com/en/actions/how-tos/monitor-workflows/use-workflow-run-logs) · [Rerun workflows and jobs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs) · [Manually run a workflow](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)

**For Pluto:** make run state, inputs, source event/ref, timeline, output, and artifacts addressable as structured data as well as readable UI. Recovery actions should be available at the smallest safe unit (retry a job/step where semantics permit), and preserve the original trigger context.

### Dev Container specification: portable setup as project metadata

`devcontainer.json` is structured project metadata that tools can consume for local or cloud development. Its reference tooling applies reusable Features and lifecycle scripts, and can reuse the same environment definition in CI. [Specification overview](https://containers.dev/overview) · [Supporting tools](https://containers.dev/supporting.html)

**For Pluto:** consider an adapter or import path for existing devcontainer metadata, rather than forcing projects to maintain a second environment recipe. For Pluto-native setup hooks, document their phases, make them repeatable, and report which phase failed.

## Cross-cutting M5 guidance

- **One work record, many interfaces.** Keep a stable ID and URL for a task across web, CLI, API, mobile clients, schedules, and GitHub events. Make command/API vocabulary mirror the UI nouns.
- **Separate work from compute.** People should address a task/thread; Pluto can provision, sleep, wake, or replace the box behind it. Surface machine lifecycle only when it affects latency, availability, or cost.
- **Make output a contract.** Give every read operation documented JSON output and every submitted run an immediate durable handle. Stream events with stable types/IDs; allow a later client to query or resume output after disconnect.
- **Show what happened and what to do next.** A run view should show trigger, repo/ref, queued/running/waiting state, box lifecycle, agent activity, logs, changed files, and result. Failures should identify the phase and offer a safe retry/resume route.
- **Make project setup discoverable and progressively extensible.** Start with repository detection and a clear first task. Support a small config surface with inspectable defaults, then hooks or adapters for teams that need custom provisioning.
- **Treat event automation as ordinary work.** PRs, issues, schedules, and client prompts should create or continue the same inspectable work objects, with origin and permissions visible to both humans and agents.

## Source scope

All claims above link directly to product documentation/specification maintained by the relevant project. The recommendations are synthesis for Pluto and are not claims made by those projects.

### OpenCode v2: separate automation from interactive clients

OpenCode v2 documents two useful operating modes. `opencode run` is a non-interactive command for scripts and CI. `opencode serve` exposes OpenCode's own HTTP server, which existing OpenCode clients can target with `--server`; its web pairing flow can also connect a browser client. The server owns OpenCode sessions, configuration, permissions, and tool execution. The CLI can instead run standalone for isolated jobs. [v2 CLI](https://opencode.ai/v2/docs/cli/) · [v2 web and pairing](https://opencode.ai/v2/docs/cli/web/)

OpenCode's GitHub installer is a different deployment model: it installs a GitHub app and workflow that invokes OpenCode from GitHub Actions. That is useful prior art for the agent command and workflow, but it does not make the user's Pluto host the execution machine. Pluto's GitHub setup should map repository events to declared jobs that run in Pluto boxes. [OpenCode GitHub integration](https://opencode.ai/docs/github/) · [OpenCode CLI](https://opencode.ai/docs/cli/)

**For Pluto:** specify automation and interactive service use as separate paths over the same host-owned box lifecycle. Automation invokes an ordinary declared job, such as `opencode run --standalone`, and records its task/run result. Interactive use starts or wakes the box and declared OpenCode service, then gives the user the service address and connection instructions for a compatible existing client. OpenCode remains the owner of interactive sessions and its client protocol; Pluto owns box/service availability and access-provider configuration. Do not require a Pluto desktop or phone client.

Existing clients are a compatibility goal, not a blanket guarantee: each supported client needs a documented connection method and any required auth/pairing setup. Validate OpenCode desktop and at least one other client such as T3 Code against the available server protocol before describing it as supported. Public ingress must not expose Pluto's control API; any public service route is limited to explicitly selected in-box services.
