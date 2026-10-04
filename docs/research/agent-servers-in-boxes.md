# Hosting agent servers in boxes

Research for pluto ticket [#8](https://github.com/Siddhj2206/pluto/issues/8), 2026-10-04. Question: what does a box need to host an agent server, and how do existing clients attach and resume across pause? Pluto pauses disk-only: the kernel and every process die, only the filesystem survives.

Studied at these revisions (cloned or fetched 2026-10-04):

- opencode `907b3bc518fa48e90e8ec24dd327d13eee71c36c` (`sst/opencode`, now `anomalyco/opencode`)
- T3 Code `d60a71ef6e371d8b345ecb1f7575117d675187c7` (`pingdotgg/t3code`)
- Codex CLI `afb436df8b70bb5bc57b86d9a3e829968988cd21` (`openai/codex`)
- First-party docs for opencode, Claude Code, T3 Code, tmux, mosh, systemd/loginctl, cloud-init

## The one table

| State | Survives pause? | Where it lives |
| --- | --- | --- |
| Kernel, processes, PTYs, sockets | No | RAM |
| In-flight agent turn | No; transcript keeps a partial/interrupted marker (opencode/Claude documented, Codex unverified) | Server RAM + disk |
| opencode sessions, messages, parts, permissions | Yes | SQLite `~/.local/share/opencode/opencode.db` |
| opencode PTY output buffers | No (memory, 2 MiB cap, 25 exited sessions retained per process lifetime) | opencode server RAM |
| T3 threads, event log, projections, checkpoints | Yes | `~/.t3/userdata/statev2.sqlite` + hidden git refs |
| T3 terminal scrollback | Yes, text only; the shell process is gone | History files, capped at 5,000 lines / 8 MiB |
| Claude Code conversation + tool history | Yes | `~/.claude/projects/<project>/<session-id>.jsonl` |
| Codex conversation + rollout | Yes | `~/.codex/sessions/` JSONL + state SQLite |
| tmux sessions/windows/panes | No | tmux server process; nothing on disk |
| mosh connection | No | `mosh-server` process |
| SSH host keys, authorized_keys, agent config dirs | Yes if on the disk image | `/etc/ssh`, `~/.ssh`, `~/.claude`, `~/.codex`, … |

The pattern across every system: **conversation state is durable on the box's disk; process state is not**. Every client reconstructs the live view from the server after it restarts, and no system replays a turn that was running when the process died — they mark it interrupted and expect the next prompt (or a provider-native resume) to continue.

---

## 1. opencode

### 1.1 Architecture

`opencode` starts a TUI and a server; the TUI is a client that talks to the server over HTTP + SSE. `opencode serve` starts the standalone headless server, and `opencode attach <url>` attaches a TUI (or the minimal `--mini` interface) to an already-running server, with `--continue`, `--session <id>`, and `--fork` options. The server publishes an OpenAPI 3.1 spec at `/doc`, a global SSE stream at `/event`, and session endpoints under `/session`. Default bind is `127.0.0.1` port `4096` (falls back to a random free port); basic auth via `OPENCODE_SERVER_PASSWORD`. `opencode web` serves a browser UI from the same server and the docs explicitly show running the web server and attaching a TUI to it simultaneously "sharing the same sessions and state".

Sources: `packages/opencode/src/cli/cmd/serve.ts`, `attach.ts`, `run.ts` (attach option), `packages/opencode/src/cli/network.ts`, `packages/opencode/src/server/server.ts` (4096 fallback), [opencode server docs](https://opencode.ai/docs/server/), [opencode web docs](https://opencode.ai/docs/web/).

### 1.2 Where session state lives

Global paths come from XDG: data `~/.local/share/opencode` (or `$XDG_DATA_HOME/opencode`), state `~/.local/state/opencode`, config `~/.config/opencode`, logs under the data dir. Session/message/part rows are SQLite in `~/.local/share/opencode/opencode.db` (override `OPENCODE_DB`). The schema has `session`, `message`, and `part` tables with timestamps, cost/token counters, revert state, and permission rulesets. A one-time migration path converts the older JSON-file storage (`~/.local/share/opencode/storage/…`) into the database; some artifacts still use JSON storage (for example session diffs).

Sources: `packages/core/src/global.ts`, `packages/core/src/database/database.ts` lines 44–53 (`opencode.db`), `packages/core/src/session/sql.ts` (schema), `packages/core/drizzle.config.ts`, `packages/opencode/src/storage/storage.ts` (JSON layout + migrations), `packages/core/src/data-migration.sql.ts`.

### 1.3 Killing the server and restarting it

- **Sessions reload.** The server loads instances per request (`x-opencode-directory` header) and reads sessions from the database; nothing about a session is process-local except execution.
- **Clients reconnect.** The TUI event client runs an SSE loop with exponential backoff from 1 s to 30 s (`retryDelay = 1000`, `maxRetryDelay = 30000`) and reconnects when the stream drops; `validateSession` runs before `attach` so an attach against a dead server fails fast. A browser tab hitting `opencode web` behaves the same way.
- **In-flight work is marked, not resumed.** At the start of a run, the runner walks the session history and fails every tool call still in `pending`/`running` with `"Tool execution interrupted"`. If a provider turn is interrupted it fails unsettled tools and the assistant message with `"Provider turn interrupted"`. A restarted server does not restart the run by itself; the next user prompt drives a new turn, and the model sees the failed tool part as history.
- **PTYs die.** opencode's terminal feature is an in-memory map of live PTY processes with a 2 MiB output buffer per session and retention for 25 exited sessions *within the server process lifetime*. The API only promises resumed cursors for a running server; nothing is written to disk.

Sources: `packages/opencode/src/cli/cmd/attach.ts` (`validateSession`), `packages/tui/src/context/sdk.tsx` (SSE reconnect/backoff), `packages/core/src/session/runner/llm.ts` (`failInterruptedTools`, "Tool execution interrupted", "Provider turn interrupted"), `packages/core/src/pty.ts` (buffer/exited limits, in-memory `Active` map), `packages/opencode/src/server/routes/instance/httpapi/handlers/pty.ts` (exited-session retention comment).

### 1.4 M0 implications for opencode

Run `opencode serve` (or `opencode web`) from a boot hook inside the box. On resume, clients attach and see every prior session; the interrupted turn shows failed tool parts; continuing means sending a new prompt or `opencode attach --continue`. Sessions, permissions, and models survive; PTY-based terminals and any tool processes do not.

---

## 2. T3 Code (agent harness control surface)

### 2.1 Architecture

T3 Code is an "agent harness control surface": web, desktop, and mobile clients control agents that live on a machine running the `t3` server. Its architecture doc is explicit: "T3 Code keeps execution in the environment that owns the workspace. Web, desktop, and mobile clients control it over authenticated RPC. A remote client must never substitute its own filesystem, provider credentials, or machine state for the environment's." Provider CLIs (Claude Code, Codex, Cursor, Grok, **opencode**, Antigravity) run as adapters behind the server; the opencode adapter owns opencode servers per thread (1.x) or per instance (2.x).

Sources: [README](https://github.com/pingdotgg/t3code/blob/d60a71ef6e371d8b345ecb1f7575117d675187c7/README.md), `docs/internals/overview.md`, `docs/internals/providers.md`.

### 2.2 Where state lives

- Server state: `~/.t3/userdata`, with a SQLite database `statev2.sqlite` (v1 used `state.sqlite`; there is an explicit v2 migration path). Uninstall leaves "your projects, threads, and settings under `~/.t3/userdata`".
- Durable orchestration: the event log is the source of truth. `EventSink` commits events, projections, the command receipt, and outbox effects in one transaction, and subscribers see events only after commit. Thread settlement is server-owned.
- Checkpoints: workspace state is captured under hidden git refs without adding commits to the user's branch (`CheckpointStore`).
- Terminal output: PTYs belong to the environment server, but scrollback is persisted to history files, capped at 5,000 lines / 8 MiB, and restored at startup from the bounded tail.
- Environment identity: "An environment keeps its ID across server restarts and endpoint changes."

Sources: `apps/server/src/config.ts` line 140 (`statev2.sqlite` under the state dir), `docs/user/background-service.md`, `docs/internals/overview.md`, `docs/internals/terminal-runtime.md`, `docs/internals/remote.md`.

### 2.3 Killing the server and restarting it

- **Clients reconnect.** One supervisor per environment owns transport retry: "Transient failures retry with jittered exponential backoff, capped at five minutes, that resets only after a connection stays up." A mobile background suspension replaces the session immediately; otherwise reconnection probes the established session. Cached projections stay readable offline and are not overwritten by older data on reconnect.
- **In-flight work is retired, not replayed.** "Effects tied to a lost provider process cannot simply replay; recovery retires them before admitting new work." The background-service doc is blunt: "Restarting interrupts running agent turns, terminals, and remote clients." Threads, history, and provider sessions persist; the run that was live does not.
- **Access survives.** Pairing authorizes a device for future connections; saved connections are client-local, server identity is stable; environments "can outlive several client releases."
- **Terminals**: history is restored, the shell process is not; a terminal restart is an explicit lifecycle operation.

Sources: `docs/internals/connection-runtime.md`, `packages/client-runtime/src/connection/supervisor.ts` (`RETRY_MAX_DELAY_MS = 300_000`), `docs/internals/overview.md`, `docs/user/background-service.md`, `docs/user/remote-access.md`, `docs/internals/terminal-runtime.md`.

### 2.4 Attach from a phone or another machine

Documented routes: direct pairing over LAN/tailnet (`t3 serve --host <tailnet-ip>`, `t3 pair` QR/link), Tailscale HTTPS (`t3 serve --tailscale-serve`, address `https://machine.tailnet.ts.net/`), desktop-managed SSH (starts/reuses a server and port-forwards), and T3 Connect (a hosted relay requiring a T3 account). For pluto — self-hosted, no trust model, tailnet-first — direct pairing over the tailnet or `--tailscale-serve` fit; T3 Connect routes through T3's cloud and is the one piece that is not self-hosted.

Sources: `docs/user/remote-access.md`.

### 2.5 M0 implications for T3 Code

Run `t3 serve` as a systemd user service in the box (`t3 service install`, linger required — see §5). Pair the phone once; pairing persists. On resume, the service starts again, mobile/web clients reconnect with backoff, threads are intact, and the interrupted run shows as retired. Terminals need an explicit restart.

---

## 3. Claude Code

### 3.1 Persistence model

- Transcripts are JSONL at `~/.claude/projects/<project>/<session-id>.jsonl`, written continuously as you work; `<project>` is the working-directory path with non-alphanumerics replaced. `CLAUDE_CONFIG_DIR` moves the root; `CLAUDE_CODE_PROJECT_DIR_NAME` names the project dir.
- Resume: `claude --continue` (most recent), `claude --resume [name|id|transcript-path]`, or `claude -p --resume <id>` for scripts/hooks. Resuming by ID searches the current project and then every other project on the machine.
- What a resumed session restores: full conversation including tool calls and results; model, agent, and (in terminal paths) permission mode; active goal; unexpired scheduled tasks. **"A tool that was still running when the previous process ended, for example in a crash, doesn't finish or run again when you resume. Claude sees the call marked as cut off before its result was recorded and is told to check whether it took effect before running it again."** Background subagents, background Bash, and workflows from the previous process appear as "didn't finish" notes and are not restarted; Claude reads them with the next prompt.

Sources: [Manage sessions](https://code.claude.com/docs/en/sessions), [CLI reference](https://code.claude.com/docs/en/cli-reference) (`--continue`, `--resume`, `--no-session-persistence`).

### 3.2 Remote access and process death

Remote Control (`claude remote-control`, `--remote-control`, `/remote-control`) lets claude.ai or the Claude app drive a session that stays local; it reconnects automatically after network drops. But: **"Local process must keep running. … If you close the terminal, quit the Desktop app or VS Code, or otherwise stop the `claude` process, the session goes offline until you bring it back. To keep a session running on a remote machine after you disconnect from SSH, start it inside `tmux` or `screen`."** After the server process is stopped, `claude remote-control` (same directory) brings its sessions back for about four hours; after that, a new session must be started. Remote Control routes through the Anthropic API and needs a claude.ai subscription — it is not self-hostable, though it degrades to plain transcript resume if unavailable.

Sources: [Remote Control](https://code.claude.com/docs/en/remote-control) (limitations, "Resume sessions after stopping the server"), [Manage sessions](https://code.claude.com/docs/en/sessions).

### 3.3 M0 implications for Claude Code

A Claude Code run inside a box survives pause as a transcript. On resume, a human or boot hook can bring it back with `claude --resume <id>` (or `--continue` in the repo) inside tmux; the cut-off tool call is marked and the next prompt continues. Remote Control is optional and vendor-cloud-bound.

---

## 4. Codex CLI

- `CODEX_HOME` selects the config directory; **"If not set, defaults to `~/.codex`."**
- Sessions are rollout files under `<CODEX_HOME>/sessions/` (archived ones under `archived_sessions/`), plus SQLite state inside `CODEX_HOME`: `state_5.sqlite` (state), `thread_history_1.sqlite`, logs, goals, memories, and a queue DB. Resume reads these.
- `codex resume [SESSION_ID] [--last]` continues a prior interactive session; a UUID or session name may be given, otherwise a picker opens. Non-interactive sessions are excluded from the picker by default (`--include-non-interactive` opts in), and there are `archive`/`queue` operations on saved sessions.
- What exactly happens to a tool call that was executing when the process died is not documented publicly; the rollout is an append-only event log, and resume replays the recorded session. Treat mid-turn semantics as unverified.

Sources: `codex-rs/utils/home-dir/src/lib.rs` (default `~/.codex`), `codex-rs/rollout/src/lib.rs` (`SESSIONS_SUBDIR = "sessions"`, `ARCHIVED_SESSIONS_SUBDIR`), `codex-rs/state/src/sqlite.rs` (DB filenames under `codex_home`), `codex-rs/cli/src/main.rs` (`Resume` command docs, `ResumeCommand`).

### 4.1 M0 implications for Codex

Same shape as Claude Code: config and rollouts are files/SQLite under `~/.codex` on the durable disk; restoring a session is `codex resume`. T3 Code's Codex adapter uses this native resume; standalone use would need a boot hook or the user to run it.

---

## 5. Terminals: sshd + tmux + mosh

### 5.1 tmux

- Client/server split; server and clients are separate processes communicating over a socket in `/tmp/tmux-UID/` (`-L` names it, default `default`).
- Sessions survive client disconnects: "Each session is persistent and will survive accidental disconnection (such as ssh connection timeout) or intentional detaching." `tmux attach` reattaches; `tmux new-session -d -A -s name` creates-or-attaches idempotently.
- The server's memory is the session: `kill-server` "kill[s] the tmux server and clients and destroy[ies] all sessions." A box pause is equivalent to killing the server — tmux itself persists nothing to disk. Reboot survival needs a boot hook that recreates the session, and scrollback is lost unless something else saves it.
- Optional: `tmux-resurrect` saves sessions/windows/panes/layouts and (opt-in) pane contents; `tmux-continuum` automates save/restore. It restores commands, not process state.

Sources: [tmux(1)](https://man7.org/linux/man-pages/man1/tmux.1.html), [tmux-resurrect](https://github.com/tmux-plugins/tmux-resurrect).

### 5.2 mosh

- Mosh logs in over SSH, starts `mosh-server` on the remote, then syncs terminal state over UDP (ports 60000–61000). It survives roaming across networks and client sleep; keystrokes get local echo.
- Durability lives in the connection, not the machine: "The client and server are executables run by an ordinary user and **last only for the life of the connection**." If the box pauses, `mosh-server` dies with it.
- Mosh syncs only the visible terminal state, so scrollback is incomplete by design; the documented workaround is running tmux (or screen) on the remote side. The canonical pattern is **mosh for the transport, tmux for session lifetime**.
- Phone clients exist: Blink (iOS, from the mosh project), Termux and JuiceSSH (Android), plus Chrome OS.

Sources: [mosh.org](https://mosh.org/) (usage, FAQ, technical info).

### 5.3 PTY handling

A PTY is a kernel object owned by a process pair. In this stack the owner is whichever long-lived process allocated it: the tmux server for tmux panes, `mosh-server` for a mosh connection, the opencode server for opencode terminals, the T3 server for T3 terminals. When pluto pauses a box the kernel dies, so every PTY is destroyed; on-disk text (T3 history files, opencode session transcripts) survives, but nothing can "reattach" to a process that is gone. Resume therefore always means **recreate the process, then let clients reattach**.

### 5.4 M0 implications for terminals

The box needs sshd (tailnet-reachable), tmux, and ideally mosh-server baked into the image. A boot hook creates the tmux session; phones and laptops attach with `ssh` or `mosh` and then `tmux attach`. tmux is the single canonical place where long-lived TUI processes belong (agent servers, shells, dev servers), because it is the one process layer the boot hook can restart deterministically.

---

## 6. What the guest needs at boot

Requirements, with the mechanism each one needs:

1. **Identity** — hostname, tailnet identity, SSH host keys, machine-id. These live on the durable disk today, but a fork or a resume on different hardware raises duplication questions (see Open questions). Per-boot facts (host address, tailscale auth) must be injectable *without* rewriting the shared disk.
2. **Auth** — `authorized_keys` for the user's laptop(s) and phone; provider credentials for agent CLIs (`~/.claude`, `~/.codex`, opencode auth, T3 provider setup). Because pluto has no trust model and boxes are keyed by worktree, these can be baked or provisioned once and then carried in the box disk.
3. **Binaries** — `openssh-server`, `tmux`, `mosh-server`, `git`, the agent CLIs (opencode, Claude Code, Codex, T3), and whatever each project's dev loop needs. Install scripts: opencode (`opencode.ai/install`), Claude Code (`claude.ai/install.sh`), Codex (`npm i -g @openai/codex` or the install docs), T3 (`curl -fsSL https://t3.codes/install.sh | sh`). Baking them into the box image keeps resume fast and network-independent.
4. **Repo checkout** — a git clone/fetch plus `git worktree` materialization of the box's (project, branch) at boot or first attach. Git is the only sync layer, so the boot hook is a plain `git fetch && git checkout`.
5. **Resume hooks** — a per-boot script that starts the agent server(s) and dev servers inside tmux, restarts LSPs, and (optionally) prints a "what was interrupted" note from the agent session stores.
6. **State directories on the durable disk** — `~/.local/share/opencode`, `~/.local/state/opencode`, `~/.config/opencode`, `~/.claude`, `~/.codex`, `~/.t3/userdata`, `~/.ssh`, `/etc/ssh`. Whatever is not under those paths is lost on pause.

Mechanisms for per-boot provisioning:

- **cloud-init NoCloud** is the standard injection layer. Config can come from a filesystem labeled `CIDATA` (seed ISO/vfat) or from a line configuration passed via kernel command line or DMI/SMBIOS serial (`ds=nocloud;s=file:///path/` or `…s=http://…`; QEMU example: `-smbios type=1,serial=ds=nocloud;s=http://10.10.0.1:8000/...`). `meta-data` must contain `instance-id`, and **changing the `instance-id` is what makes cloud-init treat a boot as a new first boot** for user-data purposes. That is the natural place to inject "this incarnation's" tailnet key, hostname, and authorized keys while keeping the content-addressed disk untouched.
- **systemd user services** with lingering: "If enabled for a specific user, a user manager is spawned for the user at boot and kept around after logouts. This allows users who are not logged in to run long-running services." The guest image can `loginctl enable-linger <user>` once; boot hooks become user units (`t3.service`, `opencode.service`, `tmux.service`) and run without an SSH session.
- **tmux** itself is the resume hook for interactive surfaces: `tmux new-session -d -A -s main -c ~/repo 'opencode serve …'`, plus windows for shells/dev servers.

Sources: [cloud-init NoCloud](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html) (sources, line config, instance-id semantics, QEMU example), [loginctl(1)](https://man7.org/linux/man-pages/man1/loginctl.1.html) (`enable-linger`), [tmux(1)](https://man7.org/linux/man-pages/man1/tmux.1.html), [T3 background service docs](https://github.com/pingdotgg/t3code/blob/d60a71ef6e371d8b345ecb1f7575117d675187c7/docs/user/background-service.md).

### What "seamless" can and cannot mean

Across all four agent systems, the resume contract is the same, and it is bounded:

- **Seamless**: sessions/threads/history are intact; a client that was disconnected reconnects by itself; the environment keeps its identity; the user sees the interrupted turn flagged and continues it with one message.
- **Not seamless**: no process is resurrected; in-flight turns never continue on their own; PTY scrollback (outside T3's persisted history) is gone; background Bash/subagents are not restarted. "Continue the agent automatically after resume" must be an explicit boot-hook choice that sends a prompt — not something the servers do.

---

## 7. How a phone or second machine attaches to the same session

- **Terminal surfaces (ssh/mosh + tmux)**: phone runs a tailnet-reachable SSH/mosh client (Blink/Termux/JuiceSSH), attaches to the box, `tmux attach`. Both surfaces see the same tmux session.
- **opencode**: phone browser opens `opencode web` over the tailnet (basic auth password), or another machine's TUI runs `opencode attach http://<box>:4096 --continue`. Both share the same sessions and state.
- **T3 Code**: install the iOS/Android app or web app, pair once over the tailnet (direct pairing or `--tailscale-serve`), pick the environment, and attach to the thread. Pairing survives restarts; the client supervisor retries while the box is down.
- **Claude Code**: Remote Control from the Claude app/web (vendor cloud relay, needs a subscription and an always-running local process) or plain `--resume` from another terminal.
- **Codex**: no first-party phone client; attach through a terminal/mosh surface and `codex resume`, or through T3/opencode adapters.

The box-side invariant for all of them: reachability is the tailnet's job, and the server identities (ports, passwords, pairing state, host keys) must live on the durable disk so reconnection works after pause.

---

## 8. M0 demo shape (what the ticket unblocks)

Smallest end-to-end that exercises pause/resume honestly:

1. Box image: sshd, tmux, mosh-server, git, opencode, tailscale; user with linger enabled; agent config dirs under the durable disk.
2. Boot hook: read per-boot config (seed drive / SMBIOS), join tailnet, `git fetch`/worktree checkout, then `tmux new-session -d -A -s main` running `OPENCODE_SERVER_PASSWORD=… opencode serve --hostname 0.0.0.0 --port 4096` (and/or `t3 serve`), reachable on the tailnet only.
3. Pause: kill the VM. Explain that all processes and PTYs die; disk survives.
4. Resume: boot the box; boot hook recreates tmux + servers. Laptop TUI/web and phone reattach; sessions are listed; the interrupted turn shows failed/cut-off tool calls; send one message to continue. `mosh`+tmux demonstrates the terminal path; `opencode attach` demonstrates the client path.
5. Optional flourish: T3 Code pairing from the phone over the tailnet, showing a thread surviving the pause.

Tests that matter for the demo: (a) session list is identical across restart; (b) reconnect happens without manual URL editing; (c) interrupted tool call is visibly marked, not silently dropped or re-run; (d) a new prompt after resume succeeds.

---

## Open questions (candidates for the map)

1. **Boot policy**: do boxes start agent servers eagerly at resume (costs a little CPU, makes attach instant) or lazily on first client connection? M0 can be eager; the fleet model eventually needs a policy.
2. **Binary provenance**: bake agent CLIs into the box image, install to a shared per-project cache, or mount from the host? Ties into the image pipeline and per-project cache decisions.
3. **Fork/move identity**: what happens to `machine-id`, SSH host keys, tailscale node identity, and `~/.t3` environment ID when a box is forked or resumed on another host? cloud-init only re-runs user-data when `instance-id` changes, so per-incarnation injection needs its own convention.
4. **One PTY owner or several**: should tmux be the canonical owner of all long-lived interactive processes (agent servers inside tmux), leaving opencode/T3 PTYs for ephemeral use only? Fewer owners makes the boot hook deterministic.
5. **Auto-continue semantics**: after resume, does the boot hook send an automatic "continue" to interrupted sessions, or is continuing always human? Claude/opencode/Codex all treat the interrupted turn as stopped by default.
6. **Auth for network surfaces**: opencode basic auth password + tailnet ACLs vs fronting every server with per-box SSH tunnels. Needs the networking ticket to settle.
7. **T3 Connect**: cloud relay conflicts with self-host-everything; confirm the demo uses direct tailnet pairing only.

---

## Sources

- opencode: [server docs](https://opencode.ai/docs/server/), [web docs](https://opencode.ai/docs/web/), source at `sst/opencode` commit `907b3bc518fa48e90e8ec24dd327d13eee71c36c` (`packages/core/src/global.ts`, `database/database.ts`, `session/sql.ts`, `session/runner/llm.ts`, `pty.ts`; `packages/opencode/src/cli/cmd/{serve,attach,run}.ts`, `cli/network.ts`, `server/server.ts`, `storage/storage.ts`, `session/session.ts`; `packages/tui/src/context/sdk.tsx`).
- T3 Code: [repo](https://github.com/pingdotgg/t3code) commit `d60a71ef6e371d8b345ecb1f7575117d675187c7` (`README.md`, `docs/user/remote-access.md`, `docs/user/background-service.md`, `docs/internals/{overview,connection-runtime,remote,terminal-runtime,providers}.md`, `apps/server/src/config.ts`, `packages/client-runtime/src/connection/supervisor.ts`).
- Claude Code: [Manage sessions](https://code.claude.com/docs/en/sessions), [Remote Control](https://code.claude.com/docs/en/remote-control), [CLI reference](https://code.claude.com/docs/en/cli-reference).
- Codex: [repo](https://github.com/openai/codex) commit `afb436df8b70bb5bc57b86d9a3e829968988cd21` (`codex-rs/utils/home-dir/src/lib.rs`, `codex-rs/rollout/src/lib.rs`, `codex-rs/state/src/sqlite.rs`, `codex-rs/cli/src/main.rs`).
- Terminals: [tmux(1)](https://man7.org/linux/man-pages/man1/tmux.1.html), [mosh.org](https://mosh.org/), [tmux-resurrect](https://github.com/tmux-plugins/tmux-resurrect).
- Guest boot: [cloud-init NoCloud](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html), [loginctl(1)](https://man7.org/linux/man-pages/man1/loginctl.1.html), [Tailscale downloads](https://tailscale.com/download).
