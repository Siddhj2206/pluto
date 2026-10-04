# Client pairing for T3 Code and opencode

Research for pluto ticket [#27](https://github.com/Siddhj2206/pluto/issues/27), 2026-10-04. Question: what exactly do T3 Code and opencode clients (desktop, TUI, web, phone) need to pair with a server running in a remote box? Extends [Research: hosting agent servers in boxes (#8)](https://github.com/Siddhj2206/pluto/issues/8); that ticket covered how servers persist and resume, this one covers the client side of the wire.

Studied at these revisions (cloned/fetched 2026-10-04):

- opencode `907b3bc518fa48e90e8ec24dd327d13eee71c36c` (`sst/opencode`, mirrored as `anomalyco/opencode`) — monorepo packages `opencode`, `core`, `server`, `app` (web/desktop renderer), `desktop`, `tui`, `sdk`
- T3 Code `eac52f0087d9ba5dee5542f24788d1482affae43` (`pingdotgg/t3code`)
- First-party docs and man pages: opencode server/web docs, T3 Code `docs/user/remote-access.md` + `docs/internals/*`, `loginctl(1)`, `systemd-ssh-generator(8)`, `systemd-ssh-proxy(1)`, `tmux(1)`, `ssh(1)`, cloud-init NoCloud

The short version: **both clients are ordinary HTTP(S)+WebSocket clients, so the pluto M0 attach story is "give each box a stable origin on the host's tsnet node and let existing clients point at it" — not "tunnel from the client" and not "mount under a path prefix."** Both web UIs and both client libraries assume they are served at the root of an origin (`/`, `/assets/...`, `/ws`, `/pair`, `/event`), and T3 explicitly normalizes any pairing URL to `pathname = "/"`. Path-prefixed relay URLs (`https://host/box/<name>/`) are therefore a dead end for these two clients; SSH port-forwarding remains the fallback for terminals and for clients you cannot give a stable origin.

---

## 1. opencode

### 1.1 Server endpoint and auth

`opencode serve` starts a headless HTTP server; the TUI, web app, and desktop app are all clients of it. Network defaults come from `packages/opencode/src/cli/network.ts`:

- `--port` defaults to `0`, which means "try 4096 first, then any free port" — the fallback is explicit in `packages/opencode/src/server/server.ts:117-122` ("explicit `0` prefers 4096 first, then any free port").
- `--hostname` defaults to `127.0.0.1`; `--mdns` (default off) publishes a discovery record and flips the bind to `0.0.0.0`; `--cors` adds allowed origins (`network.ts:6-33`, `56-79`).
- Config keys `server.port`, `server.hostname`, `server.cors`, `server.mdns` supply defaults when the corresponding flag is not passed (`network.ts:62-79`).

Auth is HTTP Basic, enabled only when `OPENCODE_SERVER_PASSWORD` is set:

- `OPENCODE_SERVER_USERNAME` defaults to `opencode` (`packages/opencode/src/server/auth.ts:17-20`); credentials are compared exactly (`auth.ts:28-34`).
- `serve`/`web` print a warning when the password is unset (`packages/opencode/src/cli/cmd/serve.ts:15-17`, `web.ts:40-42`).
- The middleware accepts either an `Authorization: Basic …` header or an `auth_token` **query parameter** whose value is base64 of `user:password` (`packages/opencode/src/server/routes/instance/httpapi/middleware/authorization.ts:12`, `77-83`). On failure it answers 401 with `WWW-Authenticate: Basic realm="Secure Area"` (`authorization.ts:13-14`, `47-52`), so a browser shows its native basic-auth prompt even without the token trick.
- Only a small allowlist bypasses auth: `GET /site.webmanifest`, the two PWA icons, and CORS preflight (`middleware/authorization.ts:101-116`; `packages/opencode/src/server/shared/public-ui.ts:4-12`). Everything else — API, SSE, PTY WebSocket, and the served UI — is protected by the same credential (`httpapi-ui.test.ts:369-408`, `443-455`).
- CORS defaults: any `localhost`/`127.0.0.1` port, Tauri/Electron origins, and `*.opencode.ai`; more via `--cors` (`packages/server/src/cors.ts:5-20`).

The server also serves the web UI itself. If the build has an embedded copy it serves that; otherwise it reverse-proxies `https://app.opencode.ai` for every non-API path (`packages/opencode/src/server/shared/ui.ts:9`, `44-49`, `64-107`). Either way the browser sees one origin for UI + API.

### 1.2 The clients

**TUI — `opencode attach <url>`.** The URL is a required positional with the example `http://localhost:4096` (`packages/opencode/src/cli/cmd/attach.ts:8-16`). Auth flags: `--password/-p` (falls back to `OPENCODE_SERVER_PASSWORD`) and `--username/-u` (falls back to `OPENCODE_SERVER_USERNAME`, then `opencode`) (`attach.ts:35-44`, `114`). Also `--dir` for the remote project directory (passed through when it doesn't exist locally, `attach.ts:70-79`), `--continue/-c`, `--session/-s`, `--fork`, and `--mini` (`attach.ts:17-61`). With `--session`, the client validates the session exists before attaching (`packages/opencode/src/cli/tui/validate-session.ts:14-28`). No TUI config key for a remembered server URL turned up at this commit; the URL appears to be per-invocation (a wrapper command or shell alias is the workaround).

**Web / phone.** Any browser pointed at the server gets the SPA. Two pairing forms:

1. `http://<host>:<port>/?auth_token=<base64(opencode:PASSWORD)>` — the app reads the token, turns it into `{username, password}`, and strips it from the URL with `history.replaceState` (`packages/app/src/entry.tsx:112-117`, `152-163`; decoding in `packages/app/src/utils/server.ts:10-19`).
2. Add it manually in the server dialog: URL + optional username/password, with a health preview (`packages/app/src/components/dialog-select-server.tsx:30-36`, `92-116`).

The page is a PWA (manifest and icons in `packages/app/index.html:13-18`, install-related auth exceptions in `public-ui.ts`), which is the phone story: install from the box origin, token survives pairing. The app defaults to `location.origin` when not hosted on `opencode.ai` (`entry.tsx:99-104`).

**Desktop app.** `packages/desktop` bundles the same app UI in Electron, spawns a local sidecar server for its own environment, and stores a default server URL in the Electron store (`packages/desktop/src/main/server.ts:14`, `38-48`). Remote servers can be added through the same server dialog (`http` connection with URL/username/password). Note: the app's connection model already includes an `Ssh` connection type — "Remote server desktop can SSH into … SSH client exposes an HTTP server for the app to use as a proxy" (`packages/app/src/context/server.tsx:212-217`) — but at `907b3bc` the `packages/desktop` tree contains no producer for it, so desktop-managed SSH is either in-flight or vestigial. Treat it as absent for M0 and a prototype item.

**SDK.** `@opencode-ai/sdk` takes `baseUrl` plus headers; the TUI and app build the Basic header from credentials (`packages/app/src/utils/server.ts:21-42`). Requests carry `x-opencode-directory` (URL-encoded) to select the project on the server (`packages/sdk/js/src/v2/client.ts:25`, `66`).

### 1.3 Session identity and reconnect

- **Identity is the URL.** The client store keys servers by normalized URL (`packages/app/src/context/server.tsx:224-227`, `290-304`), persisted under localStorage/Electron store name `server.v3` in `opencode.global.dat` (`server.tsx:263-267`; `packages/app/src/utils/persist.ts:28`, `388-432`). Credentials live in the same record. If a relay URL changes, it is a different saved server; sessions still exist on the box, but a user must re-add/re-point the server.
- **Sessions are server-side and directory-scoped.** `--continue` picks the most recently updated root session in the directory (`packages/tui/src/app.tsx:503-522`). Two clients attached to one server share the same sessions.
- **Reconnect is client-owned.** The TUI's SSE loop retries with exponential backoff from 1 s to 30 s (`packages/tui/src/context/sdk.tsx:51-52`, `113`). The web app polls server health every 10 s and retries transient transport failures (`packages/app/src/utils/server-health.ts:72-113`, `115-135`, `149-164`). Neither replays a running turn: on restart the server fails interrupted tool calls and the next prompt continues (confirmed at this commit by #8's findings, which hold here).
- **Streams.** The event stream is SSE; PTY terminals use a WebSocket at `/pty/<id>/connect` (v1) or `/api/pty/<id>/connect` (v2), and put the base64 `auth_token` in the query string when it cannot send headers (`packages/app/src/utils/terminal-websocket-url.ts:15-33`). A relay must therefore proxy both SSE and WebSocket upgrade, not just plain HTTP.

---

## 2. T3 Code

### 2.1 Server endpoint and pairing URL

`t3 serve` speaks HTTP + WebSocket; the environment owns execution and state (`docs/internals/remote.md:3-5`). Flags include `--port`, `--host`, `--base-dir`, `--tailscale-serve`, `--tailscale-serve-port` (`apps/server/src/cli/config.ts:27-83`); the SSH-launch default port is `3773` (`packages/ssh/src/tunnel.ts:48`) and `DEFAULT_PORT = 3773` (`apps/server/src/config.ts:23`). For a stable relay URL, pass an explicit `--port`.

Unlike opencode, T3's client pairing is a token exchange, not a static password:

- `t3 pair` finds the running server, probes its public descriptor at `/.well-known/t3/environment` (`apps/server/src/cli/pair.ts:61`, `208-233`), mints a one-time token, and prints a URL of the form `http://<host>:<port>/pair#token=<token>` — the backend is the URL origin, the secret lives in the **fragment** (`apps/server/src/startupAccess.ts:92-98`).
- The one-time token TTL is 5 minutes (`apps/server/src/auth/PairingGrantStore.ts:239`, `385-389`); desktop bootstrap grants are 24 h (`PairingGrantStore.ts:248`, `318-319`). The docs: "Pairing authorizes that device for future connections. Use a fresh one-time link for each new device; you do not need the original token to reconnect" (`docs/user/remote-access.md`).
- Receiving clients parse `token` from hash or query, accept `http:`, `https:`, `ws:`, `wss:`, and **normalize the backend to origin root** — `url.pathname = "/"` in `normalizeRemoteBaseUrl` and again in `toHttpBaseUrl`/`toWsBaseUrl` (`packages/shared/src/remote.ts:93-97`, `106-110`, `120-130`). A path-prefixed backend URL is silently truncated to the origin.
- The hosted web app pairs with `https://app.t3.codes/pair?host=<backend>&label=<name>#token=<token>` and the browser exchanges the fragment secret directly with the backend and strips it (`apps/web/src/hostedPairing.ts:73-87`; `docs/internals/remote.md:34-38`).

Sessions are scoped credentials: RPC scopes are checked per request, WebSocket upgrades use short-lived tickets minted over HTTP, and revocation is transactional (`docs/internals/environment-auth.md:9-13`, `27-32`, `34-38`). The environment issues its own sessions; relay/cloud identity is a separate trust boundary (`environment-auth.md:3-5`).

### 2.2 The clients

**Desktop and web.** "Add environment" accepts the pairing URL (paste) or QR; settings live under Settings → Connections (`docs/user/remote-access.md`). The desktop renderer is served from the bundled `t3code://` client, not by the backend, and API traffic always goes to the environment URL (`docs/internals/remote.md:61-71`). The release server also serves its own web client at origin root when a bundled client exists (`apps/server/src/config.ts:249-268`, `apps/server/src/cli/config.ts:384`, static serving in `apps/server/src/http.ts:553-597`); otherwise browsers use the hosted app.

**Mobile.** The iOS/Android app pairs by scanning the pairing QR (camera permission in `apps/mobile/app.config.ts:386`) or pasting the URL; registration funnels through `connectPairingUrl` → `onboarding.registerPairing({ pairingUrl })` (`apps/mobile/src/connection/onboarding.ts:13-19`). It stores the connection locally and uses the same connection runtime as web/desktop (`docs/internals/connection-runtime.md:3-7`).

**Desktop-managed SSH.** Desktop can "Add environment → SSH": it connects with the system `ssh`, downloads a pinned, self-contained T3 release archive onto the remote (`~/.t3/runtime/versions/<version>/t3`), starts `nohup … serve --host 127.0.0.1 --port <picked> --base-dir ~/.t3` if no server is already running, then forwards `ssh -L <local>:127.0.0.1:<remote>` (`packages/ssh/src/tunnel.ts:425-537`, `540-718`, `1155-1156`). It reuses a server it finds via `~/.t3/userdata/server-runtime.json` and never kills a server it did not start (`tunnel.ts:602-670`; `docs/internals/remote.md:47-53`). Remote host prerequisites: Linux or Apple Silicon macOS with `curl`/`wget`, `tar`, `sha256sum`/`shasum` (`docs/user/remote-access.md`). This is the one client flow that could run entirely over pluto's SSH surface — see §3.

**Tailscale HTTPS.** `t3 serve --tailscale-serve` / `t3 pair --tailscale` publish the server with `tailscale serve --https=443` and pair through `https://machine.tailnet.ts.net/`; the mapping persists across restarts (`docs/user/remote-access.md`). pluto targets self-hosted **headscale**, which does not support Serve ([#6](https://github.com/Siddhj2206/pluto/issues/6)), so this route is out; direct pairing through the host relay replaces it.

### 2.3 Session identity and reconnect

- **Identity is independent of the route.** "An environment keeps its ID across server restarts and endpoint changes" (`docs/internals/remote.md:11-14`); saved connections are local to each client. Advertised endpoints are reachability hints, not identity (`remote.md:22-25`). Practically, though, a client still needs the *current* address to dial; `t3 pair` can be re-run on the host at any time to mint a fresh link without restarting the server (`docs/user/remote-access.md`). Whether a saved direct connection re-resolves a changed address automatically was not established — prototype item.
- **Reconnect is supervised once per environment.** Jittered exponential backoff, base 1 s, cap five minutes, reset only after a connection stays up; offline/auth failures wait for an app wakeup instead of burning attempts (`docs/internals/connection-runtime.md:11-17`; `packages/client-runtime/src/connection/supervisor.ts:34-35`). Offline clients keep cached projections readable and replay cursors for five idle minutes (`connection-runtime.md:57-66`).
- **Threads are server-side.** A server restart interrupts running turns and terminals; the next connection sees threads intact and the interrupted run retired, not replayed (from #8, still true at this commit).
- **One-time credentials do not need to last.** The device's session/refresh path keeps the connection live across restarts; token expiry affects new HTTP requests, not an open socket, and the server is explicit that an open session stays listed after its credential expires (`connection-runtime.md:35-46`).

---

## 3. SSH port-forwarding vs relayed URLs

Neither client is a raw terminal; both want an HTTP(S) origin. That decides most of this comparison.

| | Client-side SSH forward | Relayed URL (host proxy) |
| --- | --- | --- |
| Shape | `ssh -N -L 4096:127.0.0.1:4096 box`, then point the client at `http://127.0.0.1:4096` | Host tsnet node listens on a stable per-box port/hostname and reverse-proxies to guest loopback |
| opencode TUI | Works: `attach http://127.0.0.1:<pinned>` | Works: `attach http://host:<port>` |
| opencode web / phone PWA | Only if a local forward is re-exposed to the browser; messy on phone | Native: `http://host:<port>/?auth_token=…` |
| T3 desktop/web/mobile | Desktop SSH mode can manage its own forward; web/mobile cannot | Native: pair the `http://host:<port>/pair#token=…` URL, QR to phone |
| Saved connection stability | URL includes a local port; must pin it or the client sees a new server (opencode keys on URL) | Stable origin; no per-client state beyond pairing |
| Auth | SSH keys at the host/box edge; app auth inside (opencode password / T3 tokens) | Same, plus tailnet ACLs at the host node; app auth still required |
| Requirements on pluto | Host agent SSH endpoint must support `exec` (T3 runs multi-line shell scripts) and `direct-tcpip` forwarding to box loopback | Host agent must proxy HTTP with SSE and WebSocket upgrade, no buffering, correct Host/Origin, per-box port allocation |
| Path-prefix (`/box/<name>/`) | n/a | **Not viable**: opencode's UI assets are root-absolute (`/favicon…`, `/assets…`, `packages/app/index.html:8-25`), and T3 resets any backend URL to `pathname="/"` (`packages/shared/src/remote.ts:93-97`, `106-110`). One origin per box, or a subdomain per box |
| Bulk transfer | scp/sftp over the same SSH channel | HTTP through the relay (fine over tailnet direct; avoid via DERP for large media) |

Notes that shape M0:

- **Bind guests to loopback.** Both servers default to loopback (`opencode` 127.0.0.1; T3's SSH launcher starts `--host 127.0.0.1`). Keep that: the relay terminates reachability; the box never listens on its TAP address.
- **A guest sshd is the universal bridge.** `systemd-ssh-generator(8)` already binds a socket-activated sshd to AF_VSOCK port 22 in a VM (see §4), and sshd natively implements `direct-tcpip` to guest loopback. The host agent can (a) proxy TCP from its tsnet listeners into guest vsock 22, giving real `ssh`/`git`/`scp`/`-L` semantics to any client, and (b) use its own SSH client (or the same channel) to carry HTTP for the relay, avoiding a second guest-side agent. Firecracker needs the `vsock-mux/` form of the systemd proxy because it multiplexes vsock over a UDS (`systemd-ssh-proxy(1)`); the host agent has to speak the same `CONNECT <port>` framing (already on the #6 map).
- **Stable per-box origins.** The clean M0 shape is one port per box on the host's `tsnet` node, persisted in the agent's box record (e.g. `http://pluto-host:4100` for box A, `:4101` for box B). Per-box `tsnet` nodes (`box.tailnet`) are the nicer long-term URL — the #6 research already reserved that upgrade — but per-box ports need no control-plane objects and survive pause because only the agent's record changes. A wildcard-subdomain scheme on one node would need headscale DNS support that is not there for non-node names; do not design around it before checking.
- **Everything must survive pause.** Whatever the host allocates is host-side state; the guest needs nothing new. Pairing/config that lives on the box disk (opencode password, T3 `~/.t3` state, authorized_keys) survives resume; the relay just starts forwarding again.

### What needs a prototype

1. **Host relay fidelity**: reverse-proxy an opencode server (UI, `/event` SSE, PTY WebSocket, `auth_token`) and a T3 server (`/ ws`, pairing `/pair` SPA route, `/.well-known/t3/environment`) through a single host handler over a guest SSH channel or vsock relay; verify no-buffering, WebSocket upgrade, Host/Origin, and Basic auth passthrough.
2. **Stable port allocation and re-pair behavior**: prove a box keeps the same relay origin across pause/resume, and determine what happens to saved opencode servers (keyed by URL) and saved T3 connections when it cannot. For T3, test whether a saved connection follows a changed endpoint or needs a fresh `t3 pair`.
3. **T3 desktop SSH mode against pluto's SSH bridge**: the launcher writes scripts under `$HOME/.t3/ssh-launch/…`, `nohup`s a server, and uses `ssh -L`. Either make the bridge a faithful SSH server (exec + direct-tcpip), or pre-bake `~/.t3/runtime/versions/<v>/t3` in the image and rely on the boot hook, accepting that desktop may still install its own version if they differ.
4. **opencode desktop remote server**: the add-server dialog and `http` connections exist; the `Ssh` connection type has no implementation at `907b3bc`. Prototype the manual URL+password path first.
5. **Phone ergonomics**: opencode PWA install + `auth_token` via the relay; T3 QR scan against a plain-HTTP tailnet URL; mosh/tmux fallback.

---

## 4. What the box must run at boot

Disk-only pause kills every process; the filesystem survives. So "resume" is: the box boots, systemd starts the user manager, and units recreate the servers. The pieces, all first-party:

**Linger** — one-time per user (image build or cloud-init): `loginctl enable-linger <user>`. "If enabled for a specific user, a user manager is spawned for the user at boot and kept around after logouts. This allows users who are not logged in to run long-running services." ([`loginctl(1)`](https://man7.org/linux/man-pages/man1/loginctl.1.html), `enable-linger`). This is what makes `systemctl --user` units run without any SSH session.

**systemd user units** — put the servers there rather than inside tmux, because units give restart-on-failure and start deterministically at boot:

```ini
# ~/.config/systemd/user/opencode.service
[Unit]
Description=opencode server

[Service]
ExecStart=%h/.local/bin/opencode serve --hostname 127.0.0.1 --port 4096
EnvironmentFile=%h/.config/opencode/server.env   # contains OPENCODE_SERVER_PASSWORD=…
Restart=on-failure

[Install]
WantedBy=default.target
```

```ini
# ~/.config/systemd/user/t3code.service — or just `t3 service install`
[Install]
WantedBy=default.target
```

T3 ships this path itself: `t3 service install` writes `t3code.service`, and the docs spell out that Linux needs systemd user services, that setup enables lingering, and how to recover from `linger-disabled` (`sudo loginctl enable-linger "$(id -un)"`) (`docs/user/background-service.md`, lines about "Platform support" and troubleshooting). opencode has no equivalent command; the unit is ours.

Enable once with `systemctl --user enable --now opencode t3code`. Persist the password/config on the box disk so clients reconnect after resume without re-pairing.

**sshd and vsock** — the image needs `openssh-server`; systemd's generator does the rest: "If invoked in a VM with AF_VSOCK support, a socket-activated SSH per-connection service is bound to AF_VSOCK port 22" ([`systemd-ssh-generator(8)`](https://man7.org/linux/man-pages/man8/systemd-ssh-generator.8.html); applies when sshd is installed, `systemd.ssh_auto=` defaults to yes). The host reaches it via `systemd-ssh-proxy`'s `vsock/` or — for Firecracker — `vsock-mux/` host strings (man page: Firecracker "do[es] not allow direct AF_VSOCK communication … and provide[s its] own multiplexer over AF_UNIX sockets"). `authorized_keys` for laptop and phone live in the box disk.

**tmux for interactive PTYs** — every PTY dies with the box, and nothing can reattach to a dead process (#8 §5). tmux is still the right owner for interactive shells and full-screen TUIs within an incarnation, and the boot hook recreates it idempotently: `tmux new-session -d -A -s main`, where `-A` "makes new-session behave like attach-session if session-name already exists" ([`tmux(1)`](https://man7.org/linux/man-pages/man1/tmux.1.html), new-session). Phones use mosh+tmux or plain SSH+mux; `mosh-server` is image content, not a boot service.

**Per-boot facts** — hostname, SSH host keys, and machine identity live on the durable disk; anything that must change per incarnation (e.g. a re-issued key or address) goes through cloud-init NoCloud with a changed `instance-id`, as #8 established (cloud-init only re-runs user-data when `instance-id` changes; [NoCloud docs](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html)). In the host-relay model the guest needs no tailnet identity at all, so M0 per-boot injection is small: ensure linger/units are enabled (baked), and optionally refresh credentials.

**What this means across pause**: unit state, tmux layout, PTY content, and in-flight turns do not survive; session/thread state, pairing grants, authorized keys, opencode password, and T3 environment identity do. Boot restarts the units; clients reconnect on their own; the user sees the interrupted turn flagged and continues it with one message (#8).

---

## 5. Concrete M0 attach recipes

Assumptions: box reachable through the host agent's tsnet node; stable per-box origin `http://<host>:<port>`; guest servers on loopback; opencode password `$OC_PW` stored on the box disk; T3 server on the same box (port 3773) with `t3 service install` under linger; host agent proxies both HTTP and SSH to the guest.

**A. Laptop TUI (opencode) — relayed URL (preferred):**

```sh
opencode attach http://<host>:<port> -u opencode -p "$OC_PW"
# or: export OPENCODE_SERVER_PASSWORD=$OC_PW; opencode attach http://<host>:<port> --continue
```

**B. Laptop TUI (opencode) — SSH forward (fallback, works without the HTTP relay):**

```sh
ssh -N -L 4096:127.0.0.1:4096 pluto@<host>          # host agent bridges to guest sshd/vsock
opencode attach http://127.0.0.1:4096 -p "$OC_PW"
```

Pin 4096 (or a per-box local port) so the saved client URL stays valid; `ExitOnForwardFailure=yes` and a keepalive make the forward fail loudly ([`ssh(1)`](https://man.openbsd.org/ssh.1), `-L`).

**C. Laptop or phone browser (opencode web):**
`http://<host>:<port>/?auth_token=$(printf '%s' "opencode:$OC_PW" | base64)`. The app strips the token from the URL and saves the server. On phone: install the PWA from the same origin. Without the token the browser gets a Basic-auth prompt (realm "Secure Area").

**D. T3 desktop/web:** `t3 pair` inside the box (boot hook or `pluto` command over SSH) prints `http://<host>:<port>/pair#token=…`; paste it into **Add environment** (Settings → Connections). The token is one-time (5 min) and pairing outlives it.

**E. T3 phone:** scan the same pairing URL's QR from the mobile app (Settings → Environments afterwards).

**F. Phone terminal:** `ssh pluto@<host>` (host agent → guest sshd; Tailscale SSH identity from the #6 design) then `tmux attach -t main`; or `mosh` + tmux for roaming. Claude Code/Codex TUI sessions likewise live in tmux.

**G. T3 desktop-managed SSH (prototype):** add an SSH environment pointing at the host agent. Prerequisites to make it work: exec of multi-line shell scripts, `ssh -L` (`direct-tcpip`) to box loopback, `$HOME` on the box disk, and either box egress to `t3.codes` for the ~70 MB archive or a pre-baked matching runtime under `~/.t3/runtime/versions/<version>/`.

**H. Resume test:** pause the box (all processes die) → resume → user units restart opencode/T3 and tmux is recreated → A–F reattach without re-pairing; opencode shows failed tool calls, T3 shows the retired run; one prompt continues.

---

## 6. Open questions

1. **Per-box origin allocation**: ports on the host tsnet node now, per-box `tsnet` nodes later (#6 reserved this). Does M0 pin ports per box in the agent's box record, or allocate dynamically and re-pair on change? The clients' URL-keyed stores make stability a real requirement.
2. **T3 endpoint change semantics**: environment ID is route-independent, but can a saved direct connection follow a changed address without a new pairing URL? Needs a live test.
3. **HTTP-over-SSH vs guest vsock relay**: which does the host agent implement? SSH channels reuse sshd (no new guest code) but mean the relay depends on sshd being up; a tiny vsock↔TCP relay in the image is more direct but is new guest surface. Prototype both if cheap.
4. **TLS**: plain HTTP over the tailnet, or `tsnet.ListenTLS` per box? Browsers/PWAs are happier with a secure context (service workers, clipboard, notifications); headscale does not provide `tailscale serve`. Local CA or per-box certs are undesigned.
5. **Relay auth depth**: tailnet ACL at the host plus app auth inside is the current answer to #8's auth question; decide whether the host also injects identity headers (tsnet `WhoIs`) or stays a dumb proxy.
6. **Bake vs fetch client runtimes**: T3 desktop SSH prefers its own pinned archive (`~/.t3/runtime/versions/<v>`); opencode has no such launcher. Decide what the image bakes so first attach is offline and version-skewed launchers fail gracefully.
7. **Tmux vs units**: settled above as "units for servers, tmux for interactive", but #8's M0 sketch put the server inside tmux. Confirm the change and adjust the demo assertions accordingly.

---

## Sources

Primary sources only; all fetched 2026-10-04.

**opencode** (`sst/opencode` @ `907b3bc518fa48e90e8ec24dd327d13eee71c36c`):

- CLI: `packages/opencode/src/cli/cmd/{serve,web,attach,run}.ts`, `packages/opencode/src/cli/network.ts`, `packages/opencode/src/cli/tui/validate-session.ts`
- Server: `packages/opencode/src/server/server.ts`, `packages/opencode/src/server/auth.ts`, `packages/opencode/src/server/routes/instance/httpapi/{api,server}.ts`, `.../middleware/authorization.ts`, `packages/opencode/src/server/shared/{ui,public-ui}.ts`, `packages/opencode/test/server/httpapi-ui.test.ts`, `packages/server/src/cors.ts`
- Clients: `packages/tui/src/{app.tsx,context/sdk.tsx}`, `packages/app/src/{entry.tsx,app.tsx}`, `packages/app/src/context/{server.tsx,server-sdk.tsx}`, `packages/app/src/utils/{server.ts,server-health.ts,persist.ts,terminal-websocket-url.ts}`, `packages/app/src/components/dialog-select-server.tsx`, `packages/app/index.html`, `packages/desktop/src/main/server.ts`, `packages/desktop/src/renderer/index.tsx`, `packages/sdk/js/src/v2/client.ts`
- Docs: [server](https://opencode.ai/docs/server/), [web](https://opencode.ai/docs/web/)

**T3 Code** (`pingdotgg/t3code` @ `eac52f0087d9ba5dee5542f24788d1482affae43`):

- Server: `apps/server/src/config.ts`, `apps/server/src/cli/{config,pair,service}.ts`, `apps/server/src/startupAccess.ts`, `apps/server/src/http.ts`, `apps/server/src/auth/{EnvironmentAuth,PairingGrantStore,http}.ts`, `apps/server/src/persistence/AuthSessions.ts`
- Clients: `apps/web/src/hostedPairing.ts`, `packages/shared/src/remote.ts`, `packages/client-runtime/src/connection/{supervisor,registry}.ts`, `packages/ssh/src/{tunnel,command}.ts`, `apps/mobile/src/connection/onboarding.ts`, `apps/mobile/app.config.ts`
- Docs: `docs/user/remote-access.md`, `docs/user/background-service.md`, `docs/internals/{remote,connection-runtime,environment-auth,devices}.md`

**Boot/transport:**

- [`loginctl(1)`](https://man7.org/linux/man-pages/man1/loginctl.1.html) (`enable-linger`)
- [`systemd-ssh-generator(8)`](https://man7.org/linux/man-pages/man8/systemd-ssh-generator.8.html), [`systemd-ssh-proxy(1)`](https://man7.org/linux/man-pages/man1/systemd-ssh-proxy.1.html)
- [`tmux(1)`](https://man7.org/linux/man-pages/man1/tmux.1.html) (`new-session -A`), [`ssh(1)`](https://man.openbsd.org/ssh.1) (`-L`)
- [cloud-init NoCloud](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html)
- Internal: [#6 overlay and access](https://github.com/Siddhj2206/pluto/issues/6) (`research/overlay-and-access`), [#8 agent servers in boxes](https://github.com/Siddhj2206/pluto/issues/8) (`research/agent-servers-in-boxes`, commit `c863895`)
