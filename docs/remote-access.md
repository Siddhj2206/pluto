# Remote access: reach your boxes from another machine

pluto is local-first: one host's daemon owns the boxes, and the CLI talks to it
over a unix socket. From a second machine you reach that socket through SSH —
`pluto --device` runs the same CLI on the host — and you reach an in-box web UI
through an `ssh -L` forward. There is no relay, no control plane, and the daemon
opens no ports; the route between your machines is your choice (ADR 0006).

## Prerequisites

- A host running the pluto daemon (`pluto install` for the systemd user
  service, or `pluto daemon` in the foreground) with at least one box
  (`pluto up` from a worktree).
- A second machine with an SSH client. Install pluto there as well if you want
  `pluto device` and `pluto --device`; the host needs `pluto` on the `PATH`
  that ssh sees, which is not the same as an interactive shell's:
  `ssh siddhant@neptuno pluto version` is the check.
- Passwordless SSH into the host (`ssh-copy-id siddhant@neptuno`). pluto stores
  no secrets; SSH keys are the authentication.
- A route from the client to the host's sshd: one of the recipes below.

`--device` re-executes on the host, so the host's pluto is what runs. Keep the
client reasonably current so help and checks match.

## Reach the host

### Tailnet

If both machines are on a Tailscale (or headscale) tailnet:

```sh
tailscale up          # on both machines
ssh siddhant@neptuno  # MagicDNS name, or the 100.x.y.z tailnet address
```

Tailnet ACLs can restrict who reaches port 22. pluto does not manage the
tailnet: it embeds no tsnet node and ships no relay.

### WireGuard

A point-to-point tunnel with no third party. Host `wg0.conf`:

```ini
[Interface]
Address = 10.7.0.1/24
ListenPort = 51820
PrivateKey = <host private key>

[Peer]
PublicKey = <client public key>
AllowedIPs = 10.7.0.2/32
```

Client `wg0.conf`:

```ini
[Interface]
Address = 10.7.0.2/24
PrivateKey = <client private key>

[Peer]
PublicKey = <host public key>
Endpoint = home.example.net:51820
AllowedIPs = 10.7.0.1/32
PersistentKeepalive = 25
```

```sh
wg-quick up wg0        # on both machines
ssh siddhant@10.7.0.1
```

The host side needs UDP 51820 reachable, which usually means a port-forward or
a public address.

### Plain port-forward

Forward a port on the router to the host's sshd:

```text
home.example.net:2222  ->  <host>:22
```

```sh
ssh -p 2222 siddhant@home.example.net
```

An sshd on the internet gets scanned: use keys only
(`PasswordAuthentication no`) and keep `authorized_keys` tight. Put the details
in `~/.ssh/config` so everything else can use one nickname:

```text
Host neptuno
    HostName home.example.net
    Port 2222
    User siddhant
```

## Drive the CLI remotely

`pluto device` keeps a client-side registry at `~/.config/pluto/devices.toml`.
`pluto --device <nickname|user@host>` re-executes the same command on that
machine over SSH, preserving argv and streaming output and exit codes.

```sh
pluto device add neptuno siddhant@neptuno
pluto device ls
pluto device rm neptuno
```

`device add` verifies the target with `ssh siddhant@neptuno pluto version` and
warns without failing when it cannot answer. Then the box commands work
remotely:

```sh
pluto --device neptuno ls
pluto --device neptuno up --worktree /home/siddhant/src/app   # the worktree lives on the host
pluto --device neptuno status <box>
pluto --device neptuno run <box> -- pnpm test
pluto --device neptuno attach <box>
pluto --device neptuno logs <box>
pluto --device neptuno pause <box>
pluto --device neptuno destroy <box> --yes
```

An unsaved target works too:

```sh
pluto --device siddhant@neptuno status <box>
```

Notes:

- Commands run on the host, so targets resolve there: pass box ids (`pluto
  --device neptuno ls`) or paths that exist on the host.
- `attach` and `run` stream both ways; exit codes pass through.
- The registry is client-side and holds no secrets; the daemon knows nothing
  about devices.
- Early drafts called the flag `--host`; it is `--device` now so `-h` stays
  help (ADR 0006).

## Reach an in-box web UI

opencode and T3 Code serve HTTP on loopback inside a box, and their clients
want a stable origin at the root of a host (`http://localhost:4096`, not a
per-box hostname and not a path prefix). The guest sshd is the bridge: an
`ssh -L` through it pins a local port to a box's loopback.

The guest sshd is not on a TCP port on the host; it is reached through the
box's Firecracker vsock socket. `pluto attach` builds the ssh invocation that
does this; until `pluto forward` lands, add `-L` by hand. On the host, for the
box you want:

```sh
box=<box id or 8-char prefix>
id=$(pluto status "$box" | awk '/^id:/ {print $2}')
state=${XDG_STATE_HOME:-$HOME/.local/state}/pluto
box_dir=$state/boxes/$id

pluto attach "$box"      # wake the box, then leave the shell it opens

ssh -N \
    -i "$box_dir/id" \
    -o IdentitiesOnly=yes \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR \
    -o ExitOnForwardFailure=yes \
    -o ProxyCommand="pluto vsock connect '$box_dir/v.sock' 22" \
    -L 4096:127.0.0.1:4096 \
    dev@box
```

Now <http://localhost:4096> on the host is the box's UI. The ssh options are
the ones `pluto attach` uses: the box's own key, a host key that changes with
each boot, and `pluto vsock connect` as the transport to the guest sshd (`dev`
is the box user; the hostname is a placeholder the ProxyCommand ignores).

If the browser is on another machine, keep that forward running on the host and
add one more hop from the client:

```sh
ssh -N -L 4096:127.0.0.1:4096 siddhant@neptuno
```

Now <http://localhost:4096> on the client is the box's UI. Pin the local port
and keep it the same between sessions: opencode keys saved servers by URL, so a
stable port keeps the saved server valid.

### Start the UI in the box

Declare it as a service so it survives pause/wake:

```toml
[services.opencode]
description = "web UI"
command = "opencode serve --port 4096"
port = 4096
```

Or start it in an attach session for a quick look: `pluto attach <box>`, then
run the command there.

T3 Code's server defaults to port 3773; run `t3 pair` in the box and point its
pairing URL at the forwarded origin, e.g. `http://localhost:3773/pair#token=...`.

Keep the UI on loopback in the box and keep its own auth on (opencode's
`OPENCODE_SERVER_PASSWORD`, T3's pairing): the tunnel is transport, not
authorization.

### `pluto forward` is deferred

Waking the box, resolving the socket, and adding `-L` for you is the
`pluto forward` convenience, deliberately not in v1; the manual recipe above is
the sanctioned path. Its revive trigger lives in [DEFERRED.md](DEFERRED.md),
alongside the relay that native browser and phone clients will need.
