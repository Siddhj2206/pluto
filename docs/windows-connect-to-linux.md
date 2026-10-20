# Connect Windows to a Linux Pluto host

A step-by-step walkthrough from a fresh Windows 11 x64 laptop to a saved device
you can drive over SSH. It covers the client side only; the Linux host must
already run the Pluto daemon (see [remote-access.md](remote-access.md) for the
host side and the SSH recipes).

For the full client reference, see [windows-client.md](windows-client.md). For
the supported workflow end to end, see
[windows-client-acceptance.md](windows-client-acceptance.md).

## 1. Check the Linux host

You need the Linux machine's address (LAN name or IP, or a tailnet name) and a
user account on it. Before touching Windows, confirm on the Linux host:

```sh
pluto version                 # the host CLI is installed
systemctl --user is-active pluto   # the daemon service is running
ssh localhost pluto version   # pluto is on the PATH a non-interactive ssh sees
```

If the last command fails but `pluto version` works in an interactive shell,
the host's login PATH for ssh is the problem: link or install the binary where
the ssh session can find it, for example `/usr/local/bin/pluto`. The client
runs `pluto` by name on the host, so it must resolve there.

The host also needs a base image imported (`pluto image import images/out`) if
you intend to create boxes.

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
OpenSSH new enough for every option the client uses.

## 3. Authorize your Windows key on the Linux host

If you do not already have a key:

```powershell
ssh-keygen -t ed25519
```

Press Enter for the default path (`$env:USERPROFILE\.ssh\id_ed25519`) and set a
passphrase if you want one. Then copy the public key to the host:

```powershell
type $env:USERPROFILE\.ssh\id_ed25519.pub | ssh siddhant@neptuno "mkdir -p ~/.ssh && chmod 700 ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
```

Replace `siddhant@neptuno` with your account and host. The first connection
asks whether to trust the host key. Accept it: this is the first-use acceptance
the client relies on, and a later host-key change is still rejected.

Test passwordless login and the host CLI in one command:

```powershell
ssh siddhant@neptuno pluto version
```

Expected: the host's version, printed without a password prompt. If this fails,
stop here; the Pluto client will not get further.

## 4. Get `pluto.exe`

Pick one of these.

**From a release.** Download `pluto.exe` from the GitHub Releases page for the
demo tag and put it in a directory on your `PATH`, for example
`$env:USERPROFILE\bin`. The demo branch has no release tagged yet; see
[releasing.md](releasing.md) for how a maintainer cuts one.

**Build from the demo branch.** With Go installed on Windows:

```powershell
git clone https://github.com/Siddhj2206/pluto.git
cd pluto
git checkout demo/windows-client
go build -o $env:USERPROFILE\bin\pluto.exe .\cmd\pluto
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

## 5. Save the device

This is the step that connects the two machines. The nickname is yours; the
destination is the same `user@host` you just tested.

```powershell
pluto.exe device add neptuno siddhant@neptuno
```

Expected:

```text
saved device neptuno (siddhant@neptuno)
```

`device add` writes the nickname to `%AppData%\pluto\devices.toml` and then
probes the host with `ssh siddhant@neptuno pluto version`. A host that does not
answer produces a warning, not a failure:

```text
warning: siddhant@neptuno did not answer: ...
next: ...
```

The nickname is saved either way, so you can fix connectivity and retry
without adding it again. If you see the warning, run the probe by hand to see
ssh's actual complaint:

```powershell
ssh siddhant@neptuno pluto version
```

You can skip the registry entirely for a one-off command by passing a raw
target: `pluto.exe --device siddhant@neptuno ls`.

List what is saved, and remove a nickname when you are done:

```powershell
pluto.exe device ls
pluto.exe device rm neptuno
```

## 6. Run commands on the host

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

## 7. Choose the route

LAN is the baseline: use the host's LAN name or address in `device add`.

For a separate network, install Tailscale on both machines, sign in, and use
the host's MagicDNS name or its `100.x.y.z` tailnet address:

```powershell
tailscale up
pluto.exe device add neptuno siddhant@neptuno
```

Tailnet ACLs should grant only your Windows device access to the host on TCP
22. Pluto does not manage the tailnet and adds no relay.

## 8. Host keys

The first connection records the host's key in
`%UserProfile%\.ssh\known_hosts`. A changed key is an error, by design.

```powershell
ssh-keygen -F neptuno                    # is this host known, and with which key?
ssh-keygen -R neptuno                    # remove the entry after a legitimate rebuild
ssh-keyscan neptuno                      # compare the host's current key
```

Only accept a first-use key over an owner-controlled LAN or the authenticated
tailnet. Never route first use over a plain public path.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `exec: "ssh": executable file not found in %PATH%` | The OpenSSH client is missing. Do step 2. |
| `Permission denied (publickey)` | The key is not authorized on the host. Retest `ssh user@host pluto version`; check `~/.ssh/authorized_keys` and permissions (`700` on `.ssh`, `600` on the file). |
| `warning: ... did not answer: ... pluto: command not found` | The host's `pluto` is not on the non-interactive PATH. Link or install it under `/usr/local/bin`. |
| `REMOTE HOST IDENTIFICATION HAS CHANGED` | The host key changed. Confirm it is your host (rebuild, reimage), then `ssh-keygen -R neptuno` and connect again. |
| `unknown device "neptuno"` | The nickname is not saved. Run `pluto.exe device ls`, then `device add`. |
| `pluto: host commands need --device` | This is the Windows client; it has no local daemon. Put `--device <nickname>` before the command. |
| `pluto: ssh: ...` after a network drop | The connection was lost. A new command works once the network returns; a dropped session is not resumed. |

## Where the state lives

- Saved devices: `%AppData%\pluto\devices.toml`, nicknames only, no secrets.
- Host keys: `%UserProfile%\.ssh\known_hosts`, shared with plain `ssh`.
- Everything else (boxes, worktrees, jobs, runs) lives on the Linux host.
