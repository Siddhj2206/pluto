# Connect Windows to a Linux Pluto host

From a fresh Windows 11 x64 laptop to a saved device you can drive over SSH.
It covers the client side only; the Linux host must already run the Pluto
daemon (see [remote-access.md](../remote-access.md) for the host side and the SSH
recipes).

For the full client reference, see [windows-client.md](../windows-client.md). For
the supported workflow end to end, see
[windows-client-acceptance.md](../windows-client-acceptance.md).

## Quick start: one command

This is the whole setup. It assumes:

- Windows 11 with the OpenSSH client (`ssh -V` works).
- `pluto.exe` in the current directory (see [Get `pluto.exe`](#3-get-plutoexe)).
- The Linux host runs `sshd`, accepts password login for your account, and has
  `pluto` at `~/.local/bin/pluto` or on the PATH that ssh sees.
- The host address is reachable from Windows.

```powershell
.\pluto.exe device add neptuno sid@neptuno.local
```

The first time, it saves the nickname, asks for the **Linux account password
once**, installs your SSH key, and records where pluto lives on the host:

```text
saved device neptuno (sid@neptuno.local)
installing an ssh key on sid@neptuno.local (enter the account password once)
sid@neptuno.local's password:
connected: pluto at /home/sid/.local/bin/pluto
```

After that, every command is passwordless:

```powershell
.\pluto.exe --device neptuno version
.\pluto.exe --device neptuno ls
```

If the laptop already has a key the host accepts, `device add` skips the prompt
and just records the device. Running it again replaces the nickname and keeps
the setup idempotent.

## What `device add` does

1. Saves `neptuno -> sid@neptuno.local` in `%AppData%\pluto\devices.toml`.
2. Tries the saved target over ssh in `BatchMode=yes`, so it can never hang on
   a prompt.
3. If the host refuses the key, reuses your default SSH key or generates an
   unencrypted ed25519 keypair at `%UserProfile%\.ssh\id_ed25519`, then
   authorizes its public half on the host using the account password. The
   install is de-duplicated, so a retry does not append the key twice.
4. Finds the absolute path of the host's pluto binary: `command -v pluto`
   first, then `~/.local/bin/pluto`. The path is saved with the device, so
   later commands do not depend on the non-interactive ssh `PATH` and need no
   `sudo` symlink.
5. Verifies the whole path end to end.

A host that still does not answer is a warning, not a failure: it may simply be
offline, and the nickname stays saved for the next attempt.

## 1. Check the Linux host

You need the Linux machine's address (LAN name or IP, or a tailnet name) and a
user account on it. On the Linux host, check:

```sh
pluto version                      # the host CLI is installed
systemctl --user is-active pluto   # the daemon service is running
```

The host also needs a base image imported (`pluto image import images/out`) if
you intend to create boxes.

The client finds pluto the way a non-interactive ssh session would, falling
back to `~/.local/bin/pluto`:

```sh
ssh localhost 'command -v pluto || echo ~/.local/bin/pluto'
```

You do not need to link pluto into `/usr/local/bin` for the Windows client. If
you want the bare name to work in your own ssh sessions, that symlink is still
a reasonable convenience:

```sh
sudo ln -sf ~/.local/bin/pluto /usr/local/bin/pluto
```

## 2. Install the Windows OpenSSH client

Open PowerShell and check:

```powershell
ssh -V
```

If that is not recognized, add the optional feature (PowerShell as
Administrator):

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0
```

Close and reopen the terminal, then check `ssh -V` again. Windows 11 ships an
OpenSSH new enough for every option the client uses. The client generates its
own key with Go, so `ssh-keygen` is not required.

## 3. Get `pluto.exe`

Pick one of these.

**From a release.** Download `pluto.exe` from the GitHub Releases page for the
demo tag and put it in a directory on your `PATH`, for example
`$env:USERPROFILE\Downloads`. The demo branch has no release tagged yet; see
[releasing.md](../releasing.md) for how a maintainer cuts one.

**Build from the demo branch.** With Go installed on Windows:

```powershell
git clone https://github.com/Siddhj2206/pluto.git
cd pluto
git checkout demo/windows-client
go build -o $env:USERPROFILE\Downloads\pluto.exe .\cmd\pluto
```

**Cross-build on the Linux host and copy it** (no Go toolchain needed on
Windows). On the Linux host, in the repository:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o pluto.exe ./cmd/pluto
```

Then copy `pluto.exe` to the Windows machine.

Check the binary runs and reports a version:

```powershell
pluto.exe version
```

## 4. Save the device

This is the step that connects the two machines. The nickname is yours; the
destination is the same `user@host` you can reach from Windows.

```powershell
pluto.exe device add neptuno sid@neptuno.local
```

Expected, on a laptop with no key yet:

```text
saved device neptuno (sid@neptuno.local)
installing an ssh key on sid@neptuno.local (enter the account password once)
sid@neptuno.local's password:
connected: pluto at /home/sid/.local/bin/pluto
```

The first connection accepts the host key; a later key change is still
rejected. See [Host keys](#7-host-keys).

If the host is offline or unreachable, the nickname is still saved:

```text
warning: ssh sid@neptuno.local: ...
```

Fix connectivity and run `device add` again. `device add` replaces an existing
nickname. If the warning does not name the cause, run the probe by hand:

```powershell
ssh sid@neptuno.local 'command -v pluto'
```

You can skip the registry entirely for a one-off command by passing a raw
target: `pluto.exe --device sid@neptuno.local ls`.

List what is saved, and remove a nickname when you are done:

```powershell
pluto.exe device ls
pluto.exe device rm neptuno
```

A passphrase-protected key already on the laptop cannot be unlocked by the
passwordless probes. Either let `device add` create/use `id_ed25519`, or load
the protected key into the Windows ssh-agent:

```powershell
Get-Service ssh-agent | Set-Service -StartupType Automatic
Start-Service ssh-agent
ssh-add $env:USERPROFILE\.ssh\id_ed25519
```

## 5. Run commands on the host

Put `--device` before the command. The client replays the command on the Linux
host, so box ids, paths, and contracts resolve there.

```powershell
pluto.exe --device neptuno version        # the host's version, not the client's
pluto.exe --device neptuno ls             # boxes on the host
pluto.exe --device neptuno status <box>
pluto.exe --device neptuno logs <box>
```

Create a box from a repository URL, then run work in it:

```powershell
pluto.exe --device neptuno up --repo https://github.com/you/app.git
pluto.exe --device neptuno ls
pluto.exe --device neptuno job ls <box>            # declared jobs in the host worktree
pluto.exe --device neptuno run <box> test          # a declared job
pluto.exe --device neptuno run <box> -- pnpm test  # a one-off command
pluto.exe --device neptuno attach <box>            # interactive shell
pluto.exe --device neptuno pause <box>
```

`up --repo` clones the repository on the host and provisions there; nothing is
uploaded from Windows. To wake a paused box created from a URL, use the same
`up --repo <url>` again.

Exit status and both output streams come back to Windows, so a failing job
fails the PowerShell command with the job's code.

## 6. Choose the route

LAN is the baseline: use the host's LAN name or address in `device add`.

For a separate network, install Tailscale on both machines, sign in, and use
the host's MagicDNS name or its `100.x.y.z` tailnet address:

```powershell
tailscale up
pluto.exe device add neptuno sid@100.71.31.35
```

Tailnet ACLs should grant only your Windows device access to the host on TCP
22. Pluto does not manage the tailnet and adds no relay.

## 7. Host keys

The first connection records the host's key in
`%UserProfile%\.ssh\known_hosts`. A changed key is an error, by design.

```powershell
ssh-keygen -F neptuno.local              # is this host known, and with which key?
ssh-keygen -R neptuno.local              # remove the entry after a legitimate rebuild
ssh-keyscan neptuno.local                # compare the host's current key
```

Only accept a first-use key over an owner-controlled LAN or the authenticated
tailnet. Never route first use over a plain public path.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `exec: "ssh": executable file not found in %PATH%` | The OpenSSH client is missing. Do step 2. |
| `Permission denied` after `sid@...'s password:` | The account password was rejected, or the account cannot write `~/.ssh/authorized_keys`. Check the password and the host's sshd log. |
| `warning: ... permission denied` with no password prompt | The key already exists but the host refused it, and ssh could not prompt (for example a redirected stdin). Run `device add` from an interactive PowerShell window. |
| A passphrase prompt is skipped and the probe fails | An existing key is passphrase-protected; `BatchMode=yes` cannot unlock it. Load it into the ssh-agent (step 4) or let `device add` create `id_ed25519`. |
| `warning: ... pluto is not installed on the host` | The host has no `pluto` on the non-interactive PATH and no `~/.local/bin/pluto`. Install it at one of those paths and rerun `device add`. |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | The host key changed. Confirm it is your host (rebuild, reimage), then `ssh-keygen -R neptuno.local` and connect again. |
| `unknown device "neptuno"` | The nickname is not saved. Run `pluto.exe device ls`, then `device add`. |
| `pluto: host commands need --device` | This is the Windows client; it has no local daemon. Put `--device <nickname>` before the command. |
| `pluto: ssh: ...` after a network drop | The connection was lost. A new command works once the network returns; a dropped session is not resumed. |

## Where the state lives

- Saved devices: `%AppData%\pluto\devices.toml`, nicknames, ssh destinations,
  and each host's pluto path. No secrets.
- SSH keys: `%UserProfile%\.ssh\`, shared with plain `ssh`.
- Host keys: `%UserProfile%\.ssh\known_hosts`, shared with plain `ssh`.
- Everything else (boxes, worktrees, jobs, runs) lives on the Linux host.
