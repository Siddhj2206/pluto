# M5: a coherent interface for people and agents

**Status:** product specification input; implementation is tracked in [GitHub issue #101](https://github.com/Siddhj2206/pluto/issues/101). This broad M5 brief covers the human CLI, structured agent interface, automation, interactive agent services, and provider seams. [`m5-cli-overhaul.md`](m5-cli-overhaul.md) remains the detailed CLI information architecture input. Research: [`m5-ux-patterns.md`](research/m5-ux-patterns.md), [`cli-comparables.md`](research/cli-comparables.md), and issue [#65](https://github.com/Siddhj2206/pluto/issues/65).

## Problem

Pluto has good foundations: durable boxes, project contracts, sessions and services, jobs, schedules, repository events, and remote host operation. The user experience grew as those capabilities arrived. People and agents still need a coherent way to discover projects, start work, understand its identity and state, recover from failures, and inspect results.

There are two distinct agent workflows. Automated work is a bounded job started by a prompt, schedule, or repository event. Interactive work is a long-lived agent server inside a box, reached from an existing desktop, browser, or phone client. Pluto should make both work from a host that stays available without the user's laptop, while remaining agent-agnostic and not requiring Pluto to build its own desktop/mobile client.

## Solution

Make Pluto's polished CLI and documented, versioned HTTP/JSON API two clients of one durable task/run and box lifecycle. A task is the stable requested-work identity; each accepted execution or follow-up is a child run with its own ID. Pluto can route tasks through existing branch boxes or an explicitly isolated worktree and box.

For automation, Pluto invokes declared generic jobs and retains trigger, prompt, run state, logs, and outcome. For interactive use, Pluto ensures a box and declared service are available and gives the user a connection path usable by compatible existing clients. The agent service owns its sessions and conversation protocol. Pluto does not implement agent behavior or require a Pluto web/phone app.

Use one internal extension lifecycle with distinct capabilities for private host connectivity, public in-box service ingress, event sources, and client adapters. M5 validates the seams with optional Tailscale connectivity, optional OpenTunnel ingress, and GitHub events. Keep the host owner in control of provider installation and project contract trust.

## User stories

1. As a project owner, I want Pluto to guide me from project discovery to useful work, so that I can start without first learning every internal object.
2. As a human, I want one consistent CLI vocabulary for boxes, services, jobs, tasks, runs, schedules, events, and sessions, so that related capabilities are easy to discover.
3. As a CLI user, I want help, examples, completions, and errors to use the same command model, so that I can learn and recover without separate documentation archaeology.
4. As a script author, I want stable structured output for CLI reads, so that I can automate Pluto without scraping human-oriented text.
5. As an API client, I want a documented, versioned HTTP/JSON API for task and run lifecycle, so that clients can submit and inspect work predictably.
6. As a caller submitting long-running work, I want an immediate durable task/run ID, so that I can disconnect and return later.
7. As a human or agent, I want to inspect task source, project/ref, prompts, runs, state, logs, changes, and outcome, so that I can understand what Pluto did and what to do next.
8. As a user, I want to submit follow-up prompts to a task, so that related work stays grouped while each attempt has a distinct run record.
9. As a user, I want runs within a task to execute serially, so that concurrent prompts do not race over the same task context or box.
10. As a user, I want a follow-up accepted during an active run to queue, so that my request is retained and begins when the active run ends.
11. As a project owner, I want manual prompts to create tasks and follow-ups to create runs under an existing task, so that identity and history are predictable.
12. As a user, I want Pluto to reuse the selected branch's existing box by default, so that starting agent work has low setup friction.
13. As a user, I want an explicit isolation option that creates a separate worktree and box from the selected branch, so that I can get a clean task-specific diff.
14. As a reviewer, I want Pluto to link task/run history to the relevant working-tree state and Git diff, so that I can inspect changes before using them.
15. As a user of a shared branch box, I want Pluto to show the box's current changes honestly, so that it does not imply per-task attribution where changes are shared.
16. As a project owner, I want declared jobs to invoke ordinary commands, including agent CLIs, so that Pluto remains useful across agent vendors.
17. As a user, I want automated agent work to run as a bounded job with recorded inputs and outcomes, so that event-driven and scheduled work is inspectable.
18. As an OpenCode v2 user, I want `opencode run --standalone` to be documentable as an ordinary job, so that Pluto needs no OpenCode-specific execution machinery.
19. As a user, I want a task's new prompt and task/run IDs passed to the declared job, so that the configured tool receives context while owning its own conversation continuity.
20. As a repository owner, I want a guided `pluto setup github` flow to map GitHub events to declared jobs, so that I can configure automation without writing custom Pluto machinery.
21. As a repository owner, I want one task per PR or issue and serialized runs for later matching events and explicit follow-ups, so that related automation shares an inspectable history.
22. As a repository owner, I want one task per repository branch for pushes and a new serialized run for each push, so that branch automation has a stable identity.
23. As a user, I want each schedule occurrence to create a new task, so that independent scheduled work has a clear history.
24. As an administrator, I want duplicate event deliveries to be deduplicated, so that transport retries do not repeat accepted work.
25. As a user of an interactive agent service, I want Pluto to bring the chosen box and service up and return a usable endpoint, so that I can connect with a client I already use.
26. As an OpenCode v2 user, I want to connect OpenCode Desktop or a compatible browser client to the in-box OpenCode server, so that Pluto supplies compute and access without replacing my client.
27. As a user of another agent client such as T3 Code, I want documented compatibility requirements and a verified connection recipe, so that support claims reflect real client behavior.
28. As an agent service owner, I want the service's own client protocol, authentication, pairing, and conversation state to remain authoritative, so that Pluto does not invent a competing agent API.
29. As a remote user, I want a private connection through an optional private-connectivity provider, so that I can reach the host without exposing it publicly.
30. As a project owner, I want optional public ingress to expose only a selected in-box service, so that external clients can connect without making Pluto's task/control API public.
31. As a repository owner, I want a public signed webhook-only GitHub intake option, so that GitHub can deliver events while all task/control operations remain private.
32. As a host owner, I want providers to be optional and clearly identify outside accounts or relays they depend on, so that Pluto remains account-free and I can understand external dependencies.
33. As a host owner, I want one provider lifecycle for installation, enablement, status, and removal across capabilities, so that extensions behave consistently.
34. As a host owner, I want provider installation and enablement to require separate explicit approval from project trust, so that enabling executable host extensions cannot be confused with trusting repository automation.
35. As a project owner, I want to trust a specific contract revision for unattended work, so that later contract edits do not silently gain execution authority.
36. As a project owner, I want each run to retain the contract revision and trust decision used, so that I can audit why automated work was allowed.
37. As an operator, I want the API to expose accepted, queued, starting, running, waiting, completed, failed, rejected, and blocked states with actionable errors, so that clients can drive recovery reliably.
38. As an operator, I want API operations to be safe under client retries and daemon restarts, so that a lost response does not create ambiguous duplicate work.
39. As a client developer, I want queryable run history and a documented way to follow live progress, so that clients can recover after disconnects without screen scraping.
40. As a host owner, I want task/run state to remain separate from box identity and interactive session identity, so that work survives box lifecycle and client changes.
41. As a future client author, I want the same task/run API and vocabulary available independently of the CLI, so that web and phone clients can be added later without another work model.
42. As a host owner, I want internal provider interfaces to be validated before Pluto publishes a versioned stdio plugin protocol, so that an external extension contract is based on working first-party adapters.
43. As a future extension author, I want capability boundaries for connectivity, ingress, event sources, and client adapters to be explicit, so that an extension implements only the roles it supports.
44. As an M5 implementer, I want lifecycle, trust, event intake, CLI, and provider behavior tested at public seams, so that compatibility is judged by observable behavior.

## Implementation Decisions

- M5 is a full user-experience and interface rework across human CLI use, structured agent use, automation, interactive service access, and extension setup. Preserve the existing box, contract, job, schedule, event, session, and work item model where it fits.
- Use **task** for durable requested work and **run** for each execution attempt. Tasks have stable IDs independent of host box and session; each run has its own ID. A task owns prompts, run history, trigger context, and outcomes.
- Serialize runs within a task. An accepted follow-up during a running attempt queues the next run. Keep any existing per-box scheduling/serialization constraints consistent with this rule and communicate queue state to callers.
- The selected branch box is the default execution target. Isolation explicitly creates a separate worktree and box from that branch. The box remains the source of working-tree changes; only isolated task boxes imply a clean task-specific diff.
- Maintain two agent paths:
  - **Automated:** invoke a declared generic job from a prompt, event, or schedule. OpenCode v2 `opencode run --standalone` is an example. The configured job owns tool-specific behavior; Pluto records lifecycle and result.
  - **Interactive:** ensure a declared long-lived service in the box is running and return its endpoint and client instructions. OpenCode v2 `opencode serve` is the reference example. The existing client's protocol, pairing/authentication, and session state belong to that service. Do not build a Pluto desktop/phone client or agent conversation layer in M5.
- Existing-client support must be tested and described per client/protocol, not assumed from a generic URL. Include OpenCode Desktop as the first reference client and validate at least one additional compatible client (T3 Code is a candidate) before claiming it works. Document any server version, networking, auth, and pairing requirements.
- Define a documented, versioned HTTP/JSON control API for task and run operations. MCP is a possible later client adapter, not the M5 authority or sole agent interface. Design client adapters against the same API.
- Keep a polished CLI with documented human and structured output. Resolve detailed grammar, migration, help, completion, and output choices in the CLI subbrief and any required ADRs before changing commands. Do not silently break existing CLI behavior.
- Make task/run API operations typed and explicit about state transitions, stable IDs, errors, retry/idempotency behavior, and progress/history retrieval. The API must support disconnected clients returning to query work. Design live progress as a documented stream or equivalent resumable mechanism.
- GitHub event mapping: one task per PR/issue; later qualifying events append serialized runs. One task per repository branch for pushes; each push appends a run. Each schedule occurrence creates a new task. Every new manual prompt creates a task unless explicitly submitted as a follow-up to an existing task. Preserve event deduplication.
- `pluto setup github` guides generic event-to-declared-job wiring. OpenCode GitHub Actions integration is documentation/prior art only; Pluto jobs execute on Pluto's host boxes. Keep signed public webhook intake narrowly scoped to verified event delivery; task/control APIs remain private.
- Add one internal provider lifecycle across transports, event sources, and client adapters. Keep capabilities separate: private host connectivity, public service ingress, event sources, and client adapters. A provider may implement more than one. M5 proves this with optional Tailscale private connectivity, optional OpenTunnel public ingress, and GitHub events.
- Tailscale supplies private connectivity using host identity/ACLs. OpenTunnel or another opt-in ingress provider may expose explicitly selected in-box services. Neither public ingress nor the GitHub webhook receiver exposes Pluto's task/control API. Clearly disclose third-party accounts, hosted relays, or services. Pluto itself remains account-free.
- Keep plugin executable trust independent from project contract trust. Host owner approval to install/enable a provider names its capabilities and external dependencies. Project trust names a specific contract revision; any contract change requires renewed approval before unattended execution. Record the effective revision and trust decision per run. Existing trust-class restrictions for untrusted PR work remain in force.
- Internal provider interfaces come first. Do not publish the versioned stdio plugin protocol in M5; validate the shared lifecycle through first-party providers, then specify/publish the external protocol in a later milestone.
- Review/recovery surfaces must distinguish task history from current box state. Expose logs, changed files/diff where meaningful, trigger/prompt context, outcome, and safe next actions through CLI and API.
- Issue #65 is a backlog scratchpad. Promote M5 UX and remote-client ideas here; keep M4 correctness and maintenance follow-ups in their existing backlog rather than silently broadening M5.
- This work intentionally reopens the access direction recorded in ADR 0006 to evaluate first-party private connectivity and public in-box service ingress. Update or supersede that ADR before implementing changed access behavior.

## Testing Decisions

- Good tests exercise observable contracts at the highest stable seam; assert public task/run states, outputs, authorization boundaries, and provider behavior rather than private implementation structure.
- Test task/run creation, follow-up serialization, retries, restart recovery, and idempotency through the typed daemon HTTP API with a fake runner.
- Test trust revision changes, separate plugin trust, public/private API boundaries, and event admission through the same daemon API and event intake seam.
- Test CLI human UX and structured output through the existing CLI integration tests; verify guidance and compatibility for new and retained command forms.
- Test every provider against a shared fake-provider contract covering lifecycle, capabilities, errors, configuration, and cleanup. Also verify Tailscale, OpenTunnel, and GitHub adapters against their first-party boundaries without requiring real external accounts in contract tests.
- Validate interactive connections with real supported clients and declared in-box services. Cover OpenCode Desktop first and at least one additional client before documenting support; check private connectivity and selected-service public ingress separately.
- Prior art: existing daemon API/router tests, fake runner patterns, CLI integration tests, and existing provider-like GitHub event/webhook behavior.

## Out of Scope

- A Pluto-owned hosted control plane, Pluto accounts, telemetry, or mandatory third-party relay.
- A native Pluto desktop, web, or phone client in M5.
- Agent-specific orchestration, prompts, conversation storage, model selection, agent session protocol, or custom OpenCode GitHub Actions machinery.
- Public access to Pluto's task/control API.
- Publishing the external versioned stdio plugin protocol before first-party validation.
- MCP as a required or sole agent interface.
- Fixing unrelated M4 leftovers from issue #65 as part of M5.

## Further Notes

- OpenCode v2 supports both non-interactive `opencode run` and a persistent server (`opencode serve`) that existing clients can target. Its GitHub installer instead runs through GitHub Actions; Pluto's `setup github` should wire events to host-executed generic jobs. See [OpenCode v2 CLI](https://opencode.ai/v2/docs/cli/), [v2 web/pairing](https://opencode.ai/v2/docs/cli/web/), and [GitHub integration](https://opencode.ai/docs/github/).
- Prior art includes [mise tasks](https://mise.jdx.dev/tasks/), [Amp Orbs](https://ampcode.com/docs/orbs), [GitHub Actions run monitoring](https://docs.github.com/en/actions/how-tos/monitor-workflows), [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve), [Tailscale Funnel](https://tailscale.com/docs/features/tailscale-funnel), [OpenTunnel](https://github.com/anomalyco/opentunnel), and [mise trust](https://mise.jdx.dev/cli/trust.html). These inform interaction patterns, not wholesale product assumptions.
- The issue #65 `[box].resources` item is resolved by M3 issue #66. M4 correctness and maintenance items remain follow-ups; see backlog issue #65.
