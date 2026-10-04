# Self-hosted overlay and remote access

Research for pluto ticket #6 (`Research: self-hosted overlay and remote access`), branch
`research/overlay-and-access`, 2026-10-04.

Question: with everything self-hosted, how do the laptop, the phone, and the hosts reach the boxes?

Scope covered: headscale vs tailcat vs tsnet vs plain WireGuard; official Tailscale clients against a
self-hosted control server and what that costs; self-hosted DERP and NAT traversal; public-relay rate
limits; host-relay vs per-box overlay identity (pause/fork/auth/undo); concrete SSH, web UI, and git
paths; and the phone story end to end.

## Recommendation (TL;DR)

**Headscale as the control plane, official Tailscale clients on laptop and phone, and a userspace
`tsnet` node inside the pluto host agent — with boxes reached by relay through the host (host-relay),
not by giving every box its own overlay identity.**

- Headscale is the only option that gives the phone a first-class client: Tailscale's official iOS and
  Android apps both support custom coordination servers, and headscale is explicitly documented by
  Tailscale as the server to point them at ([Tailscale custom control server docs][tt-custom],
  [headscale clients page][hs-clients]).
- `tsnet` is the host-agent shape that respects "daily driver, no sudo, no system-wide changes": it
  embeds a Tailscale node in a Go process with a userspace network stack and no root, no TUN, no
  routing changes ([tsnet docs][tsnet]).
- Host-relay keeps pause, fork, and undo cheap: boxes have no control-plane identity, so pausing a box
  is a local operation and forking one cannot collide with an existing node. Per-box identity on the
  host (`tsnet.Server` per box) is the upgrade path if per-box names/ACLs are wanted; running
  `tailscaled` *inside* boxes is the option to avoid because cloned disk state duplicates node keys.
