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

This review establishes documented connection methods and protocol boundaries, not a live connection to a Pluto box. Before marking #105 fully verified, record the tested OpenCode Desktop and CLI versions, connect to an in-box v2 server using SSH or pairing, and capture the successful steps and authentication requirements. A T3 direct-client test is unnecessary because its documented protocol boundary rules that path out; test the two-hop setup only if Pluto intends to claim support for it.
