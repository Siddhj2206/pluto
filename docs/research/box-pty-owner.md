# The box PTY owner: herdr vs tmux (with zellij, dtach/abduco, Eternal Terminal, mosh)

Facts gathered 2026-10-04. Primary sources only: vendor docs and source, project repos and release
APIs, man pages, plus a bounded local measurement of herdr 0.9.3 on the pluto first host (clearly
marked). Quotes are verbatim. herdr was read at `v0.9.3` (released 2026-09-29); zellij at `v0.45.1`;
tmux at 3.7c; repo metadata from the GitHub API on 2026-10-04.

---

## 1. What is being decided

pluto's guest-environment decision ships **tmux as the canonical owner of interactive PTYs in the
base shape** ([#14](https://github.com/Siddhj2206/pluto/issues/14)). The question here: should an
**agent box** keep tmux, or adopt an agent-aware multiplexer — herdr being the lead candidate?

Hard constraint from the map and [#13](https://github.com/Siddhj2206/pluto/issues/13):

- Agent servers run as **systemd user units** inside boxes; clients attach from outside.
- **Busy signals come from agent APIs** (opencode `session/status`, T3 `activityRunStatus`), not the
  multiplexer.
- **pluto's lifecycle must never depend on the multiplexer.** Pause kills all processes; disk
  survives. The multiplexer is a convenience layer; its absence degrades to plain SSH.

---

## 2. herdr

**What it is.** A background *runtime* that owns real PTYs; every UI (TUI, CLI, plain SSH) is a
client of the server ([compare](https://herdr.dev/compare), [how-to-work](https://herdr.dev/docs/how-to-work/)):
"the remote server owns the running panes and sends their terminal content and session state over
SSH." One Rust binary; Apache-2.0; v0.9.3 stable 2026-09-29; repo created 2026-03-27; 42,192 stars,
3,285 forks, 335 open issues as of 2026-10-04 ([GitHub API](https://api.github.com/repos/herdrdev/herdr)).
The Linux x86_64 release asset is **29,962,088 bytes** and `static-pie linked`
([release v0.9.3 asset](https://github.com/herdrdev/herdr/releases/tag/v0.9.3), locally verified).

**Architecture and headless operation.** The server is a normal background process; the CLI runs it
explicitly: "`herdr server` runs the headless server explicitly. Use it for supervised or
service-style setups." ([CLI reference](https://herdr.dev/docs/cli-reference/)). Named sessions each
get their own socket; the default socket is `~/.config/herdr/herdr.sock`
([Socket API](https://herdr.dev/docs/socket-api/)). Detach/reattach keeps processes alive; none of
this needs a terminal on the server side.

**Restore semantics after a restart.** The docs are explicit — processes never survive, only state
is reconstructed ([Session state and restore](https://herdr.dev/docs/session-state/)):

| Case | Processes keep running | Layout returns | Recent screen returns | Agent conversation resumes |
| --- | --- | --- | --- | --- |
| Detach and reattach | Yes | Yes | Yes, from the live terminal | Yes, because the process never stopped |
| Server restart | No | Yes | Only with pane screen history | Only with native agent session restore |

- "Snapshot restore does not preserve running shells, servers, tests, or arbitrary processes. Panes
  that cannot use a stronger restore path come back as new shells in their saved directories."
- Pane screen history is **off by default** (it can contain secrets: "pane output can include
  secrets, tokens, prompts"); enable with `[experimental] pane_history = true`.
- Native agent session restore is **enabled by default** (`[session] resume_agents_on_restore =
  false` to disable). Supported agents get their native conversation relaunched, e.g. OpenCode
  integration `5` → `opencode --session <id>`, Claude Code `6` → `claude --resume <id>`, Codex `5` →
  `codex resume <id>` ([session-state](https://herdr.dev/docs/session-state/),
  [integrations](https://herdr.dev/docs/integrations/)).
- Resume is **lazy**: "After a client attaches and provides terminal size and theme context, Herdr
  resumes eligible restored agent panes across workspaces and tabs without waiting for each pane to
  be focused." A headless restart alone does not necessarily relaunch agents.
- On systems with systemd-logind, herdr listens for the shutdown warning and "requests a short delay
  while it saves and stops"; forced shutdown, power loss, or killed processes are explicitly not
  covered.

**What I measured locally (herdr 0.9.3, first host, 2026-10-04).** Commands and outputs are
reproduced in §10. Summary:

- `herdr server` ran headless with no TTY; CLI created a workspace, split panes, ran a command, read
  output, and stopped the server, all through the Unix socket.
- Server RSS: **17,124 KB fresh**, **21,108 KB after a pane + shell** (VSZ ≈ 70 MB).
- `session.json` (schema `"version": 3`) appeared **within 10 s** of layout changes and contains
  workspaces, tabs, the BSP layout, pane cwds, focus. Pane *content* is not saved by default.
- Clean `herdr server stop` then restart restored the workspace label and both panes as **fresh
  shells**; the pre-stop marker was not replayed.
- `kill -9` + restart restored only the last-saved layout; a workspace created after the last save
  was gone. The docs do not state the autosave interval; observed here ≤10 s.
- With `unshare -rn` (no network), the server still started, accepted CLI commands, ran a pane, and
  stopped cleanly.

**Socket / CLI API.** Newline-delimited JSON over a local Unix socket (named pipe on Windows);
stable across client and server builds via "endpoint generation" negotiation; "JSON API clients
should ignore unknown fields and handle unsupported methods as normal errors"
([Socket API](https://herdr.dev/docs/socket-api/)). Method areas include `pane.*` (`split`,
`send_text`, `send_keys`, `read`, `wait_for_output`), `agent.*` (`list`, `read`, `prompt`, `wait`,
`start`, `attach`), `workspace.*`, `tab.*`, `worktree.*`, `events.subscribe`/`events.wait`,
`layout.export`/`layout.apply`, and plugin/notification calls. `agent.wait` is "server-owned and
event-driven" and observes semantic state (`idle|working|blocked|done|unknown`). CLI wrappers mirror
the socket (`herdr agent wait w1:p1 --until blocked`, `herdr pane read w1:p1 --source
recent-unwrapped`). Panes are exported the environment (`HERDR_PANE_ID`, `HERDR_SOCKET_PATH`).

**MCP.** No first-party MCP server or MCP documentation exists in the repo; a GitHub code search for
"mcp" in `herdrdev/herdr` matches only Claude Code's own MCP-related screen-detection rules in the
changelog/manifests, not a herdr MCP surface. herdr's own agent-integration route is an "Agent skill
file" and CLI/socket calls. An MCP bridge **could** be built over the socket API, but it would be a
pluto (or third-party) artifact, not a herdr feature, and it would be version-pinned to the socket
schema.

**Remote attach.** Three paths ([how-to-work](https://herdr.dev/docs/how-to-work/),
[persistence-remote](https://herdr.dev/docs/persistence-remote/)):

1. `ssh you@server` then `herdr` — the tmux-style path; the server is remote and clients are just
   terminals. Works on phones via any SSH client.
2. `herdr --remote workbox` (or `ssh://user@host:port`) — the **local UI** draws while the **remote
   server** owns panes and sends terminal content over SSH. Auth is "your normal OpenSSH
   authentication"; keepalives and a control socket are added through a generated SSH config
   (`[remote].manage_ssh_config = false` opts out).
3. Saved machines (`herdr machine add`) and `herdr --machine <label>` forwarding, where "Requests and
   responses travel through the JSON API over non-interactive SSH".

Remote setup may install/update herdr on the remote host; "For Linux and macOS hosts, Herdr can copy
the current local binary when the platforms match. ... it downloads the matching release asset for
the current client version from `https://herdr.dev/latest.json`." Non-interactive runs "fail instead
of modifying the host"; `HERDR_REMOTE_BINARY` pins a local binary. All paths are plain SSH/TCP — no
UDP, no relay, no account.

**Offline / no-account.** No account or hosted control plane is required for local or SSH attach;
the cloud page is a waitlist for a future product. But two background checks default to on
([config reference](https://herdr.dev/docs/config-reference/), fetched from the repo-generated
`config-reference.json`):

- `update.version_check` (default `true`): "Check herdr.dev for new Herdr versions in the
  background."
- `update.manifest_check` (default `true`): "Check herdr.dev for remote agent-detection manifest
  updates in the background. Bundled manifests and local overrides still apply."

Both can be disabled (`[update] version_check = false`, `manifest_check = false`); bundled manifests
and local overrides still work. The offline run above showed no startup failure — only background
checks are affected.

**Footprint / headless.** 30 MB static binary; ~17–21 MB RSS for the server in the measurement.
Runs headless and under a supervisor. The server does not need a terminal, but TUI clients do.

**Governance / business model.** "© 2026 Herdr, Inc." (website footer). Herdr raised a **$6M seed
led by Bessemer Venture Partners**, with Y Combinator, e2vc, and angels including Tobi Lütke and
Dane Knecht ([blog, 2026-09-08](https://herdr.dev/blog/herdr-raised-a-seed/); [YC post,
2026-08-06](https://herdr.dev/blog/herdr-is-joining-y-combinator/)). **Herdr Cloud is "coming soon"**
with a waitlist: "Your machines. One Herdr. No SSH setup. ... You bring the machines. We connect
them." ([herdr.dev/cloud](https://herdr.dev/cloud/)). The repo is Apache-2.0, but contributions are
gated: "Herdr does not accept unsolicited pull requests"; implementation PRs are limited to
maintainers and a curated **`.github/APPROVED_CONTRIBUTORS`** list (76 entries), maintained by
**three maintainers** (`ogulcancelik`, `kangal-bot`, `JJLiebig`) per
[CONTRIBUTING.md](https://github.com/herdrdev/herdr/blob/master/CONTRIBUTING.md) and
[.github/MAINTAINERS](https://github.com/herdrdev/herdr/blob/master/.github/MAINTAINERS). The
sponsorship program is closed ([SPONSORS.md](https://github.com/herdrdev/herdr/blob/master/SPONSORS.md)).
Release cadence is very fast: 0.9.0 (2026-09-07), 0.9.1 (09-16), 0.9.2 and 0.9.3 (09-29), plus
frequent preview builds.

---

## 3. tmux (baseline)

**Model.** Server/client; sessions, windows, panes; the server process is the only owner of live
state. Restore is not built in: the FAQ answers "tmux says no sessions" with "Check if tmux is still
running with pgrep or ps. If not, then the server probably crashed or was killed and the sessions
are gone." ([tmux FAQ](https://github.com/tmux/tmux/wiki/FAQ)). tmux is part of OpenBSD, released
roughly every six months with it; latest **3.7c, 2026-08-17** ([releases](https://github.com/tmux/tmux/releases)),
license **ISC** ([repo API](https://api.github.com/repos/tmux/tmux), `COPYING` in the tree). The
first host carries tmux 3.7c; package size ≈1.47 MB.

**Control surface.** Commands over the client/server socket: `send-keys`, `capture-pane`,
`wait-for`, `list-panes -F`, `run-shell`, `if-shell`, format variables (e.g.
`#{session_activity}`), user options for storage. **Control mode** (`tmux -C`/`-CC`) is a "text-only
protocol" that "can easily be parsed and used over ssh(1)"; it emits `%output` notifications,
lifecycle notifications, flow control, and format subscriptions
([Control Mode](https://github.com/tmux/tmux/wiki/Control-Mode)). It has no notion of an agent.

**Restore ecosystem.** Third-party plugins only. `tmux-resurrect` "saves all the little details from
your tmux environment so it can be completely restored after a system restart": sessions, windows,
panes, order, cwd, exact layouts, active pane, and optionally pane contents; "Only a conservative
list of programs is restored by default" ([tmux-resurrect](https://github.com/tmux-plugins/tmux-resurrect)).
`tmux-continuum` adds "Automatic tmux start" and "Last saved environment is automatically restored
when tmux is started" ([tmux-continuum](https://github.com/tmux-plugins/tmux-continuum)). Both are
community plugins, not core; automatic restore happens *exclusively* on tmux server start.

**Remote attach.** Any SSH session that can run `tmux attach`; control mode was designed for
SSH transport. No extra ports, no UDP.

---

## 4. zellij

**Model.** Rust terminal workspace; **MIT**; v0.45.1 (2026-08-28); 35,646 stars, 1,946 open issues
([repo API](https://api.github.com/repos/zellij-org/zellij)). Linux musl tarball 18.7 MB
([release assets](https://github.com/zellij-org/zellij/releases/tag/v0.45.1)).

**Restore semantics (built in).** "By default, each Zellij session is serialized and kept in the
user's cache folder waiting to be recreated after an intentional quit or an unintentional crash."
It serializes "the session layout (panes and tabs) and the command running in each pane (it will
re-run them in command panes)", and "serializes the session data into a layout every 1 second".
Viewport/scrollback are optional (`pane_viewport_serialization`, `scrollback_lines_to_serialize`).
Resurrected commands are **not run immediately**: they sit behind a "Press ENTER to run..." banner
unless `--force-run-commands` is passed ([Session Resurrection](https://zellij.dev/documentation/session-resurrection.html)).
There is no agent session awareness; whatever command is discovered gets re-run.

**Control surface.** `zellij action <CliAction>` is a large, scriptable CLI surface: `write`,
`send-keys`, `paste`, `dump-screen`, `new-pane`, `new-tab`, `list-panes`, `list-tabs`,
`current-tab-info`, `pipe`, `switch-session`, and many more
([`zellij-utils/src/cli.rs`](https://github.com/zellij-org/zellij/blob/master/zellij-utils/src/cli.rs),
`enum CliAction`). Plugins (WASM/Rust) can subscribe to events and issue commands
([Plugin API](https://zellij.dev/documentation/plugin-api.html)). There is **no documented stable
external JSON socket API** for third-party tools — automation goes through the CLI or plugins.

**Remote attach.** Two paths: plain SSH + `zellij attach`, or the built-in **web client** (off by
default). The web server provides login tokens, HTTPS (required when not on `127.0.0.1`),
read-only tokens, PWA/mobile UI, and a reverse-proxy `base_url`; `zellij attach
https://my-server:8082/my-session --token <token>` attaches a local terminal to the remote session.
The docs claim a bookmarked URL lets you "drop back in to exactly where you left off ... even if
we've since shut down our machine completely" ([Web Client](https://zellij.dev/documentation/web-client.html)).

---

## 5. dtach and abduco

**dtach** — "a program written in C that emulates the detach feature of screen, which allows a
program to be executed in an environment that is protected from the controlling terminal"; GPL-2.0;
0.9-era code, repo last pushed 2025-06-21 ([crigler/dtach](https://github.com/crigler/dtach)). It is
a detach shim over a Unix socket: no layout, no windows, no restore, no discovery beyond manual
socket paths, no API.

**abduco** — "provides session management i.e. it allows programs to be run independently from their
controlling terminal"; ISC; latest release 0.6 (24.03.2016). It improves on dtach with a session
list, retained exit status, read-only attach, better shared-session resize, and socket recreation
via `SIGUSR1` ([abduco README](https://github.com/martanne/abduco)). Still no restore across
process death; both are effectively dormant.

Either could shim a single interactive command, but neither is a PTY *owner* with layout or agent
awareness; a disk-only pause leaves nothing to restore.

---

## 6. Eternal Terminal

ET is "a remote shell that automatically reconnects without interrupting the session"
([README](https://github.com/MisterTea/EternalTerminal)). It is active: **v7.0.0, 2026-07-07**,
Apache-2.0, last push 2026-10-02 ([repo API](https://api.github.com/repos/MisterTea/EternalTerminal)).
Architecture: `et` client + `etserver` daemon (default **TCP 2022**) + `etterminal` per-user
process; SSH is used for handshake/auth, then the persistent ET connection runs on its own port.

What persists: client-side reattach. "On macOS and Linux, `et` saves each direct session's
reattachment credentials in `~/.et/sessions` (owner-only, plaintext), so after a client crash or
reboot you can return to a remote shell that is still running." A server reboot — or a pluto pause —
kills `etterminal`/HTM, so that shell is no longer running and the saved record is stale; ET's
value is surviving network changes and client restarts, not server process death. v7 also bundles
**HTM**, a multiplexer that "speaks tmux control mode" with its own `htm`/`htmd` daemon. ET needs
TCP 2022 reachable in addition to SSH, which is extra plumbing over a vsock-only relay, and it does
not replace the PTY-owner decision (HTM is not agent-aware).

---

## 7. mosh (+ tmux)

mosh survives client sleep, roaming, and lossy links, but explicitly not server process death. From
the README:

- "The `mosh` program will SSH to `user@host` to establish the connection... The server process
  listens on a high UDP port and sends its port number and an AES-128 secret key back to the
  client over SSH. The SSH connection is then shut down and the terminal session begins over UDP."
- "To function, Mosh requires UDP datagrams to be passed between client and server. By default,
  `mosh` uses a port number between 60000 and 61000."
- "Mosh does not support X forwarding or the non-interactive uses of SSH, including port
  forwarding."

mosh is GPL-3.0; latest release **1.4.0 (2022-10-27)**, last push 2026-03-22 ([repo
API](https://api.github.com/repos/mobile-shell/mosh)). It is a transport, not a PTY owner: pairing it
with tmux is the usual way to get both roaming and persistence. After a disk-only pause,
`mosh-server` is dead and nothing is restored.

---

## 8. Restore after a disk-only pause — and what no multiplexer can do

Pause kills the kernel and every process; only the filesystem survives. Consequences by tool:

| Tool | What returns after resume | What does not | Agent conversation |
| --- | --- | --- | --- |
| tmux | Nothing by itself. With resurrect/continuum: layout, cwd, allowlisted programs | Live processes, arbitrary programs | Only if the restored command carries a resume flag (agent-specific) |
| herdr | Saved layout, workspaces/tabs/panes, cwd; direct attach surfaces | Live processes, unsaved changes since last autosave | Yes, for supported integrations (e.g. `opencode --session`), resumed **when a client attaches** |
| zellij | Serialized layout + discovered commands (behind ENTER banner); optional viewport/scrollback | Live processes; command output identity | No; the discovered command is re-run as-is |
| dtach / abduco | Nothing | Everything | No |
| Eternal Terminal | Client can reconnect to a *still-running* server-side session | If the server rebooted/paused, the session process is gone | No |
| mosh | Nothing (transport only) | Everything | No |

The load-bearing fact: **no multiplexer restores a process or an in-flight turn.** All agent stacks
in [#8](https://github.com/Siddhj2206/pluto/issues/8) keep thread state on disk and let processes
die; resume is a fresh process plus client reconnect. Therefore "restore" in a pluto box is two
independent layers:

1. **Unattended layer** — systemd user units restart agent *servers* on boot; this is pluto's own
   mechanism and must not move into the multiplexer.
2. **Interactive layer** — the multiplexer reconstructs the human-visible layout and (at best)
   relaunches agent TUIs into their prior conversation.

herdr is the only candidate that does anything on layer 2 beyond layout: native session restore,
including pluto's lead agent. tmux and zellij need a boot hook to re-run the right command with the
right resume flag, which requires knowing the session id — herdr stores and replays it. But herdr's
agent resume is **client-triggered** (it waits for attach), so it is not a substitute for layer 1.

Pause cleanliness matters: herdr saves on clean shutdown (logind warning) and autosaves while
running (measured ≤10 s, undocumented bound); a hard kill restores the last saved layout only.
pluto's `pause` is defined as a clean stop ([#13](https://github.com/Siddhj2206/pluto/issues/13)),
which is the good case, but the boot path should still tolerate the last-save state.

---

## 9. Socket/CLI API vs agent APIs, and MCP

pluto's authoritative busy signals are agent APIs (opencode `session/status`, T3
`activityRunStatus`; see [#13](https://github.com/Siddhj2206/pluto/issues/13) and
[`docs/research/auto-pause-signals.md`](auto-pause-signals.md)). herdr's own semantic state comes
from screen-manifest detection or its own integrations
([Agents](https://herdr.dev/docs/agents/)); for OpenCode the integration "reports lifecycle state
and session identity while OpenCode runs inside a Herdr pane"
([Integrations](https://herdr.dev/docs/integrations/)).

Overlap and non-overlap:

- **Duplicated:** `working|blocked|done|idle` and "wait until blocked/done". This is a second,
  screen-derived source of truth for the same thing the agent APIs already report. pluto should not
  consume it for lifecycle decisions; divergence is a feature-detection bug waiting to happen.
- **Complementary:** raw PTY capability the agent APIs do not have — split/close panes, send keys
  to a non-agent TUI, read *any* pane's rendered output, wait for arbitrary output, attach a
  read-only stream (`terminal session observe`), and drive shells/tests/logs. This is what an MCP
  surface could usefully expose for "interactive shell in the box", not agent busy state.
- **Beware headless:** the OpenCode integration's pane-local lifecycle reporting does not run for
  "V2 Mini and headless clients", i.e. there is **no** herdr lifecycle reporting for `opencode
  serve` systemd units. That is another reason agent APIs stay authoritative.
- **MCP status:** herdr ships no MCP server and does not document one; its socket API is the
  substrate a pluto-authored MCP bridge could wrap. That bridge is optional and version-pinned, and
  it should expose pluto box verbs plus raw-terminal operations — not duplicate agent APIs.

---

## 10. Remote attach over SSH and vsock/relay compatibility

pluto's M0 access path is host-relay over vsock; interactive SSH reaches guest sshd bound to AF_VSOCK
port 22 by systemd-ssh-generator ("If invoked in a VM with AF_VSOCK support, a socket-activated SSH
per-connection service is bound to AF_VSOCK port 22"), and the host connects with
systemd-ssh-proxy, including `vsock-mux/` mode for "cloud-hypervisor/firecracker which do not allow
direct AF_VSOCK communication between the host and guests" (`ssh vsock-mux/run/vm-1234.sock`).
Both man pages: [systemd-ssh-generator(8)](https://man7.org/linux/man-pages/man8/systemd-ssh-generator.8.html),
[systemd-ssh-proxy(1)](https://man7.org/linux/man-pages/man1/systemd-ssh-proxy.1.html).

- **tmux:** works anywhere `ssh` works, including this path; control mode is designed for it.
- **herdr:** `ssh` + `herdr` and `herdr --remote` both use OpenSSH; `--machine` uses non-interactive
  SSH. Only requirement is that a compatible herdr binary exists in the box (bake it into the image;
  `HERDR_REMOTE_BINARY` avoids any herdr.dev download). Works through the vsock relay as plain SSH.
- **zellij:** SSH attach works; the web client is TCP/HTTPS and could ride the relay, but adds an
  HTTP auth/TLS surface and a second access path pluto does not need.
- **Eternal Terminal:** needs TCP 2022 in addition to SSH; would require extra forwarding through
  the relay, and its reconnect value overlaps with pluto's own pause/resume model.
- **mosh:** requires UDP 60000–61000 between client and server and does not support port
  forwarding. It cannot traverse an SSH-only/TCP-forwarded vsock relay and cannot tunnel the relay
  HTTP port. mosh only fits if pluto also gives the client a direct UDP path (e.g. an overlay),
  which is outside the M0 vsock access decision.

---

## 11. Footprint, headless, offline, governance — side by side

| | tmux | herdr | zellij | dtach/abduco | ET | mosh |
| --- | --- | --- | --- | --- | --- | --- |
| License | ISC | Apache-2.0 | MIT | GPL-2.0 / ISC | Apache-2.0 | GPL-3.0 |
| Maturity | OpenBSD, ~6-mo releases | v0.9.3, 7 months old, rapid | v0.45.1, since 2020 | dormant (2016–2020) | v7.0.0, active | 1.4.0 (2022) |
| Binary | ~1.5 MB | ~30 MB static | ~19 MB tarball | tiny | client+server+daemon | two binaries |
| Headless server | yes | yes (`herdr server`) | yes (server process) | yes (`abduco -c`) | `etserver` runs as service | `mosh-server` per session |
| Built-in restore | no | layout + agent resume | layout + command re-run | no | no | no |
| Agent-aware | no | yes | no | no | no | no |
| External API | commands + `-CC` control mode | JSON socket + CLI | `zellij action` + WASM plugins | none | none (client CLI) | none |
| Remote attach transport | SSH | SSH (or SSH-only `--remote`) | SSH or HTTPS web | SSH | SSH + TCP 2022 | SSH + UDP |
| Phones home by default | no | version + manifest checks (disable-able) | no | no | no | no |
| Account/hosted service | none | none today; Herdr Cloud waitlist | none | none | none | none |
| Governance risk | OpenBSD/nicm, stable | VC-backed company, contribution gate | community/MIT, large backlog | abandoned | small maintainer group | slow cadence |

---

## 12. Graceful degradation when the multiplexer is absent

Must keep working with **no multiplexer at all**: systemd user units (agent servers), agent
HTTP/SSE/WS APIs, plain `ssh box cmd`, cloud-init/boot hooks, relays, pause/hibernate. None of these
reference the multiplexer.

Degrades when absent:

- Interactive PTY sessions do not survive pause; a fresh SSH login is the only recovery.
- Layout replay and agent-TUI continuation are unavailable (herdr/zellij), or need tmux-resurrect.
- The auto-pause signals that read tmux state ([`auto-pause-signals.md`](auto-pause-signals.md):
  `list-clients`, `session_activity`) disappear; pluto falls back to agent APIs, utmp/logind, and
  cgroup counters — which the map already treats as the authority.

Design rule to record: **the multiplexer is a replaceable in-box convenience; pluto never queries it
for lifecycle, and a box image without it is fully functional.**

---

## 13. Recommendation

**Base shape stays tmux; the agent shape picks herdr** — as an opt-in, pinned, agent-aware PTY layer
inside agent images, with tmux and plain SSH retained as fallbacks.

Reasoning:

1. **Only herdr maps onto the agent shape's actual gap.** After disk-only pause, every candidate
   needs systemd to restart servers; only herdr additionally reconstructs the interactive agent
   surface with the prior conversation (`opencode --session <id>`) without pluto inventing its own
   session-id store. It also knows which pane is an agent, which is what makes "attach to this
   agent's TUI" and a shell-oriented MCP surface meaningful.
2. **It fits the access path with no new machinery.** SSH-native remote attach, a headless server
   for systemd supervision, a static binary, and offline operation with two defaults disabled. No
   UDP, no extra ports, no account, no relay.
3. **The hard constraint is easy to keep.** Install herdr as just another systemd user unit; agents
   stay separate units; busy signals stay on agent APIs; herdr's semantic state is treated as a
   display convenience for humans and explicitly not read by pluto.
4. **Its risks are pin-able, not fatal.** Pre-1.0 churn → vendor/pin `v0.9.3`; Herdr Cloud →
   self-hosted attach does not touch it today, and the abstraction is thin enough to swap;
   contribution gate → Apache-2.0 permits a fork, though pluto should avoid needing one; default
   herdr.dev checks → disable in images.
5. **The alternatives are worse fits for the agent shape.** tmux has no agent awareness and
   third-party restore; zellij has the best pure layout restore but re-runs commands blindly and has
   no agent resume or stable external JSON API; dtach/abduco are dormant shims; ET and mosh solve
   client reconnection, not process survival (and mosh's UDP conflicts with the vsock path).

If any prototype gate below fails, the fallback order is: (1) **tmux + systemd boot hooks** (the
existing base), with tmux-resurrect/continuum only if layout replay is wanted; (2) zellij if a
built-in, no-plugin resurrection is judged worth a second multiplexer. The base shape does not
change either way.

**Prototype items (verify on the first host, inside a real box):**

1. **Pause/resume fidelity.** Bake herdr into an agent image; run it under a systemd user unit; make
   changes; hibernate/pause via pluto's clean-stop path; resume. Check that `session.json` is
   written on pause, that layout returns, and that `opencode --session` actually reopens the prior
   conversation when a client attaches. Repeat with a hard `kill -9` to bound the unsaved-loss
   window.
2. **Agent units vs herdr panes.** Decide whether OpenCode runs as a systemd `serve` unit (HTTP)
   with herdr panes only for interactive TUIs. If both, verify the herdr OpenCode integration does
   not fight pluto's busy signals, and confirm the integration's headless blind spot (`V2 Mini` /
   headless clients) matches the design.
3. **MCP bridge.** Prototype a small guest-side bridge from pluto's MCP surface to the herdr socket
   (`pane.read`, `pane.run`, `terminal session observe`); check schema stability against a pinned
   herdr version and confirm it stays optional.
4. **Remote attach over vsock.** From the host, `herdr --remote` through an SSH `ProxyCommand`
   using `systemd-ssh-proxy vsock-mux/...`; confirm no herdr.dev download happens with a preinstalled
   binary (or `HERDR_REMOTE_BINARY`), and test `--machine` JSON forwarding and a phone SSH client.
5. **Offline image.** Set `[update] version_check = false` and `manifest_check = false`; verify no
   DNS stalls during boot/attach and that detection falls back to bundled manifests.
6. **Footprint in a small box.** Measure RSS with several panes and a real agent TUI, not just
   shells; confirm the 512 MB-class guest budget.
7. **Governance watch.** Track stable releases and Herdr Cloud; keep the integration surface limited
   to CLI/socket calls so the swap cost stays low.

---

## 14. Uncertainty

- **herdr autosave interval is not documented.** Locally, `session.json` appeared within 10 s of
  layout changes; the worst-case unsaved window and the hard-kill behavior were only observed once.
- **herdr agent resume was not tested end-to-end here** (no OpenCode integration installed during
  the measurement); the resume behavior is from the v0.9.3 docs. The docs do not list **T3 Code** as
  a supported agent, so T3 conversation restore would remain a boot-hook concern.
- **Resume-on-attach** means a box resumed with no client may show empty/fresh agent panes until
  someone attaches; unattended work must come from systemd units, not herdr panes.
- **zellij resurrection under a hard kill** was not tested; the docs describe serialization every
  1 s and resurrection after quit/crash, so the last second of layout may be lost and command
  re-run fidelity depends on discovery (`post_command_discovery_hook` exists for wrappers).
- **Eternal Terminal's saved sessions across a server reboot** are not explicitly documented as
  invalid; the process model implies the record goes stale, and it was not tested here.
- **Herdr Cloud's future scope** is unstated beyond "connect your machines without SSH setup". The
  current self-hosted attach path does not depend on it; that could change.
- Version pins: herdr `v0.9.3`, zellij `v0.45.1`, tmux `3.7c`, ET `v7.0.0`, mosh `1.4.0`,
  fetched 2026-10-04. herdr's rapid cadence makes the pin a hard requirement.

---

## Appendix: local measurement commands and outputs (first host, 2026-10-04)

herdr 0.9.3, downloaded from the GitHub release, run with a throwaway `HOME` under
`/tmp/opencode/herdr-proto`; no sudo, no system changes, all processes stopped afterward
(`pgrep -a herdr` empty).

```
$ file herdr
herdr: ELF 64-bit LSB pie executable, x86-64 ... static-pie linked, BuildID[sha1]=..., not stripped
$ ls -l herdr
29962088 bytes
$ ./herdr --version
herdr 0.9.3

# headless server + CLI over the socket
$ herdr server &
$ herdr status server
status: running
version: 0.9.3
socket: <HOME>/.config/herdr/herdr.sock
$ herdr workspace create --cwd /tmp --label proto --no-focus     # -> w1:p1
$ herdr pane run w1:p1 "echo hello-from-pane"
$ herdr pane read w1:p1 --source recent --lines 5
❯ echo hello-from-pane
hello-from-pane
$ ps -o rss,vsz -C herdr
 RSS 21108 KB   VSZ 69816 KB     # one pane + shell

# restore after clean stop
$ herdr server stop
$ cat <HOME>/.config/herdr/session.json | head
{"version":3,"workspaces":[{"id":"w1","custom_name":"clean", ... "tabs":[{"layout":{"Split":...}}]}]}
$ herdr server &
$ herdr workspace list     # label "clean" and both panes restored as fresh shells

# restore after kill -9
$ kill -9 <server-pid>
$ herdr server &
$ herdr workspace list     # only the last-saved workspace; later changes lost

# offline (no network namespace)
$ unshare -rn env HOME=<HOME> ./herdr server &
$ herdr workspace create --cwd /tmp --label offline --no-focus   # works
$ herdr pane run w1:p1 "echo offline-ok" && herdr pane read w1:p1 --lines 3
offline-ok
```