- Self-host DERP (headscale's embedded server, or `cmd/derper`) once a public endpoint exists;
  Tailscale's public DERP fleet is rate-limited, has no SLA, and should be treated as bootstrap
  fallback only ([tailcat announcement][tailcat-blog]).
- `tailcat` is the best zero-control-plane transport (no accounts, no IPs, userspace) and a good
  laptop/desktop ↔ host tool, but it has no iOS/Android client today, so it cannot be the phone story.
- Plain WireGuard is the wrong layer for this job: it deliberately leaves key distribution and
  coordination out of scope and has no NAT traversal or relay ([WireGuard][wg]).

The sharp constraint to settle next: **headscale and DERP both need a public endpoint** — public IP,
HTTPS/443, and UDP/3478 for STUN ([headscale requirements][hs-req]). If no pluto host can be reached
from the open internet, plan for one small always-on VPS (still self-hosted software) or a port-forward,
or drop headscale as the primary and accept a tailcat-shaped architecture without phone support.

---

## 1. The four options are not the same layer

The ticket lists four things, but they answer different parts of the problem. Comparing them as
mutually exclusive is a category error; the real choices are *which control plane* and *which data
plane*.

| | Control plane | Data plane | NAT traversal | Phone client | Root required | Per-node identity |
|---|---|---|---|---|---|---|
| **Headscale** | self-hosted, implements TS control protocol | — (clients bring their own) | — | official TS app (custom server) | no (server), n/a | yes, nodes |
| **Tailscale client / `tsnet`** | Tailscale CP or headscale (`ControlURL`) | magicsock (userspace WireGuard + DERP) | yes (STUN, hole punching, DERP fallback) | via official apps; `tsnet` no app | no for `tsnet`; yes for system `tailscaled` | yes |
| **tailcat** | none | magicsock, no IPs/identity | yes (same magicsock + DERP) | **no** (CLI: Linux/macOS/Windows/BSD; wasm browser) | no | no — bearer-address capability |
| **Plain WireGuard** | none (key/config distribution explicitly out of scope) | kernel or userspace WireGuard | **no** (no STUN, no hole punching, no relay) | app exists, but you hand-wire peers | yes (kernel/TUN) | static keys |

### 1.1 Headscale (control plane)

Headscale is an open-source, self-hosted implementation of the Tailscale control server, scoped to a
single tailnet, for personal use ([headscale README][hs-readme]). It exchanges WireGuard/node public
keys, allocates IPs, distributes ACLs, and hands clients a DERP map. Its project docs claim "full base
support" of Tailscale features, and the matrix currently includes: preauth keys and web auth, MagicDNS
and split DNS, Taildrop and Taildrive, tags, subnet routers, exit nodes, ephemeral nodes, embedded
DERP, **peer relays**, ACLs/grants, **Tailscale SSH**, and OIDC login ([headscale features][hs-feat]).

Not supported (open issues): Funnel, Serve, network-flow logs; OIDC groups cannot be used in ACLs;
device posture and IP sets are unsupported ([hs-feat], [headscale policy][hs-policy]).

Operationally: one Go binary + SQLite, HTTPS on 443, a public IP (dual-stack recommended), and
UDP/3478 only if its embedded DERP is enabled ([hs-req]). Tailscale itself says "we love headscale and
partially fund development of it" ([tailcat-blog]).

### 1.2 Tailcat (data plane only)

Tailcat is a 2026 open-source remix of Tailscale's data plane: magicsock (STUN, UDP hole punching),
userspace WireGuard, userspace gVisor netstack, and DERP as rendezvous/fallback — with **no control
plane, no accounts, no IPs, no admins, no root** ([tailcat README][tailcat-readme], [blog][tailcat-blog]).

How it works: a server starts, picks a DERP relay, prints a `tc...` address that encodes its WireGuard
public key, a pre-shared key, and DERP info. That address *is* the bearer credential; the client passes
it and connects. Keys can be ephemeral (default) or named/saved for a stable address. Clients can be
restricted to known peer keys with `tailcat serve --allow=nodekey:...`. The CLI can serve TCP ports,
`forward` them to local ports, `browse`, run SOCKS, run SSH (authorized_keys or `no-auth-ssh`), run a
command per connection (`exec`, which gets `$TAILCAT_PEER_KEY`), serve files via SFTP, and act as an
exit node. The Go library exposes `Server.OnTCP`/`OnUDP` and `Client.Dial*` ([tailcat-readme]).

Costs: no mobile client (install matrix: Linux, macOS, Windows, FreeBSD/OpenBSD, browser wasm), the
browser demo is DERP-only and experimental, the API/CLI/wire format carry no stability promises, and
the default public relays are explicitly rate-limited with no uptime or throughput target and may be
revoked at any time. A self-hosted `derper` removes the dependency ([tailcat-readme]).

### 1.3 tsnet (embedding the Tailscale client)

`tsnet` embeds a Tailscale node directly in a Go program: userspace gVisor TCP/IP stack, no root, no
system daemon, multiple independent nodes per process, node state in a directory you control, and
`ControlURL` to point at headscale. API: `Listen`, `ListenTLS`, `ListenSSH`, `ListenPacket`, `Dial`,
`HTTPClient`, `WhoIs` (caller identity), `LocalClient`, `Loopback` (SOCKS5 + LocalAPI), `Ephemeral`,
`Hostname`, `Dir`, `AuthKey`, `AdvertiseTags` ([tsnet docs][tsnet]). Funnel and Tailscale Services
listeners exist but need Tailscale-control-plane machinery (and headscale marks Funnel unsupported),
so they are out of bounds for pluto.

### 1.4 Plain WireGuard

WireGuard itself is a data plane with a deliberately narrow scope: "All issues of key distribution and
pushed configurations are out of scope." It has cryptokey routing, built-in roaming (a peer's endpoint
is learned from authenticated packets), kernel performance, and apps for iOS/Android — but no STUN, no
hole punching, no relay, and no coordination ([WireGuard][wg], [install][wg-install]). Behind NAT with
no port forwarding, two WireGuard peers simply cannot find each other; that is exactly the problem
magicsock/tailcat/headscale exist to solve. Use it where both ends have stable public endpoints (or
inside the DERP/magicsock path, which is WireGuard anyway).

---

## 2. Can official Tailscale clients point at a self-hosted control server?

