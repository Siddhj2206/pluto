# Agent sandbox landscape

Research for pluto ticket [#3](https://github.com/Siddhj2206/pluto/issues/3), under map [#1](https://github.com/Siddhj2206/pluto/issues/1).

**Question.** How do today's agent sandbox and dev-environment systems work end-to-end, and which of their properties are commodity, rare, or absent — and what does that imply for pluto's unique claim?

**Method.** Mechanics are cited to primary sources: official docs, source repos, API references, first-party blogs. Complaint sections use GitHub issues and Hacker News/Reddit threads, labelled as anecdotal. All links accessed **2026-10-04** unless noted. Unverified or conflicting points are marked **Uncertainty**.

**Scope covered.** E2B, microsandbox, Arrakis, Handler, Hotcell, Docker Sandboxes, AWS Lambda MicroVMs, Coder, Devpod, GitHub Codespaces.

---

## 0. Orientation

Two useful axes separate these systems:

- **Where the machine lives**: cloud-managed (E2B Cloud, Lambda MicroVMs, Codespaces, Docker Cloud sandboxes, microsandbox Cloud) vs local/self-hosted (microsandbox local, Arrakis, Handler, Hotcell, Docker local, E2B Embed, Coder, Devpod).
- **What the product is**: an *agent sandbox* (ephemeral execution environment for model-generated code: E2B, microsandbox, Arrakis, Lambda MicroVMs, Docker Sandboxes) vs a *dev environment* (a persistent workspace a human returns to: Coder, Devpod, Codespaces). Handler and Hotcell straddle the line: agent-supervision control planes over local VMs.

| System | VMM / unit | Pause model | Fork | Self-host | License |
|---|---|---|---|---|---|
| E2B | Firecracker microVM | memory+disk, or disk-only | yes (100/call) | single machine (Embed), BYOC | Apache-2.0 runtime |
| microsandbox | libkrun microVM | disk default; full snapshot | yes, CoW memory | local-first runtime | Apache-2.0 |
| Arrakis | Cloud Hypervisor | snapshot/restore (memory+disk) | via snapshot | single-host daemon | AGPL-3.0 + commercial |
| Handler | Docker + Firecracker + 6 clouds | stop only; VM clone | worktree/VM clone | yes (Linux full) | MIT |
| Hotcell | Docker / Firecracker / Apple VZ | none documented | none documented | per-host daemon | Apache-2.0 |
| Docker Sandboxes | microVM (native hypervisor) | stop (VM persists) | none documented | local free; cloud managed | proprietary (free CLI) |
| Lambda MicroVMs | Firecracker (AWS) | full memory+disk suspend | no | in-account, not self-hosted | proprietary |
| Coder | Terraform-defined (K8s/VM/container) | stop (disk persists) | no | yes (control plane) | AGPL-3.0 |
| Devpod | devcontainer on any provider | stop (workspace persists) | no | client-only | MPL-2.0 |
| Codespaces | container on GitHub VM | stop (disk persists) | no | no | proprietary |

---

## 1. E2B

**Image/build pipeline.** A template starts from a container image; E2B extracts its filesystem, provisions it (including `envd`), runs user build steps, boots a sandbox, runs the start command, waits for readiness, then **snapshots the running sandbox — filesystem and memory together — as the template**. Sandboxes "create" by resuming that snapshot, which E2B claims loads in ~80 ms; the guest kernel is LTS 6.1 pinned at template-build time ([how templates are built](https://docs.e2b.dev/template/how-it-works.md)). Builds are layered and hashed so rebuilds skip unchanged steps, and a final "optimize" phase records which memory pages a fresh resume touches ([runtime architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)). Artifacts live in object storage as `{buildID}/memfile`, `rootfs.ext4`, `snapfile`, `metadata.json` with `.header` indexes; a paused snapshot and a template have the *same artifact shape* — a paused box is a build whose mem/rootfs are stored as diffs against its template ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)).

**Lifecycle verbs.** SDK/API: create, connect (auto-resumes), pause, resume, kill, fork, snapshot; `setTimeout`; lifecycle config `onTimeout: kill|pause` and `autoResume`. CLI: create sandbox, connect, fork, exec, shutdown sandboxes, manage snapshots, template build ([persistence](https://docs.e2b.dev/sandbox/persistence.md), [fork](https://docs.e2b.dev/sandbox/fork.md), [CLI index](https://docs.e2b.dev/llms.txt)). A paused sandbox is kept **indefinitely**; only `kill` removes it. Continuous running is capped at 1 h (Hobby) / 24 h (Pro); the cap resets on pause/resume ([persistence](https://docs.e2b.dev/sandbox/persistence.md), [FAQ](https://docs.e2b.dev/faq/sandbox-lifetime.md)).

**State and snapshot model.** Default pause saves memory + filesystem. `pause({keepMemory:false})` saves **filesystem only**; resume reboots and processes die ([filesystem-only snapshots](https://docs.e2b.dev/sandbox/filesystem-only-snapshots.md)). Pause takes ~4 s per GiB RAM, resume ~1 s ([persistence](https://docs.e2b.dev/sandbox/persistence.md)). Memory pages are restored lazily via `userfaultfd`; rootfs is a copy-on-write NBD overlay over the read-only template; pause diffs dirty memory pages and dirty rootfs blocks against the template and uploads asynchronously to object storage ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)). Fork checkpoints the live sandbox in place (pausing it briefly, dropping open connections), snapshots once, and boots up to 100 independent copies from that snapshot; the original keeps running and its expiration is untouched ([fork](https://docs.e2b.dev/sandbox/fork.md)).

**Networking.** Each sandbox gets a network namespace with veth + tap, NAT, and a per-slot nftables egress firewall that can inspect TLS SNI and HTTP Host for allow/deny lists ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)). Any port a process opens is reachable at `https://<port>-<sandboxID>.<domain>` with per-sandbox access tokens; paused sandboxes wake transparently on incoming traffic ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)). Sandbox egress IPs are ephemeral, rotating, and not published as CIDRs ([FAQ](https://docs.e2b.dev/faq/egress-ip-ranges.md)). Secrets are metadata-only on the control plane and substituted into outbound requests by the egress proxy, never entering the VM ([runtime README](https://github.com/e2b-dev/runtime)).

**Agent integration.** `envd` runs in every VM (systemd, port 49983, Connect RPC + REST) and exposes process/PTY, filesystem, watcher, and port APIs; it can be live-upgraded at resume without dropping workloads ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)). SDKs (Python/JS, plus desktop/code-interpreter variants), SSH via WebSocket proxy, a **MCP gateway with 200+ servers**, and ready-made templates for Claude Code, Codex, OpenCode, Cursor, LangChain Deep Agents, Vercel Eve, etc. ([docs index](https://docs.e2b.dev/llms.txt)).

**Fleet and self-hosting.** The full runtime is Apache-2.0: REST API (Gin), a per-node orchestrator driving Firecracker, `client-proxy` edge routing, `envd` in-VM, template manager, Postgres + Redis + ClickHouse + object storage, and best-of-K placement across nodes ([runtime README](https://github.com/e2b-dev/runtime), [architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)). **E2B Embed** runs the whole stack on one Linux + KVM machine via Docker Compose, Terraform (GCP/AWS), or a Kubernetes node, same SDK/API as cloud, data stays on the node; the README calls it "an evaluation package, not a production deployment pattern" ([Embed README](https://github.com/e2b-dev/runtime/blob/main/embed/README.md)). Beyond one machine the options are private cloud (in development) and BYOC managed by E2B — "a managed deployment in your account rather than self-hosting" ([Embed README](https://github.com/e2b-dev/runtime/blob/main/embed/README.md), [BYOC](https://docs.e2b.dev/byoc.md)).

**User complaints (anecdotal + documented limits).** The docs themselves document the operational sharp edges: pause takes 4 s/GiB; a pause can be refused with HTTP 503 (`ServiceBusyException`) when the node is still snapshotting, and the caller must retry; an auto-pause that keeps being refused for ~2 minutes degrades to a filesystem-only snapshot so the sandbox stops overstaying its timeout ([persistence](https://docs.e2b.dev/sandbox/persistence.md)). Volumes (beta) explicitly cannot be snapshotted, file locking can hang, and mounts are fixed at creation ([FAQ](https://docs.e2b.dev/faq/volumes-beta-limitations.md)). On HN a user building an agent platform noted "E2B sandboxes can die/restart" as a thing their state machine had to handle ([comment, Oct 2025](https://news.ycombinator.com/item?id=45743040)). The open-source runtime's issue tracker shows a long tail of self-hosting pain (e.g. #864, #731, #1582 at 19–26 comments) ([issues](https://github.com/e2b-dev/runtime/issues?q=is%3Aissue+sort%3Acomments-desc)). A competitor notes E2B's 24 h continuous cap as a differentiator ([HN comment](https://news.ycombinator.com/item?id=46691072)) — treat as marketing, not fact, though the cap itself is documented.

**Pluto-relevant.** E2B is the closest architecture to a pluto box: snapshot-as-machine, object-storage artifacts, lazy memory, diff-on-pause, auto-pause + traffic resume, fork. The critical divergence: E2B's fast path is *memory* restore. Its filesystem-only mode is the exception, is incompatible with auto-resume, and cold-boot requires journal replay (`e2fsck -p -E journal_only`) plus freeze/thaw hooks — direct evidence that disk-only pause is a distinct, solvable problem, not just "pause minus memory" ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md), [filesystem-only](https://docs.e2b.dev/sandbox/filesystem-only-snapshots.md)).

---

## 2. microsandbox

**Image/build pipeline.** Runs OCI images from Docker Hub/GHCR/any registry in libkrun microVMs; layers are flattened into a block-backed OCI rootfs, with an OCI image cache, disk-image roots, host-directory binds, and named/disk volumes ([README](https://github.com/superradcompany/microsandbox), [docs index](https://docs.microsandbox.dev/llms.txt)). It is a library/SDK first: `Sandbox.builder("...").create()` boots the VM as a child process, "no setup server, no long-running daemon" ([README](https://github.com/superradcompany/microsandbox)). The runtime was rewritten in April 2026 around an embeddable SDK and a smoltcp userspace network stack ([changelog](https://docs.microsandbox.dev/changelog/2026-04-03.md)).

**Lifecycle verbs.** `msb run`, `msb create`, `msb exec`, `msb stop`, `msb start`, `msb rm`, `msb fork`, `msb snap create/restore/ls/inspect/rm/head/export/import`, `msb modify`, `msb ls/ps/inspect/metrics`; SDKs mirror these (Rust, Python, TypeScript, Go, Ruby). Sandboxes can run detached for long-lived sessions ([README](https://github.com/superradcompany/microsandbox), [snapshots](https://docs.microsandbox.dev/sandboxes/snapshots)).

**State and snapshot model.** Two snapshot types: **disk** (default; files, boots a new VM on restore) and **full** (disk + memory + processes; resumes execution) ([snapshots](https://docs.microsandbox.dev/sandboxes/snapshots)). Restore always creates a new sandbox; the source is unchanged. Full restores copy memory, or share unchanged pages copy-on-write with `--cow-mem`/`cowMemory()`. Fork creates a child from a running or paused sandbox directly, with memory sharing built in ([snapshots](https://docs.microsandbox.dev/sandboxes/snapshots)). Snapshots form **groups** with heads (immutable members), can be exported as `.msb` archives (with or without image) for moving between machines, support `--since` delta export, integrity hashing, and disk-layer compaction. Guest flush policies (`auto`, `required`, `skip`) decide whether pending filesystem writes are flushed before capture; a required flush is recommended before disk snapshots of paused sandboxes ([snapshots](https://docs.microsandbox.dev/sandboxes/snapshots)). Cloud supports only disk snapshots from stopped persistent sandboxes; full snapshots, forking, groups, and archives are local-only ([cloud compat](https://docs.microsandbox.dev/cloud/overview)).

**Networking.** All traffic crosses a host-controlled userspace stack. Defaults: public internet allowed; private ranges, loopback, link-local, and cloud metadata denied; only explicitly published ports accept inbound; published ports bind `127.0.0.1` by default. Policies compose from `public/private/host` profiles or first-match rules, with strict hostname mode (fails closed when SNI cannot be inspected), DNS rules, SOCKS outbound proxies, TLS interception with a generated CA, NAT64 handling, rate limits, and `host.microsandbox.internal` for reaching the host ([networking](https://docs.microsandbox.dev/networking/overview)). Secrets are substituted host-side ("unexploitable secret keys that never enter the VM") ([README](https://github.com/superradcompany/microsandbox)). `multi-tenant` deployment profiles add a host-runtime isolation floor (forced DNS-rebinding protection, no published ports, connection caps) ([networking](https://docs.microsandbox.dev/networking/overview)).

**Agent integration.** Embedded SDKs, MCP server (`microsandbox-mcp`) with tools for lifecycle/exec/filesystem/volumes/monitoring, agent skills installable via `npx skills add`, native SSH/SFTP, and first-party examples for Claude Code, Codex, OpenCode, Gemini CLI, Goose, Playwright, and Docker-in-VM ([README](https://github.com/superradcompany/microsandbox), [docs index](https://docs.microsandbox.dev/llms.txt)).

**Fleet and self-hosting.** The local runtime is the product: Apache-2.0, runs on Linux (KVM), macOS (Apple Silicon), Windows (WHP), single machine. microsandbox Cloud is a hosted backend behind the same CLI/SDK, currently private beta; profiles accept a custom `url` for "a development, self-hosted, or on-premises control plane", and an enterprise section covers managed device config — but no open control-plane codebase or multi-host scheduler is documented ([backends](https://docs.microsandbox.dev/operations/backends), [cloud](https://docs.microsandbox.dev/cloud/overview), [enterprise](https://docs.microsandbox.dev/enterprise.md)). **Uncertainty:** whether the cloud control plane can be self-hosted is not resolvable from public docs.

**User complaints (anecdotal).** The project labels itself beta ("expect breaking changes, missing features, rough edges") ([README](https://github.com/superradcompany/microsandbox)). Recent GitHub issues show the failure surface: full snapshot fails when virtio-fs state exceeds 1 MiB (#1750), a published-port listener can outlive its sandbox (#1717), one slow request on a virtio-fs mount blocks every other request (#1702), executions stuck in "draining" (#1742), DNS/allowlist edge cases (#1745), no GPU support (#291, 21 comments) ([issues](https://github.com/superradcompany/microsandbox/issues)).

**Pluto-relevant.** Closest to pluto's *local* shape: embedded, no daemon, disk snapshots as the default with full snapshots optional — and it independently converged on the same semantics pluto wants (disk survives, processes die on disk restore). Its snapshot groups/heads, `since` delta export, guest-flush policies, and CoW memory sharing are the mature vocabulary for the state model pluto is designing. Its gap is exactly pluto's opening: everything is per-host; archives move snapshots manually; there is no leased, content-addressed fleet.

---

## 3. Arrakis

**Image/build pipeline.** Rootfs customized through a Dockerfile (packages/binaries added there), with a prebuilt Linux kernel or a user-supplied one; each sandbox runs Ubuntu with a code-execution service and a VNC server at boot. Overlayfs protects the host rootfs view ([README](https://github.com/abshkbh/arrakis)).

**Lifecycle verbs.** `arrakis-client start -n`, `stop`, `destroy`, `list`, `list-all`, `snapshot -n <vm> -o <snap>`, `restore -n <vm> --snapshot <snap>`; the REST server exposes start/stop/destroy/list-all; a Python SDK (`py-arrakis`) and an MCP server wrap the same API ([README](https://github.com/abshkbh/arrakis)).

**State and snapshot model.** Snapshot-and-restore of the whole VM via **Cloud Hypervisor** snapshots: "any processes spawned, files modified etc. will be restored as is" — i.e. memory + disk ([README](https://github.com/abshkbh/arrakis)). Restored VMs currently reuse the original IP; restoring on the same host requires stopping or destroying the original first ([README](https://github.com/abshkbh/arrakis)). Cloud Hypervisor's own docs note snapshot/restore is not guaranteed across VMM versions and not for live migration across versions ([Cloud Hypervisor repo](https://github.com/cloud-hypervisor/cloud-hypervisor)).

**Networking.** Each sandbox gets a tap device added to a host Linux bridge; the restserver automatically manages port forwarding from the host to sandbox ports (VNC/GUI, code-server), and SSH with a configured user/password is available ([README](https://github.com/abshkbh/arrakis)).

**Agent integration.** REST API, Python SDK, an MCP server ([arrakis-mcp-server](https://github.com/abshkbh/arrakis-mcp-server)), VNC + pre-installed Chrome for computer-use agents, and code execution inside the sandbox ([README](https://github.com/abshkbh/arrakis)).

**Fleet and self-hosting.** Self-hosted, AGPL-3.0 with a commercial-license path and a CLA; single-host design — every VM's lifetime is tied to the lifetime of the `arrakis-restserver` daemon; VM snapshot paths are on the host ([README](https://github.com/abshkbh/arrakis)). GCP setup instructions exist, but no multi-host control plane ([GCP setup](https://github.com/abshkbh/arrakis/blob/main/setup/gcp-instructions.md)).

**User complaints (anecdotal).** The tracker is small and the project is dormant (last push 2025-06-02, 883 stars), but its issues are instructive: snapshot creation fails with "source file not found" when snapshotting a restored VM (#4), restoring multiple snapshots fails with "tap ID is not available" (#5), VNC startup issues (#1), and — most relevant to any control-plane design — **the REST API has no authentication**, allowing unlimited VM create/delete and host exhaustion (#8) ([issues](https://github.com/abshkbh/arrakis/issues)).

**Pluto-relevant.** Arrakis demonstrates the simplest credible self-hosted form (one daemon, REST, tap/bridge, Dockerfile rootfs, MCP) and its failure modes: IP/tap identity is coupled to the host network stack, snapshots are host-local paths with fragile restore chains, and there is no auth/lifecycle safety. Pluto's lease model must not tie a resumed box to host network identity the way Arrakis does.

---

## 4. Handler

**Image/build pipeline.** Docker images for containers; Dockerfile-based templates plus VM base images. The Firecracker path installs Firecracker and downloads an Ubuntu 24.04 base image via `scripts/setup.sh`; VM disks are sparse ext4 images; templates support multi-backend builds ([site](https://handler.dev), [README](https://github.com/Launchable-AI/handler.dev)).

**Lifecycle verbs.** Unified API: create/start/stop/delete for sandboxes; Firecracker VMs boot in ~5 s per the README (marketing site claims < 2 s — **Uncertainty:** discrepancy); `Fork` (git worktree) and `Clone` (VM clone) from the canvas; tmux sessions survive WebSocket disconnects, server restarts, and page reloads ([README](https://github.com/Launchable-AI/handler.dev)).

**State and snapshot model.** No memory snapshot. Stop persists the VM; terminal state is persisted via tmux; VM disks are compacted on stop with `zerofree` + `fallocate --dig-holes` because virtio-blk does not pass DISCARD to the host ([README](https://github.com/Launchable-AI/handler.dev)). Forking is at the *worktree* level: "Fork a VM into a worktree to give two agents the same starting point on different branches" ([README](https://github.com/Launchable-AI/handler.dev)).

**Networking.** A Rust `handler-tap-helper` with `CAP_NET_ADMIN` creates tap devices on a `handler-br0` bridge with NAT; VMs get IPs on `172.31.0.0/24`; port forwarding; SSH via generated keys on port 2222; the control API binds to `127.0.0.1` only, with iptables hardening so guests cannot reach it ([README](https://github.com/Launchable-AI/handler.dev)).

**Agent integration.** Agent detection via `pgrep` for Claude Code, Codex, Gemini CLI, OpenCode; per-agent config presets (model, MCP servers, instructions, permissions, env); an **MCP server registry** deployed per sandbox; xterm.js terminals; an LLM classifies terminal activity as needs_input/working/error/done ([README](https://github.com/Launchable-AI/handler.dev)).

**Fleet and self-hosting.** MIT, fully self-hosted control plane + web UI; backends behind one `SandboxAdapter`: Docker + Firecracker locally, plus AWS, GCP, Azure, DigitalOcean, Linode, Daytona ([README](https://github.com/Launchable-AI/handler.dev)). Linux/WSL get Firecracker; macOS gets containers only. A cloud offering is advertised on the site (`/cloud`, `/pricing`) but was not researched here — **Uncertainty**.

**User complaints (anecdotal).** Essentially none: the repo (created 2026-04-14) has **0 open issues**, 12 stars, last push 2026-06-20; the Show HN post has 2 points and 0 comments ([Show HN](https://news.ycombinator.com/item?id=47850720), [repo](https://github.com/Launchable-AI/handler.dev)). Setup requires `sudo ./scripts/setup.sh` for the Firecracker path. The honest read: immature, but the closest existing product to "agent control plane over worktree-keyed local VMs".

**Pluto-relevant.** Handler validates two pluto instincts: worktree-level forking is a desirable, understandable primitive; and a local control plane with an API + terminal surface is enough (no Kubernetes, no cloud). It also shows the cost pluto is avoiding: eight backend adapters and a web UI.

---

## 5. Hotcell

**Image/build pipeline.** Docker images on the container driver; OCI-style workload contract (`hotcell run --egress --opencode` provisions: starting → cloning repo → running setup); works on macOS Docker, Linux Docker, Firecracker (Linux/KVM), and Apple VZ (macOS) ([site](https://hotcell.sh), [README](https://github.com/sinameraji/hotcell)).

**Lifecycle verbs.** `hotcell create -n 5 --repo … --branch`, `terminal <id>`, `run` (one-shot create → run → destroy), `rm --all`, `keys add/import/ls`, `exec`; a TypeScript SDK (CLI + daemon + SDK in one npm install) and a Python SDK (`pip install hotcell`) ([README](https://github.com/sinameraji/hotcell)).

**State and snapshot model.** No snapshot or fork is documented; cells are created/removed. The differentiator is not state — it's key isolation and egress (see below). **Uncertainty:** whether paused/stopped cells persist is not documented in the material consulted.

**Networking.** A host-side gateway is the only egress path: real API keys stay on the host, the sandbox sees a short-lived per-cell token, and git `origin` is rewired so `git push` works without keys. Egress can be default-deny and is "kernel-enforced on microVMs (no NIC) and Linux containers, advisory on the microVM-NIC and macOS-Docker paths"; microVM guests have no network device by default and reach out only via the gateway over vsock. Non-HTTP protocols, request-signing schemes (SigV4), mTLS, and OAuth refresh flows are explicitly *not* protected ([README](https://github.com/sinameraji/hotcell), [site](https://hotcell.sh)).

**Agent integration.** OpenCode wiring (`--opencode`), per-repo branch fan-out for N agents, CLI/TS/Python SDKs; no MCP server documented ([README](https://github.com/sinameraji/hotcell)).

**Fleet and self-hosting.** Apache-2.0, one daemon per host, "on your own hardware" (Mac Mini, cloud VM, bare metal), admission control that refuses to oversubscribe rather than OOM, and a Linux self-hosting guide; SDKs are TypeScript and Python. No multi-host scheduler documented ([README](https://github.com/sinameraji/hotcell), [site](https://hotcell.sh)).

**User complaints (anecdotal).** Small project (19 stars, last push 2026-09-25, 3 open issues). Issue #3: Apple VZ unavailable from a plain npm install — the signed helper/guest isn't bundled; #1 (closed): request for declarative per-sandbox setup at create time ([issues](https://github.com/sinameraji/hotcell/issues)). Low adoption; limited complaint corpus.

**Pluto-relevant.** Hotcell is the "many agents on one repo" pattern pluto also serves, and its key/egress gateway is the strongest secret-hygiene design in the local-first group (keys never enter the box; git push works keylessly). Pluto has no trust model, but hygiene still matters; this is the pattern to borrow if pluto ever injects credentials.

---

## 6. Docker Sandboxes

**Image/build pipeline.** `sbx` boots an agent-specific template into a microVM; custom templates and "kits" (declarative YAML) package tools/network/entrypoint; `WORKDIR` defaults to `/home/agent/workspace` for mountless sandboxes ([docs](https://docs.docker.com/ai/sandboxes/), [architecture](https://docs.docker.com/ai/sandboxes/architecture/)). Local sandboxes use native hypervisors (macOS Virtualization.framework, Windows Hyper-V, Linux KVM) and do not need Docker Desktop or Docker Engine; the CLI is free including commercial use ([install](https://docs.docker.com/ai/sandboxes/install/), [docs](https://docs.docker.com/ai/sandboxes/)).

**Lifecycle verbs.** `sbx run <agent>`, `sbx create`, `sbx exec`, `sbx attach`, `sbx stop`, `sbx rm`, `sbx ls`, `sbx cp`, `sbx secret`, `sbx mcp`; `sbx --cloud …` routes to Docker-managed cloud sandboxes ([docs](https://docs.docker.com/ai/sandboxes/), [cloud](https://docs.docker.com/ai/sandboxes/cloud/)). Local sandboxes persist until removed; stop/restart preserves installed packages, Docker images, and in-sandbox files ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)). Cloud sandboxes expire after **one hour by default**; on expiry the service stops resumable sandboxes and deletes the rest ([cloud](https://docs.docker.com/ai/sandboxes/cloud/)).

**State and snapshot model.** Three workspace layouts: direct bind mount (host files, no sync), clone mode (host repo mounted read-only at `/run/sandbox/source`, agent works in a private clone), and mountless (files stay in the VM and persist across stop/start) ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)). **No snapshot/pause API is documented** — persistence is VM-stays-alive; there is no memory or disk snapshot verb. Each sandbox has its own Docker daemon and image cache; sandboxes do not share images/layers ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)).

**Networking.** All outbound TCP routes through a host-side proxy; HTTP/HTTPS via forward proxy with allow/deny policy, other TCP transparently. Credentials are injected by the proxy after the request leaves the VM (the agent sees a sentinel); a host-side MCP gateway brokers registered MCP servers, including host-stdio servers that never run inside the VM ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)).

**Agent integration.** Agent-agnostic: Claude Code, Codex, Copilot CLI, Gemini CLI, Kiro, OpenCode; MCP gateway; editor attach over SSH (VS Code, Cursor); kits for reusable environments; org admins can centrally enforce network/filesystem/MCP policy (paid) ([docs](https://docs.docker.com/ai/sandboxes/), [blog](https://www.docker.com/blog/docker-sandboxes-run-claude-code-and-other-coding-agents-unsupervised-but-safely)).

**Fleet and self-hosting.** Local: one per developer machine; there is no self-hosted fleet control plane. Cloud: "same microVM boundary" on Docker-managed infrastructure with carry-over between local and cloud ([product page](https://www.docker.com/products/docker-sandboxes)), experimental, requires a paid subscription ([cloud](https://docs.docker.com/ai/sandboxes/cloud/)). Sign-in to a Docker account is required even for local use ([install FAQ](https://docs.docker.com/ai/sandboxes/install/faq/)).

**User complaints (anecdotal).** 255 open issues on `docker/sbx-releases` — a real bug surface for a young product: Linux memory-pressure watcher for unprivileged users never starts (#630), kit-builder kernel fails on Linux x86_64 (#643), AppArmor/permissions with the raw Linux tarball (#654), GPU on AMD EPYC (#655), virtiofs `link()`/`fsync()` correctness (#629), Windows secrets/rm failures (#633), MCP gateway duplicate-name dropping (#636) ([issues](https://github.com/docker/sbx-releases/issues)).

**Pluto-relevant.** Docker proves the demand: a first-class local microVM sandbox CLI, git-aware (clone mode), with strong secret hygiene and no snapshots at all — persistence means "keep the VM running". That is precisely the resource model pluto replaces with disk-only pause. Its clone mode is the closest commercial analogue to a worktree-keyed box.

---

## 7. AWS Lambda MicroVMs

**Product shape.** Announced 2026-06-22: a serverless primitive inside AWS Lambda for isolated, stateful execution environments — Firecracker VMs running Amazon Linux 2023, with full OS access, up to 8 h per session, launched/suspended/resumed/terminated via API ([AWS News Blog](https://aws.amazon.com/blogs/aws/run-isolated-sandboxes-with-full-lifecycle-control-aws-lambda-introduces-microvms), [core concepts](https://docs.aws.amazon.com/lambda/latest/dg/microvms-how-it-works.html)).

**Image/build pipeline.** You upload a zip containing a `Dockerfile` + app artifacts to S3; Lambda starts a fresh VM from a managed AL2023 base image (or your own container base via ECR), runs the Dockerfile, starts the app via ENTRYPOINT/CMD, waits for the `/ready` hook, then **captures a snapshot of disk and memory state including running processes**. A MicroVM run restores from that snapshot. Images are versioned with explicit `ACTIVE`/`INACTIVE` activation and base-image deprecation windows. Build hooks: `/ready` and `/validate`; runtime hooks: `/run`, `/suspend`, `/terminate`. A documented snapshot-compat gotcha: anything unique generated at build time (IDs, secrets, connections) is shared by every MicroVM from that image; generate it in `/run` ([images](https://docs.aws.amazon.com/lambda/latest/dg/microvms-images.html)).

**Lifecycle verbs and states.** `run-microvm`, `suspend-microvm`, `resume-microvm`, `terminate-microvm`; auto-suspend after an idle period, auto-resume on traffic (`autoResumeEnabled`) or explicit resume. States: PENDING → RUNNING → SUSPENDING → SUSPENDED → (RUNNING | TERMINATING) → TERMINATED; suspended VMs preserve memory and disk and accrue **no compute charges**; resume is sub-second per AWS ("single-digit seconds" per an independent note). `maximumDurationInSeconds` caps sessions at 28,800 s (8 h) ([core concepts](https://docs.aws.amazon.com/lambda/latest/dg/microvms-how-it-works.html), [Claude integration](https://docs.aws.amazon.com/lambda/latest/dg/microvms-integrations-claude-managed-agents.html)).

**Sizing and pricing.** Baseline/peak model: baseline 0.5 GB/0.25 vCPU up to 8 GB/4 vCPU, bursting to 4× (max 32 GB/16 vCPU); disk 8–32 GB; per-second billing on baseline plus peak usage; snapshot operations/storage billed; suspended = no compute charges ([images](https://docs.aws.amazon.com/lambda/latest/dg/microvms-images.html), [product page](https://aws.amazon.com/lambda/lambda-microvms)).

**Networking.** MicroVMs receive requests through inbound HTTPS endpoint URLs; network connectors control inbound (Lambda-provided defaults with JWE authentication, shell access) and outbound access independently, including a VPC egress connector for private resources; public internet egress is the default. Build-time and runtime connectors can differ ([core concepts](https://docs.aws.amazon.com/lambda/latest/dg/microvms-how-it-works.html), [Claude integration](https://docs.aws.amazon.com/lambda/latest/dg/microvms-integrations-claude-managed-agents.html)). IAM credentials are delivered via IMDSv2 ([Claude integration](https://docs.aws.amazon.com/lambda/latest/dg/microvms-integrations-claude-managed-agents.html)).

**Agent integration.** Lambda MicroVMs are a **managed sandbox provider for Claude Managed Agents self-hosted sandboxes**: Anthropic runs the agent loop, the MicroVM runs tool calls (bash/read/write/edit/glob/grep in `/workspace`) and posts results back; a launch Lambda starts one MicroVM per session from a webhook; AWS publishes a CloudFormation reference sample ([Claude integration](https://docs.aws.amazon.com/lambda/latest/dg/microvms-integrations-claude-managed-agents.html), [AWS Compute Blog](https://aws.amazon.com/blogs/compute/running-self-hosted-ai-agent-sandboxes-with-aws-lambda-microvms)).

**Fleet and self-hosting.** AWS-managed only. "Self-hosted" here means the sandbox runs in *your AWS account/VPC* while the orchestration loop (Anthropic's) stays outside; there is no self-hostable software. Scale and fleet management are AWS's ([Claude integration](https://docs.aws.amazon.com/lambda/latest/dg/microvms-integrations-claude-managed-agents.html)).

**User complaints (anecdotal).** A 386-point HN thread with 212 comments shows appetite plus skepticism; the most concrete recurring themes are the crowdedness of the sandbox market and cost-model wariness. An independent write-up notes practical caveats: snapshots capture unique content generated at build time, so randomness/identity must be re-initialized via lifecycle hooks, and launch is "within seconds" rather than instant ([Aidan Steele, 2026-06-23](https://awsteele.com/blog/2026/06/23/some-notes-on-lambda-microvms.html)). Reddit discussion exists but is mostly exploratory ([r/aws](https://www.reddit.com/r/aws/comments/1ueul5o/hardest_problems_lambda_microvms_can_solve_now/)). No systematic complaint corpus yet — the product is ~3.5 months old.

**Pluto-relevant.** Lambda MicroVMs is "E2B as a cloud primitive": memory+disk suspend, snapshot-restore launch, per-session billing, idle suspend. It confirms suspend/resume as the serverless agent model **and** confirms that the managed version still needs lifecycle hooks, unique-content reinit, and an external control plane — exactly the complexity pluto removes by keeping machines local and state in a bucket.

---

## 8. Coder

**Image/build pipeline.** Workspaces are defined in **Terraform** (EC2 VMs, Kubernetes pods, Docker containers, etc.); templates come from a registry; dev-container support builds `devcontainer.json` on Docker/K8s/OpenShift; a private VS Code marketplace supports air-gapped use ([README](https://github.com/coder/coder), [docs](https://coder.com/docs)).

**Lifecycle verbs.** Create/start/stop/restart/delete via CLI and UI; workspaces "automatically shut down when not used"; connectivity is through a WireGuard tunnel, so no public ingress is needed. Coder Agents runs a native coding-agent loop **in the control plane** with no API keys in workspaces, with centralized model governance and audit ([README](https://github.com/coder/coder)).

**State and snapshot model.** Workspaces keep disk state (home volume) across stop/start; stopped workspaces stop costing compute. There is **no VM memory snapshot** and no fast disk-snapshot fork; provisioning is infrastructure-as-code driven ([README](https://github.com/coder/coder)). The project itself flags this as a problem: issue #26874, "Faster sandboxes: 2-second Firecracker/microVM Workspaces", states that "Coder workspaces, even with the current prebuilds system, take ~30s to provision and become healthy" and proposes supporting sandbox providers such as Daytona without Terraform in the loop ([issue #26874](https://github.com/coder/coder/issues/26874)).

**Networking.** Secure WireGuard tunnel between the client and workspace (plus SSH, web terminal, port forwarding over that tunnel); self-hosted control plane with PostgreSQL; external access URL for the deployment ([README](https://github.com/coder/coder)).

**Agent integration.** Coding agents (Claude Code, Codex, OpenCode, …) run isolated in Coder workspaces via registry modules; Coder Agents runs the loop server-side; AI Gateway centralizes auth, auditing, and cost; VS Code/JetBrains plugins ([README](https://github.com/coder/coder)).

**Fleet and self-hosting.** Fully self-hosted, AGPL-3.0 core with paid premium features; multi-host by design (Kubernetes, Nomad, VMs); validated architectures for sizing; 16.8k stars, active ([README](https://github.com/coder/coder), [pricing](https://coder.com/pricing)).

**User complaints (anecdotal).** The 30 s provisioning latency is documented by Coder's own issue ([#26874](https://github.com/coder/coder/issues/26874)); 1,185 open issues overall is a maturity/backlog signal, not a specific complaint list. The structural critique implied by the issue: Terraform-in-the-loop is heavyweight for agent-speed workloads.

**Pluto-relevant.** Coder is pluto's "fleet" reference minus the VM model: self-hosted, multi-host, persistent workspaces, idle stop, WireGuard access. Its own team is looking at microVMs precisely because provision-and-healthy in 30 s is too slow for agents. Pluto should treat "seconds to a usable machine" as the bar, with disk-only resume on the fast path.

---

## 9. Devpod

**Image/build pipeline.** Devcontainer-based (`devcontainer.json`, DevContainer features), with automatic language detection and templates when none exists; prebuilds are a first-class feature; credentials and dotfiles sync into workspaces ([README](https://github.com/loft-sh/devpod), [what is DevPod](https://devpod.sh/docs/what-is-devpod)).

**Lifecycle verbs.** `devpod up` creates and starts; workspaces "can be stopped and restarted without losing state"; providers add auto-inactivity shutdown; stop/delete are documented flows ([workspaces](https://devpod.sh/docs/developing-in-workspaces/what-are-workspaces), [architecture](https://devpod.sh/docs/how-it-works/overview), [README](https://github.com/loft-sh/devpod)).

**State and snapshot model.** The container/workspace persists across stop/start ("install additional programs or change configuration without reconfiguring"); no snapshots, no memory, no fork ([workspaces](https://devpod.sh/docs/developing-in-workspaces/what-are-workspaces)). **Uncertainty:** whether a stopped workspace's disk lives on the provider VM or a portable volume varies by provider; the docs consulted don't specify.

**Networking.** Client-only, client-agent architecture: DevPod deploys an agent to the host machine and to the container; the agent runs a gRPC/SSH server over a provider-specific "tunnel" (instance connect for AWS, the K8s control plane for k8s), and the local IDE attaches over SSH; port forwarding requires an active IDE/SSH session because the SSH server lives in the agent ([architecture](https://devpod.sh/docs/how-it-works/overview)).

**Agent integration.** Any IDE over SSH, VS Code/JetBrains, and the devcontainer ecosystem; no dedicated MCP/agent surface documented ([README](https://github.com/loft-sh/devpod)).

**Fleet and self-hosting.** 100% open source (MPL-2.0), client-only — "no need to install a server backend"; providers cover local, SSH machines, K8s, and clouds; the trade-off vs Coder is no central control plane ([README](https://github.com/loft-sh/devpod), [what is DevPod](https://devpod.sh/docs/what-is-devpod)). Stewardship is Loft Labs; the repo's last push was **2025-11-14** (~11 months before this research), which reads as low activity for a 15k-star project ([repo metadata](https://github.com/loft-sh/devpod)).

**User complaints (anecdotal).** Open issues include Neovim TUI breakage inside the container (#1187, 43 comments), AppImage not running on Fedora 41 (#1410, 24), "Community Devpod" (#1946, 24), plus Windows SSH/VSCode timeouts (#972) and rebuild-every-restart behavior with features (#904) ([issues](https://github.com/loft-sh/devpod/issues?q=is%3Aissue+sort%3Acomments-desc)). The dormancy plus these issues is the complaint signal.

**Pluto-relevant.** Devpod is the "no server backend" endpoint of the dev-environment spectrum, matching pluto's "self-host everything, no UI, existing clients attach". Its tunnel-per-provider complexity (and the requirement that port forwarding needs an active IDE session) is the part pluto avoids by using tailscale as the overlay.

---

## 10. GitHub Codespaces

**Image/build pipeline.** Docker container on a dedicated Linux VM; `devcontainer.json` (+ Dockerfile, features); default "universal" image if none; prebuilds for faster creation; dotfiles repo; machine types from 2 cores/8 GB RAM/32 GB storage to 32/128/128 ([overview](https://docs.github.com/en/codespaces/overview), [deep dive](https://docs.github.com/en/codespaces/about-codespaces/deep-dive)).

**Lifecycle verbs.** Create (browser, VS Code, CLI), stop, start/restart, rebuild, delete, `gh codespace stop`; connect without affecting running processes ([overview](https://docs.github.com/en/codespaces/overview), [lifecycle](https://docs.github.com/en/codespaces/about-codespaces/understanding-the-codespace-lifecycle)).

**State and snapshot model.** Disk persists, memory does not: stopping "preserves saved changes"; uncommitted changes survive stop/start; rebuild clears everything outside `/workspaces`; a `POST`-create flow re-runs lifecycle scripts. Limits: **30 min default idle timeout**, **12 h maximum lifetime** regardless of activity, and inactive stopped codespaces are **auto-deleted after 30 days** by default. Storage is billed while a codespace exists, including stopped ([lifecycle](https://docs.github.com/en/codespaces/about-codespaces/understanding-the-codespace-lifecycle), [deep dive](https://docs.github.com/en/codespaces/about-codespaces/deep-dive)).

**Networking.** Automatic port forwarding from the container to the user; ports are private by default and can be shared per organization/public URL; no VPN into private networks ([deep dive](https://docs.github.com/en/codespaces/about-codespaces/deep-dive)).

**Agent integration.** Any tool inside the container (agents included); attach via browser/VS Code/CLI; the container is the agent environment, not a service with an MCP surface ([deep dive](https://docs.github.com/en/codespaces/about-codespaces/deep-dive)).

**Fleet and self-hosting.** GitHub-managed only; no self-hosting; billing is metered monthly with quotas and spending limits ([overview](https://docs.github.com/en/codespaces/overview)).

**User complaints (anecdotal).** The docs' own constraints are the pain: 12 h max lifetime, 30-day deletion, storage charges for stopped environments, and quota exhaustion blocking usage. These are contract terms, not bug reports; I did not find a rigorous complaint corpus worth citing beyond them.

**Pluto-relevant.** Codespaces is the polish benchmark for attach UX (IDE, CLI, port forwarding) and the cautionary tale for pause semantics: stop kills processes, disk persists, and an external service decides when your machine is deleted. Pluto's "paused indefinitely until explicitly killed" is the inverse and a real differentiator for durable personal machines.

---

## 11. Cross-cutting patterns

1. **Everything is a microVM now.** Firecracker (E2B, Lambda, Handler, Hotcell), libkrun (microsandbox), Cloud Hypervisor (Arrakis). The VMM choice is no longer differentiation; it is a build-vs-borrow decision.
2. **Everything is built from Dockerfile/OCI.** E2B, microsandbox, Arrakis, Handler, Lambda MicroVMs, Docker Sandboxes, Devpod, Codespaces all start from container images or devcontainers. pluto's bst/OCI image question is an integration detail, not a novelty.
3. **Snapshot/resume is the serverless agent contract.** The systems that feel fast resume snapshots instead of booting kernels (E2B, Lambda MicroVMs, microsandbox full snapshots, Arrakis). The systems that don't (Coder, Devpod, Codespaces, Handler, Docker Sandboxes) feel like "machines" and pay boot/provision costs — Coder's own issue says ~30 s is too slow.
4. **Memory is the expensive part.** E2B's fast path, Lambda's suspend, microsandbox full snapshots, and Arrakis snapshots all carry a memfile; E2B documents that pause cost scales with RAM and dirty pages (4 s/GiB) and that its disk-only mode cannot auto-resume and needs journal replay. Disk-only pause is a **first-class cheaper mode**, not a synonym for pause.
5. **Fork is the "rare" primitive.** E2B (100/call), microsandbox (CoW memory), Arrakis (snapshot/restore), Handler (worktree/VM clone). All are memory-inclusive or worktree-level; nobody offers disk-only copy-on-write *fork into another host*.
6. **Agent integration converged on MCP + exec/SSH APIs.** E2B has an MCP gateway; microsandbox an MCP server; Docker an MCP gateway; Arrakis an MCP server; Handler an MCP registry; Coder agents. MCP is table stakes now.
7. **Secret hygiene converged on "keys never enter the sandbox".** E2B, microsandbox, Docker Sandboxes, and Hotcell all substitute credentials at a host boundary. Even with no trust model, this pattern is worth copying for hygiene.
8. **Self-hosting splits into *single machine* and *fleet*.** Single-machine is common and real (E2B Embed, microsandbox local, Handler, Hotcell, Arrakis, Devpod). Fleet self-hosting is rare and heavy (E2B full stack needs Postgres/Redis/ClickHouse/object storage; Coder needs Terraform + Postgres + orchestrators). Nobody offers a *fleet of personal durable machines without a consensus service*.
9. **No one keys machines by git worktree as a product identity.** Codespaces/Coder/Devpod clone repos per workspace; Handler's worktree fork is UI-level; Docker's clone mode is per-sandbox. pluto's "box = (project, branch) worktree" is the nearest thing to unique that this landscape surfaces.
10. **Complaints cluster around the same four things:** provisioning/start latency, snapshot failures/flakiness, cost/pricing surprises, and platform limits (12 h caps, 30-day deletion, 8 h sessions, 1 h cloud expiration).

---

## 12. Commodity / rare / absent

"Commodity" = multiple independent primary-source implementations, table stakes in 2026. "Rare" = one or two products ship it, usually as an option or only on one platform. "Absent" = no evidence in the surveyed set; possibly exists elsewhere.

| Property | Verdict | Evidence |
|---|---|---|
| Firecracker/libkrun/Cloud Hypervisor microVM per workload | **Commodity** | E2B, microsandbox, Arrakis, Handler, Hotcell, Lambda MicroVMs, Docker Sandboxes |
| Dockerfile/OCI image pipeline for sandboxes | **Commodity** | E2B templates, microsandbox OCI, Arrakis Dockerfile rootfs, Handler, Lambda, Docker, Devpod, Codespaces |
| Memory+disk pause/resume | **Commodity** | E2B, microsandbox full snapshots, Arrakis, Lambda suspend, Handler clone (no memory), Hotcell none |
| Disk-only pause/resume as a designed mode | **Rare** | E2B `keepMemory:false` (documented as lighter, reboot-on-resume, no auto-resume); microsandbox disk snapshots (default for `snap`); Coder/Devpod/Codespaces "stop = disk persists"; Lambda suspend is memory+disk; Docker/Handler stop only |
| Fork of a live machine | **Rare** | E2B (≤100/call), microsandbox (`fork`, `--cow-mem`), Arrakis snapshot/restore, Handler worktree clone |
| Snapshot artifacts as diffs against a base, in object storage | **Rare** | E2B (mem/rootfs diffs + `.header` chains); microsandbox (`--since` export, layered disks) but local |
| Content-addressed chunk store for disk state shared across boxes and hosts | **Absent** | E2B diffs per snapshot chain; microsandbox local layers; Lambda opaque AWS storage; no CAS semantics documented anywhere surveyed |
| Pause a box on host A, resume its disk on host B | **Absent** | E2B stores snapshots in object storage but prefers the origin node; microsandbox archives are manual; Coder/Codespaces pin workspaces; no lease/handoff semantics documented |
| Machine identity = git worktree (project, branch) | **Absent as a product primitive** | Handler forks worktrees in the UI; Docker clone mode; everyone else clones a repo into a session |
| Single-machine whole-stack self-host | **Rare-to-commodity** | E2B Embed, microsandbox local, Handler, Hotcell, Arrakis, Devpod (client-only); Coder needs a server; Docker local is per-machine; Lambda managed |
| Multi-host self-hosted fleet control plane | **Commodity but heavy** | E2B (API+orchestrators+Postgres/Redis/ClickHouse), Coder (Terraform+Postgres+K8s/Nomad); both assume a control plane / scheduler |
| Fleet without a consensus service; lease a paused box to a host | **Absent** | No surveyed system leases paused machines between hosts |
| MCP integration | **Commodity** | E2B MCP gateway, microsandbox MCP server, Docker MCP gateway, Arrakis MCP server, Handler MCP registry |
| SSH attach and exec/filesystem APIs | **Commodity** | All except Codespaces/Devpod expose it indirectly; E2B SSH over WebSocket proxy, microsandbox native SSH/SFTP |
| Egress allowlists and host-side secret substitution | **Commodity among agent sandboxes** | E2B, microsandbox, Docker Sandboxes, Hotcell; Handler minimal |
| "No trust model" — machines for one person, isolation for lifecycle/hygiene, not security | **Absent by construction** | Every product's pitch is security/isolation; the dev-environment tools serve trusted developers but still run multi-tenant control planes |
| Git as the only sync layer between client and machine | **Absent** | Everyone else syncs files/volumes or clones at creation; Handler/Codespaces/Coder use git among other channels |

---

## 13. Implications for pluto's unique claim

**What is no longer claimable.** "MicroVM sandboxes", "snapshot/resume", "fork", "MCP for agents", "Dockerfile images", "egress policy" are all commodity. Building any of them is necessary work, not differentiation. Memory snapshots in particular are a trap for pluto's stated design: they add memfile diffing, UFFD, page-optimization phases, and restart/reinit hazards, and the settled decision is already disk-only. E2B's own docs are the best argument: its filesystem-only path needs freeze/thaw hooks, journal replay, and a retry/degrade state machine, and still cannot auto-resume on traffic — complexity pluto can design away by never having memory state to preserve.

**What the landscape leaves open.** The combination pluto intends is absent from every system surveyed:

1. **Box = worktree.** A machine whose identity is `(project, branch)` and whose lifecycle is the branch's, not a session's. Handler's worktree fork is the only product hint of this; nobody manages machines *keyed by* worktrees across a fleet.
2. **Disk-only pause as the only pause.** One state model, no memory mode, no mode-specific resume semantics. Cheaper to build and reason about; pause cost tracks dirty blocks, not RAM (contrast E2B's 4 s/GiB).
3. **Content-addressed disk state in a self-hosted bucket.** No surveyed system does CAS dedup of disk state; E2B does per-snapshot diff layers. This is pluto's genuinely novel storage claim, and it is the part that makes cross-host resume cheap.
4. **Lease, not scheduling.** Hosts pull/lease boxes; no consensus service, no placement database. Every fleet system surveyed centralizes placement (E2B best-of-K, Coder/Nomad/K8s). With one personal operator and no trust model, leases can be simple and pull-based.
5. **Git-only sync.** No product does this. It matches the worktree identity: code moves by push/pull; the box's disk carries the build caches and uncommitted work, and results return by git.
6. **No trust model, no UI.** Inverse of the market. Pluto can use SSH/HTTP/MCP and existing clients (tailscale, opencode, T3 Code) instead of control-plane UI and policy engines.

**Suggested one-line claim for humans (candidate, not decision):** *pluto is a personal fleet of durable machines — one per git worktree, paused to disk-only state in your own bucket, leased to whichever host you have, with no memory snapshots, no consensus service, and no cloud.*

**Design lessons worth importing (with sources).**
- Model the pause state machine explicitly, including **backpressure and refusal**: E2B surfaces `ServiceBusy` (503) and retries, degrades to fs-only after a ~2-minute overstay budget, and uses detached time budgets (80 s pause, 95 s Redis TTL) so a deploy can't cut a snapshot ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md), [persistence](https://docs.e2b.dev/sandbox/persistence.md)).
- **Quiesce then snapshot**: freeze/thaw hooks in the guest; on cold boot of an unfrozen rootfs, journal-only `e2fsck`; document that unflushed writes are lost (crash-consistency, not clean-shutdown, semantics) ([filesystem-only](https://docs.e2b.dev/sandbox/filesystem-only-snapshots.md), [architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)).
- **Snapshot typing and lineage**: microsandbox's disk vs full, groups/heads, delta export/import, and guest-flush policies are the cleanest public vocabulary for the disk-state model pluto needs ([snapshots](https://docs.microsandbox.dev/sandboxes/snapshots)).
- **Optimize the boot path by recording what a boot touches** (E2B's optimize phase) — directly applicable to pluto's disk-only cold boot from S3 chunks ([architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)).
- **Keep secrets host-side** even without a trust model (Hotcell/Docker/E2B pattern) for hygiene and accidental-leak prevention ([Hotcell](https://github.com/sinameraji/hotcell), [Docker](https://docs.docker.com/ai/sandboxes/architecture/)).
- **Session continuity without memory snapshots**: Handler's tmux persistence and microsandbox's detached sandboxes show the expected UX ("my terminal is where I left it"); pluto should have an explicit answer (reattach via SSH/tmux in the guest on resume) rather than letting users expect process continuity ([Handler](https://github.com/Launchable-AI/handler.dev), [microsandbox](https://docs.microsandbox.dev/sandboxes/lifecycle)).

**What to avoid (learned from complaints).** Provisioning latency (Coder's 30 s problem) [issue](https://github.com/coder/coder/issues/26874); host-local snapshot coupling (Arrakis' tap/IP restore failures) [issues](https://github.com/abshkbh/arrakis/issues); control planes that require Postgres+Redis+ClickHouse for a personal fleet (E2B's full stack); multi-backend adapter sprawl (Handler's eight adapters); mandatory accounts for local use (Docker Sandboxes); and platform-deletion semantics for paused state (Codespaces' 30-day purge, Docker cloud's 1-hour expiry).

---

## 14. Open questions this raises (for the map)

1. **Disk-state artifact.** Whole-image per box vs chunk-level CAS: what exactly gets uploaded on pause, how are chunks keyed (content hash, box, generation?), and how does GC/prune work when branches are deleted? No surveyed system does CAS disk state, so this must be designed from bst-CAS first principles.
2. **Quiesce protocol.** Which hooks freeze/thaw the box before a pause upload (envd-style agent? a tiny guest agent?), and what are the documented crash-consistency semantics for a cold resume? E2B's freeze + journal-replay shape is the reference.
3. **Pause backpressure.** What does pluto do when a host is mid-upload and another pause arrives for the same box, or when a lease-holder dies mid-pause? A refusal state with retry/degrade rules needs to be in the public box state machine.
4. **Resume locality.** If any host can lease a box, what does the fast path look like with an empty local cache? Does the origin host keep a cache lease, and how are S3 reads parallelized/lazy during boot (E2B serves mem faults lazily; pluto must do this for blocks)?
5. **Worktree semantics vs shared caches.** Map says box = worktree and per-project caches are shared across a project's boxes. In a disk-only world, what is inside the box disk vs the shared cache, and how do worktrees share `.git` across boxes on different hosts when git is the only sync layer?
6. **Agent hosting surface.** Do agents run inside the box (Claude Code/opencode inside, like E2B/Docker/Handler) or outside driving it over SSH/MCP (like Lambda's external loop)? M0 probably needs one clear answer.
7. **Networking surface for M0.** Tailscale-only reachability with SSH, or per-box published ports (microsandbox-style loopback publishing) for HTTP? The landscape suggests "loopback publish + overlay" is the least surprising.
8. **Minimum viable fleet.** The landscape supports a single-machine first form (E2B Embed, Handler, Hotcell, microsandbox); does M0 need any fleet/lease machinery at all, or just one host + bucket + CLI? (This research argues yes for M0, with the lease protocol designed but deferred.)
9. **Box state machine verbs.** Candidate public verbs, drawn from the strongest systems: `create/start`, `pause` (disk-only), `resume`, `kill/rm`, `fork` (optional), `exec/attach`. Decide whether "hibernate" and "snapshot" are distinct from "pause" in pluto's vocabulary (microsandbox distinguishes disk vs full; E2B distinguishes pause vs snapshot vs fork).
10. **Pause durability guarantees.** Do we promise "paused indefinitely" (E2B) and no automatic deletion (Codespaces does the opposite)? If yes, state it and make GC explicit.

---

*Single-file deliverable for ticket #3. All primary-source links above were retrieved on 2026-10-04; page contents may drift. Claims about shipping behavior are the vendors' own unless labelled as independent or anecdotal.*
