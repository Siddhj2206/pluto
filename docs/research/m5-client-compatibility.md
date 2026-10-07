# M5 interactive client compatibility

Research for [issue #105](https://github.com/Siddhj2206/pluto/issues/105), under the [M5 specification](https://github.com/Siddhj2206/pluto/issues/101). Source review performed 2026-10-07. These findings are desk research; a hands-on connection against a Pluto box remains necessary before claiming runtime verification.

## OpenCode v2

OpenCode v2 Desktop is the reference client for an in-box OpenCode server. The v2 documentation describes two connection paths:

- **SSH server connection:** Desktop connects to a remote host, installs a matching OpenCode CLI there, and tunnels its service. The feature was added in [PR #47753](https://github.com/anomalyco/opencode/pull/47753); the maintainer's setup instructions are in [issue #7790](https://github.com/anomalyco/opencode/issues/7790). This is the preferred path when the Pluto host is reachable over SSH.
- **Add server / pairing:** Run `opencode serve`, then connect Desktop to its URL using the service's own authentication or pairing. The server listens on localhost by default; the [v2 web docs](https://opencode.ai/v2/docs/cli/web/) describe pairing and SSH port forwarding. Pairing uses the shared service and is unavailable when that service is disabled.

The v2 web documentation gives `ssh -L 49374:127.0.0.1:49374 <host>` as a tunnel pattern. The server's protocol and authentication remain authoritative; Pluto should only prepare the box, service, and route. OpenCode Desktop v1 should not be claimed as compatible for remote use. The native SSH path is new and actively maintained, so verify the supported Desktop and CLI versions during hands-on validation.

## T3 Code

T3 Code is not a direct client for an OpenCode server: its clients speak the T3 server protocol. T3 Code's OpenCode provider can target an existing OpenCode server, so a supported arrangement would run both services in the box: T3 client → T3 server → OpenCode server. T3's own remote-access methods likewise require a T3 server; T3 Connect uses an account and managed relay.

Do not claim that T3 Code connects directly to Pluto's in-box OpenCode service. If the two-hop arrangement is later supported, disclose its extra service and any account or relay requirements. Sources: [T3 remote access](https://github.com/pingdotgg/t3code/blob/main/docs/user/remote-access.md), [T3 OpenCode provider](https://github.com/pingdotgg/t3code/blob/main/docs/user/providers-opencode.md), and [T3 remote architecture](https://github.com/pingdotgg/t3code/blob/main/docs/internals/remote.md).

## Validation still required

### Hands-on OpenCode CLI check (2026-10-07)

A real Pluto box was booted from `m5/integration` at `60d1248`, with a declared `opencode serve` service. OpenCode CLI **2.0.24** in the box and on the host were the server and client. Using the documented guest SSH tunnel, the host CLI queried `/api/info`, `/api/config`, `/api/agent`, and `/api/session`; the responses showed the in-box project directory, and `opencode --server ...` rendered the remote TUI. No model call or credentials were used. The isolated scratch box, daemon, tunnel, and files were removed afterward.

Reproducible outline (replace placeholders; keep the password private):

1. In the project contract declare a service with `command = "opencode serve --hostname 127.0.0.1 --port 4096"`, `port = 4096`, and `OPENCODE_SERVER_PASSWORD = "<box-password>"`; provision OpenCode CLI 2.0.24 in the box.
2. Start the box with `pluto up`, then forward its guest loopback port over the box's SSH/vsock path, as described in [remote access](../remote-access.md):

   ```sh
   box=<box-id>
   state=${XDG_STATE_HOME:-$HOME/.local/state}/pluto
   box_dir=$state/boxes/$box
   ssh -N -i "$box_dir/id" -o IdentitiesOnly=yes \
     -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
     -o ExitOnForwardFailure=yes \
     -o "ProxyCommand=pluto vsock connect $box_dir/v.sock 22" \
     -L 14096:127.0.0.1:4096 dev@box
   ```

3. From a second terminal, run `OPENCODE_SERVER_PASSWORD='<box-password>' opencode api --server http://127.0.0.1:14096 GET /api/info` and `OPENCODE_SERVER_PASSWORD='<box-password>' opencode --server http://127.0.0.1:14096`. The first returns the server version; the second opens the TUI. The service requires HTTP Basic authentication (username `opencode`); the CLI also accepts `OPENCODE_PASSWORD`. A client without a password fails clearly. The server binds loopback by default, so remote access requires an explicit tunnel or provider route.

This verifies the OpenCode v2 CLI over the same server protocol, not the Desktop GUI. Desktop v2 remote SSH and pairing paths remain documented above, but the installed Desktop build was not running/scriptable during the check. T3 Code's direct incompatibility is established by its own protocol documentation; its client could not be exercised because no CLI was available. A two-hop T3 → T3 server → OpenCode server remains a separate, unsupported setup.

The full isolated run record is `/tmp/pluto-m5-research-105-e2e.md`; it includes host/base-image details and cleanup evidence. Pluto's `connect` flow now prepares the box and declared service, waits for the guest service manager to report it active, and returns an SSH tunnel command plus a local endpoint. This process state is not an application-level HTTP readiness probe.

OpenCode Desktop v2's SSH server connection and Add Server/pairing paths remain documented options, but they have **not** been hands-on validated with Pluto. The installed `~/AppImages/opencode.appimage` is v2.0.14 and was not scriptable in this environment, so the v2.0.24 CLI test above is not Desktop GUI verification. Parent issue #101 should retain manual Desktop GUI verification as an outstanding review item. Pluto does not handle OpenCode authentication or pairing; clients must use the in-box server's own service authentication.