**Yes, and it is now first-party documented.** Tailscale's own docs describe configuring a custom
control server URL for macOS (UI and CLI), Windows (CLI or `LoginURL` registry value), Linux (CLI),
iOS ("Use a custom coordination server"), Android ("Use an alternate server"), and tvOS ([tt-custom]).

Platform notes that affect experience:

- **iOS/Android**: in-app setting, a few taps; sign-in is non-SSO (headscale web-auth page or OIDC)
  ([tt-custom], [headscale Apple guide][hs-apple]).
- **macOS**: the UI path only appears if another tailnet is already authenticated; otherwise use
  `tailscale login --login-server`, or Option-click → Debug → Custom Login Server ([tt-custom]).
- **Windows**: the UI does not support custom servers; use `tailscale login --login-server` or the
  registry value ([tt-custom]).
- **Linux**: `tailscale login --login-server=<URL>`; system install needs root, which matters for the
  daily-driver host (hence `tsnet` there).
- **tvOS** also supported, if that ever matters.

Headscale states it aims to support the **last 10 Tailscale client releases** on Linux, OpenBSD,
FreeBSD, Windows, Android, macOS, iOS, and tvOS ([hs-clients]).

What it costs:

- **Feature gaps**: Funnel, Serve, and network-flow logs are unsupported; device posture and IP sets
  are unsupported; OIDC groups can't be referenced in ACLs ([hs-feat], [hs-policy]). In particular
  there is no `tailscale serve`/`funnel` under headscale — publish HTTP with a plain reverse proxy or a
  per-node listener instead.
- **Newer Tailscale features may lag**: Tailscale Services, Funnel, flow logs, app-capability headers
  and similar control-plane-coupled features are not in headscale's support matrix. Treat anything not
  listed as unverified.
- **Auth surface**: headscale supports interactive web auth (approve with
  `headscale auth register --user <u> --auth-id <id>`), pre-authenticated keys for automation, tags,
  and OIDC. There is no admin UI — headscale CLI/API only, which suits pluto's no-UI stance
  ([hs-reg], [headscale features][hs-feat]).
- **Defaults are privacy-friendlier than Tailscale SaaS**: headscale ships with `logtail.enabled:
  false` (client logs would otherwise go to Tailscale Inc.), `auto_update.enabled: false`, and node
  key expiry `0` (no default expiry; Tailscale SaaS uses 180 days) ([config-example][hs-config]).
- **One external dependency by default**: out of the box, headscale serves Tailscale's public DERP map
  (`https://controlplane.tailscale.com/derpmap/default`) to clients. You can empty `derp.urls` and run
  the embedded DERP, at the cost of a single point of failure ([headscale DERP docs][hs-derp],
  [hs-config]).
- **Derived client behavior**: if the headscale server is down, already-paired devices keep talking
  (cached netmap, ACLs, WireGuard sessions, persisted DERP map); new devices can't join and keys/ACLs
  can't change ([coordination server down][tt-cp-down]). That is acceptable for a personal fleet but is
  a central coordinator — not a consensus service for leases, but a single point for *access*.

---

## 3. DERP, NAT traversal, and relays

### 3.1 What DERP is for

DERP servers (1) negotiate/establish connections and (2) relay when no direct or peer-relay path
exists. They forward already-encrypted WireGuard packets and can't decrypt them; most traffic only uses
DERP as a low-bandwidth side channel during NAT traversal ([DERP servers][tt-derp]). Tailscale's NAT
traversal writeup describes the mechanics (STUN endpoint discovery, simultaneous send, birthday-paradox
probing, port-mapping protocols, CGNAT, hairpinning) and estimates a direct path for roughly 90% of
pairs with DERP guaranteeing connectivity otherwise ([NAT traversal][tt-nat]). The same magicsock logic
runs under `tsnet`, official clients, and tailcat.

### 3.2 Self-hosted DERP

Two routes:

1. **headscale embedded DERP** (`derp.server.enabled: true`): requires TLS already in place
   (`server_url` must be https), opens TCP/443 and UDP/3478 (STUN must be defined), defaults to region
   ID 999, `verify_clients: true`, and can advertise the server's public IPv4/IPv6 for stability. It is
   merged with any other DERP maps; `urls: []` removes Tailscale's public regions — with the docs
   warning that a sole DERP is a single point of failure. External `derper` instances can verify
   against headscale's `/verify` endpoint instead of a local `tailscaled` ([hs-derp]).
