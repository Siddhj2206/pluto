# Remote access: reach your boxes from another machine

pluto is local-first: one host's daemon owns the boxes, and the CLI talks to it
over a unix socket. From a second machine you reach that socket through SSH —
`pluto --device` runs the same CLI on the host. There is no relay, no control
plane, and the daemon opens no ports; the route between your machines is your
choice.

## Prerequisites

- A host running the pluto daemon (`pluto daemon` in the foreground) with at
  least one box (`pluto box create` from a workspace).
- A second machine with an SSH client. Install pluto there too if you want
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

`pluto device` keeps a client-side registry at
`~/.config/pluto/devices.yml`. `pluto --device <nickname|user@host>`
re-executes the same command on that machine over SSH, preserving argv and
streaming output and exit codes.

```sh
pluto device add neptuno siddhant@neptuno
pluto device ls
pluto device rm neptuno
```

`device add` verifies the target with `ssh siddhant@neptuno pluto version` and
warns without failing when it cannot answer. Then the box commands work
remotely:

```sh
pluto --device neptuno box ls
pluto --device neptuno box create --workspace /home/siddhant/src/app
pluto --device neptuno box status /home/siddhant/src/app
pluto --device neptuno run dev            # run from the workspace on the host
pluto --device neptuno attach
pluto --device neptuno ls runs
pluto --device neptuno box rm /home/siddhant/src/app --yes
```

An unsaved target works too:

```sh
pluto --device siddhant@neptuno box ls
```

Notes:

- Commands run on the host, so targets resolve there: pass box ids
  (`pluto --device neptuno box ls`) or paths that exist on the host.
- `attach` and `run` stream both ways; exit codes pass through.
- The registry is client-side and holds no secrets; the daemon knows nothing
  about devices.