2. **Standalone `cmd/derper`**: Tailscale calls custom DERP an *alpha* advanced operation. It needs
   direct internet (no NAT, no load balancer), inbound and outbound ICMP, and TCP/80, TCP/443,
   UDP/3478; one DERP server per region; must be rebuilt from source to stay compatible with client
   updates; monitor with `derpprobe`. `--verify-clients` (local tailscaled) or `--verify-client-url`
   (headscale `/verify`, fail-open configurable) restricts relays to your tailnet
   ([custom DERP][tt-custom-derp], [derper README][derper-readme]).

Because pluto's first host is a daily-driver workstation that may be behind NAT, **where the DERP
server lives is a real decision**: headscale's docs require a public IP for the headscale server
itself, and derper explicitly refuses to work behind NAT or a load balancer ([hs-req],
[tt-custom-derp]).

### 3.3 Public relays and rate limits

- Tailscale's hosted DERP servers are shared and treated as a shared resource; the tailcat
  announcement says plainly: "If you use Tailscale-hosted DERP servers, those are rate-limited
  (bandwidth costs us money)" ([tailcat-blog]). The tailcat-run public fleet is explicitly throttled,
  region-limited, has no SLA, and can be revoked ([tailcat-readme]).
- Tailscale does not publish numeric rate limits for its public DERP fleet. Treat the number as
  unknown and assume low for sustained throughput; direct paths or a self-hosted/peer relay are the
  fix.
- The open-source server now supports per-client and global receive rate limiting (`v1.98+`,
  `--per-client-rate-limit`, `--rate-config` JSON reloadable on SIGHUP). The derper README's guidance:
  don't rate-limit UDP STUN, don't rate-limit outbound TCP, only inbound ([derper README][derper-readme],
  [derpserver docs][derpserver]).
- **Peer relays** are the in-tailnet alternative: a tagged device relays for peers when direct
  connection fails, before DERP is used, with lower latency and no egress costs; access is controlled
  by a grant with `tailscale.com/cap/relay`, and the relay device should have a stable network and an
  open UDP port. Headscale lists peer relays as supported ([peer relays][tt-peer-relay], [hs-feat]).
  For pluto, a well-connected host could serve as the relay instead of (or alongside) DERP.

### 3.4 Expectations for pluto

- Host ↔ host and client ↔ host on the same LAN: direct magicsock paths, no relay involvement.
- Laptop on hotel/cellular: usually direct after hole punching; DERP otherwise. The relayed path is
  fine for SSH/TUI, tolerable for opencode/T3 HTTP, poor for bulk git or disk-state sync.
- Phone on cellular (especially CGNAT): likely DERP-relayed; fine for attach, not for transferring a
  large build artifact.
- Without a public endpoint there is no self-hosted control plane at all, so the fleet's access story
  collapses to tailcat-style out-of-band addresses with public DERP — laptop/desktop only.

---

## 4. Host-relay vs per-box overlay identity

### 4.1 The two models

**Host-relay.** The host agent is the only tailnet node; boxes have no overlay identity. Clients reach
the host agent (tsnet `Listen`/`ListenSSH`/HTTP proxy), which bridges into the box over vsock (or a
Unix socket). Firecracker's vsock is a concrete example of the channel: the host connects to the VM's
`uds_path` and sends `CONNECT <port>\n`; guest-initiated connections land on `<uds_path>_<port>`; the
docs' examples use `socat` ([Firecracker vsock][fc-vsock]). The VMM choice is another ticket, but any
virtio-vsock-capable VMM gives this shape.

**Per-box overlay identity.** Each box is a tailnet node. Two implementations:

- *Guest `tailscaled`*: the box runs the full client; its disk state (node key, WireGuard keys,
  `tailscaled.state`) lives in the box image/snapshot. This is the classic pattern and the one with
  the fork problem.
- *Host-side `tsnet` per box*: the host agent runs one `tsnet.Server` per box (unique `Dir`,
  `Hostname`, optional `Ephemeral`) and proxies the netstack's TCP connections into the box over
  vsock. The box image contains no overlay identity at all.

### 4.2 Pause

- Host-relay: pause is purely local; nothing exists in the control plane to update. Resuming reuses
  the same box name in the agent's routing table.
- Per-box, guest client: pausing the VM makes the node go offline; resuming with the same disk state
  reconnects with the same node key and IP. Cheap, but the identity lives in the snapshot.
- Per-box, host-side `tsnet`: stop the `tsnet.Server` on pause (node offline), restart with the same
  `Dir` on resume (same identity/IP). Identity lives outside the snapshot, which is the point.

### 4.3 Fork

This is where the models diverge sharply.

- Host-relay: fork = copy the disk state and start a second VM; give it a new box name in the agent.
  No control-plane work, no key collision.
- Per-box, guest client: cloning disk state duplicates the node key and `tailscaled.state`. Tailscale
  documents exactly this failure — devices made by restoring a backup or cloning a filesystem show a
  **"Duplicate node key"** badge, and the fix is to completely delete the Tailscale state on one of
  them so it gets a new IP ([duplicate 100.x][tt-dup]). So a fork must deliberately wipe/re-provision
  the overlay identity in the clone; otherwise both machines claim the same node and connections are
  nondeterministic.
- Per-box, host-side `tsnet`: fork creates a new `tsnet.Server` with a fresh `Dir`/`Hostname` and a
  preauth key; the clone gets its own identity automatically. No duplicate keys are possible because
  the identity was never in the box disk.

### 4.4 Undo / cleanup cost

Headscale's node lifecycle CLI is the undo surface:

- `headscale nodes expire --identifier <id>` keeps the node in the DB and forces re-authentication;
  `--disable` disables key expiry entirely (node never expires) ([headscale CLI nodes.go][hs-cli-nodes]).
- `headscale nodes delete --identifier <id>` removes it; the device must register again with a key
  ([hs-cli-nodes]).
- Ephemeral nodes are auto-deleted after inactivity: headscale's default
  `node.ephemeral.inactivity_timeout: 30m` ([hs-config]).
- Node key expiry defaults to `0` (never) in headscale, so stale nodes do not clean themselves up
  unless you delete them or use ephemeral registration ([hs-config]).

Consequences:

- Host-relay undo = delete the box's local state; zero control-plane residue. The cheapest option.
- Per-box identity undo = `nodes delete` (or ephemeral timeout). Each fork/destroy leaves a record
  until cleaned; a household fleet will want a `pluto destroy` hook that calls headscale, or ephemeral
  registration plus accepting identity loss on resume after 30 minutes offline.
- Ephemeral vs persistent is a real trade: ephemeral gives free cleanup, but a deleted ephemeral node
  comes back with a new identity/IP when recreated ([ephemeral nodes][tt-ephemeral]) — so a paused box
  resumed after the timeout pays a re-registration cost and loses its previous address.

### 4.5 Auth

- Host-relay: auth is decided by the host agent. A `tsnet` agent gets caller identity from
  `LocalClient().WhoIs()` and can put it in HTTP headers or SSH session objects (`tsnet.ListenSSH`
  yields sessions carrying the peer's Tailscale identity) ([tsnet][tsnet]). Headscale's policy (ACLs,
  grants, Tailscale SSH rules) still gates who can reach the host agent at all
  ([hs-policy], [Tailscale SSH][tt-ssh]).
- Per-box identity: each box gets its own ACL line/tag; more granular, more records to keep in sync.
  For M0 this is not worth it.
- tailcat alternative: `tailcat serve --allow=nodekey:<client>` pins client keys; with `no-auth-ssh`
  the address is the shell credential, which the docs warn is unsafe to publish ([tailcat-readme]).
  No ACLs, no identity map: worse ergonomics for a fleet, fine for a quick laptop↔host pipe.

### 4.6 Recommendation on identity

M0: **host-relay, no per-box identity**. Add per-box `tsnet` nodes only when per-box MagicDNS names,
per-box ACLs, or direct box connections are demonstrably needed. Never bake guest `tailscaled` state
into box images unless fork is explicitly re-keyed.

---

## 5. Concrete access paths

### 5.1 SSH / TUI

**Headscale + host-relay (recommended):**

- Host agent runs `tsnet` and `ListenSSH(":22")` (needs `import _ "tailscale.com/feature/ssh"`).
  Clients connect with any SSH client; authentication is the tailnet identity ("open SSH ... the SSH
  client and server will still create an encrypted SSH connection, but it will not be further
  authenticated"), gated by headscale `ssh` policy rules ([tt-ssh], [tsnet]).
- The agent presents a session; `pluto attach <box>` (or a per-box command) bridges that session to
  the box's vsock shell/console. Box-side sshd on vsock is the alternative.
- Tailscale SSH limitations to respect: port 22 only, server component on Linux (and macOS open-source
  client), and *it cannot reach devices that are not tailnet nodes behind a subnet router* — which is
  precisely why the agent must terminate the SSH session and relay the bytes itself, rather than
  expecting the box to be Tailscale-SSH-able ([tt-ssh]).
- Known client quirk: some SSH clients can't do Tailscale's no-auth dance; the documented workaround is
  appending `+password` to the username and typing any password ([tt-ssh]).

**Headscale + per-box identity (later):** each box is its own tsnet node; `ssh <box-name>` via MagicDNS
directly. Same Tailscale SSH rules; loses the single choke point.

**tailcat:** `tailcat serve --ssh-authorized-keys=~/.ssh/authorized_keys ssh` prints an address; the
client runs `tailcat ssh <addr>`. No overlay names, no ACLs; works from Linux/macOS/Windows only
([tailcat-readme]).

**Plain WireGuard:** host must have a reachable endpoint; ssh to the tunnel IP. No roaming rendezvous,
so a laptop changing networks needs a hub with a public endpoint.

### 5.2 HTTP UIs (opencode, T3 Code, anything HTTP)

Any HTTP app in the box binds loopback (e.g. `opencode serve` defaults to `127.0.0.1:4096`, exposes an
OpenAPI doc at `/doc`, and supports HTTP basic auth via `OPENCODE_SERVER_PASSWORD`; `opencode web`
serves a browser UI) ([opencode server docs][oc-server]).

- **Host-relay:** the host agent runs a reverse proxy on its tsnet node and maps
  `http://<host-name>/box/<box>/` (or a per-box port) to the box's loopback address over vsock. The
  agent can read `WhoIs` to tag requests with tailnet identity. Port 80 can be bound *inside the tsnet
  userspace stack without root*, which fits the no-sudo host constraint.
- **Per-box identity:** the per-box `tsnet.Server` `Listen`s on the box's behalf (e.g. port 4096) and
  the UI is `http://<box-name>.<base-domain>:4096/`. Simplest URL story, more nodes.
- Do **not** plan on `tailscale serve`/Funnel: headscale marks both unsupported ([hs-feat]). `tsnet`'s
  `ListenFunnel`/`ListenService` also depend on Tailscale control-plane features and are out of scope.
- T3 Code (or any external HTTP client) points at the same URL; nothing special is needed beyond
  reachability and auth.

### 5.3 Git

The pluto model is "code in and results out are git", so both directions matter:

- **Box egress:** boxes push/pull to their origin (GitHub or a host-side bare repo) over the host's
  normal network; no overlay identity needed. This is the simplest, and probably the M0 answer.
- **Git over SSH to/from a box (host-relay):** the host agent's SSH server can dispatch `exec`
  requests, so `git` can use `GIT_SSH_COMMAND='ssh -o ...'` and the agent can route `git-upload-pack` /
  `git-receive-pack` into the box. tailcat ships this exact pattern in its docs:
  `tailcat serve no-auth-ssh -- git-upload-pack /srv/repo.git` and `exec` with per-peer key
  ([tailcat-readme]).
- **Git over Tailscale SSH (per-box or host):** works (Tailscale SSH supports SFTP/SCP); watch the
  `username+password` quirk for clients that can't do no-auth ([tt-ssh]).
- **Git over HTTP:** proxy via the same host reverse proxy as web UIs.

### 5.4 Host ↔ host

Hosts join the same headscale tailnet as `tsnet` nodes (or system `tailscaled` on hosts where root is
acceptable). This is how image distribution, the S3 endpoint, and any host-to-host control traffic
travel: magicsock direct when possible, DERP/peer relay otherwise ([DERP][tt-derp],
[peer relays][tt-peer-relay]). Nothing about the box relay model prevents host-to-host direct links;
the box is the only layer that goes through the host agent.

---

## 6. Phone, end to end, under the recommendation

1. **One-time fleet setup.** Headscale runs at `https://hs.example` on a public endpoint; embedded DERP
   enabled (443/TCP, 3478/UDP) with `verify_clients: true`; MagicDNS base domain configured; a policy
   file with `tagOwners` for `tag:host` and an `ssh` rule allowing members → `tag:host`
   ([hs-derp], [hs-config], [hs-policy]).
2. **Host agent.** First host runs the pluto agent as a `tsnet` node (`ControlURL=https://hs.example`,
   preauth key or OIDC), no root, no system changes; it serves SSH (Tailscale SSH) and an HTTP proxy
   that routes to boxes over vsock ([tsnet], [fc-vsock]).
3. **Phone.** Install the official Tailscale app → Settings → "Use a custom coordination server"
   (iOS) / "Use an alternate server" (Android) → enter `https://hs.example` → sign in via headscale
   web auth (approve with `headscale auth register --user ... --auth-id ...`) or OIDC
   ([tt-custom], [hs-apple], [hs-reg]).
4. **Attach (TUI).** Open any SSH client on the phone (Termius, Blink, and similar) →
   `ssh pluto-host1` using the MagicDNS name. Tailscale SSH authenticates by node identity and ACL;
   the agent drops the session into `pluto attach <box>` ([tt-ssh], [MagicDNS][tt-magicdns]). If the
   client rejects no-auth SSH, use `pluto-host1+password` with any password ([tt-ssh]).
5. **Web UI.** Phone browser → `http://pluto-host1/box/<box>/` (host-relay), or later
   `http://<box>.<base-domain>:4096/` (per-box identity). opencode's password env adds a second factor
   inside the box ([oc-server]).
6. **Git.** From the phone: Termux + `git` over SSH through the agent, or (more likely) the box does
   git work itself and pushes to origin; the phone only inspects or issues TUI commands.
7. **Fallback behavior.** On cellular/CGNAT the path may be DERP-relayed; attach works, bulk transfer
   doesn't. If headscale is down, the phone and host still talk on cached keys/ACLs, but nothing new
   can join and policy changes don't propagate ([tt-cp-down]).

---

## 7. Uncertainty and open questions

1. **Public endpoint.** Headscale requires a public IP + HTTPS/443; embedded DERP adds UDP/3478; a
   standalone derper explicitly must not be behind NAT. Does the first host have a public IP (or
   IPv6)? Is a small VPS acceptable under "self-host everything" as the fleet lighthouse? This gates
   the whole recommendation.
2. **Phone auth choice.** Headscale web auth (CLI approval with an auth-id) vs OIDC. OIDC is more
   seamless on the phone but OIDC groups can't be used in ACLs ([hs-feat]); for a one-person fleet
   that's likely fine.
3. **Identity policy for boxes.** Confirm M0 host-relay (recommended) and the eventual trigger for
   per-box `tsnet` nodes. If per-box: persistent identities (manual `nodes delete`) vs ephemeral (auto
   cleanup, new IP after 30 min offline).
4. **DERP placement and count.** One embedded DERP on the lighthouse is a documented single point of
   failure; Tailscale public DERP is rate-limited with no SLA; peer relays are supported by headscale
   but need a stable host with an open UDP port. Which mix for M0?
5. **Do we need Taildrive/Taildrop** for artifact movement, or is git-over-SSH sufficient? Both are
   headscale-supported, so this is a product choice, not a blocker ([hs-feat]).
6. **Unlisted features.** Tailscale Services, `tailscale serve`, Funnel, IP sets, and device posture
   are not (or not fully) supported by headscale; nothing in pluto's current design needs them, but
   verify before designing around them ([hs-feat], [hs-policy]).
7. **Client autoupdate/skew.** Headscale targets the last 10 Tailscale client releases; pluto should
   pin/observe client versions on laptop/phone or expect occasional breakage windows
   ([hs-clients]).
8. **tailcat as fallback.** Worth keeping in the back pocket for laptop↔host when headscale/DERP are
   down (address exchange out of band), but its lack of mobile clients and stable API keeps it out of
   the critical path ([tailcat-readme]).

---

## Sources

Primary sources only; all links fetched 2026-10-04.

- [tt-custom] Tailscale, *Configure Tailscale clients to use a custom control server* — https://tailscale.com/docs/how-to/set-up-custom-control-server
- [tt-derp] Tailscale, *DERP servers* — https://tailscale.com/docs/reference/derp-servers
- [tt-custom-derp] Tailscale, *Custom DERP servers* — https://tailscale.com/docs/reference/derp-servers/custom-derp-servers
- [tt-nat] Tailscale, *How NAT traversal works* — https://tailscale.com/blog/how-nat-traversal-works
- [tt-peer-relay] Tailscale, *Tailscale Peer Relays* — https://tailscale.com/docs/features/peer-relay
- [tt-ssh] Tailscale, *Tailscale SSH* — https://tailscale.com/docs/features/tailscale-ssh
- [tt-ephemeral] Tailscale, *Ephemeral nodes* — https://tailscale.com/docs/features/ephemeral-nodes
- [tt-magicdns] Tailscale, *MagicDNS* — https://tailscale.com/docs/features/magicdns
- [tt-dup] Tailscale, *Troubleshoot multiple devices with the same 100.x IP address* — https://tailscale.com/docs/reference/troubleshooting/network-configuration/multiple-devices-same-100-x-ip-address
- [tt-cp-down] Tailscale, *What happens if the coordination server is down?* — https://tailscale.com/docs/reference/coordination-server-down
- [tsnet] Tailscale, `tailscale.com/tsnet` package docs (v1.104.0) — https://pkg.go.dev/tailscale.com/tsnet
- [derper-readme] Tailscale, `cmd/derper` README — https://github.com/tailscale/tailscale/blob/main/cmd/derper/README.md
- [derpserver] Tailscale, `tailscale.com/derp/derpserver` (RateConfig, v1.98) — https://pkg.go.dev/tailscale.com/derp/derpserver
- [tailcat-readme] Tailscale, *tailcat* README — https://github.com/tailscale/tailcat
- [tailcat-blog] Tailscale, *Tailcat: Tailscale without Tailscale, by Tailscale* (2026-08-31) — https://tailscale.com/blog/tailcat
- [hs-readme] juanfont/headscale README — https://github.com/juanfont/headscale
- [hs-feat] Headscale, *Features* — https://headscale.net/stable/about/features/
- [hs-clients] Headscale, *Client and operating system support* — https://headscale.net/stable/about/clients/
- [hs-req] Headscale, *Requirements and Assumptions* — https://headscale.net/stable/setup/requirements/
- [hs-config] Headscale, `config-example.yaml` — https://github.com/juanfont/headscale/blob/main/config-example.yaml
- [hs-reg] Headscale, *Registration methods* — https://headscale.net/stable/ref/registration/
- [hs-derp] Headscale, *DERP* — https://headscale.net/stable/ref/derp/
- [hs-policy] Headscale, *Policy* — https://headscale.net/stable/ref/policy/
- [hs-apple] Headscale, *Apple* client guide — https://headscale.net/stable/usage/connect/apple/
- [hs-cli-nodes] Headscale CLI node commands — https://github.com/juanfont/headscale/blob/main/cmd/headscale/cli/nodes.go
- [wg] WireGuard, project overview — https://www.wireguard.com/
- [wg-install] WireGuard, installation (iOS/Android clients) — https://www.wireguard.com/install/
- [fc-vsock] Firecracker, *Using the Firecracker Virtio-vsock Device* — https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
- [oc-server] opencode, *Server* — https://opencode.ai/docs/server/
