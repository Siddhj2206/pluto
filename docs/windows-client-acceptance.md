# Windows acceptance: drive a Linux host from Windows 11

This is the required acceptance run for the Windows client (#157). It is a
manual procedure: a person sits at a Windows 11 x64 laptop, drives a Linux
host over SSH, and records what they saw. It is the only check that exercises
real OpenSSH, a real tailnet, and a real Windows console against a real Linux
host.

> The review-3 demo keeps the client-to-server leg on a LAN or phone hotspot
> and does not require Tailscale. The server itself has internet ingress for
> GitHub's webhook. This acceptance record still covers the original
> cross-network run; it stays unfilled until that run actually happens. See
> [r3-plan.md](r3-plan.md).

Windows CI and the Wine rehearsal are not substitutes. The `windows-client`
CI job drives `pluto.exe` against a fake `ssh.exe`, and
`scripts/windows-client-wine-smoke.sh` runs the same executable under Wine on
Linux. Both prove argument handling, stream plumbing, exit codes, and config
paths in isolation. Neither has real OpenSSH, real Tailscale, a real Linux
daemon, or a real terminal. See [testing.md](testing.md) for what those runs
cover.

> **Status: unfilled.** No Windows 11 x64 machine was available to the
> implementer, so this procedure has not been run. The record at the end is
> blank. #162 stays open until a Windows operator runs it and fills the record
> against a stated commit. A Linux-only rehearsal does not close it.

## What the run proves

- The full client workflow works over Tailscale from a separate network,
  with LAN as the baseline.
- The client provisions from a repository URL, inspects and manages the box,
  runs a declared job with streamed output and a returned exit status,
  inspects durable tasks and runs, attaches interactively, and pauses.
- First-use host-key acceptance happens only over the authenticated tailnet
  or the owner-controlled LAN, and a later host-key change is rejected.
- After a network interruption, a new CLI operation succeeds; a dropped SSH
  or `attach` session is not resumed.
- The run is recorded against a Windows 11 x64 client and a Linux host.

## Host-owner prerequisites (before the boundary)

Do these before the Windows client connects. After this, the host needs no
Linux-side interaction for the rest of the run.

1. Start the daemon and confirm it is up.

   ```sh
   pluto install                    # systemd user service, enabled with linger
   systemctl --user is-active pluto # prints active
   pluto version
   ```

   `pluto daemon` in the foreground is the alternative where systemd is not in
   use. See [testing.md](testing.md) for the host requirements this needs.

2. Import the base image and confirm it is listed.

   ```sh
   pluto image import images/out
   pluto image ls
   ```

3. Confirm the host has `pluto` where the client looks. The client checks the
   `PATH` a non-interactive ssh session gets, then falls back to
   `~/.local/bin/pluto`:

   ```sh
   ssh <user>@<host> 'command -v pluto || echo ~/.local/bin/pluto'
   ```

   Expected an absolute path that exists. The client records that path with
   the device, so it needs no `/usr/local/bin` symlink and no interactive
   profile.

4. Confirm the account accepts password login once. `device add` uses it to
   install the Windows user's SSH key; after that every command is
   passwordless. If the host already accepts the laptop's key, the prompt is
   skipped.

   On a host with password login disabled, install the key by hand instead:
   create/reuse `%UserProfile%\.ssh\id_ed25519.pub`, append it to
   `~/.ssh/authorized_keys` on the host, and keep `~/.ssh` at 700 and
   `authorized_keys` at 600.

5. Prepare a repository URL the host can clone with its own git credentials.
   It must contain a `.pluto.toml` that declares at least one job, and it must
   not embed credentials in the URL. The host owns the clone and the box; the
   Windows laptop never uploads a folder.

6. For the cross-network part, enrol the host on Tailscale and write the ACL:

   ```sh
   tailscale up
   tailscale status
   tailscale ip -4
   ```

   Grant only the Windows device access to the host on TCP 22. The tailnet
   policy file is JSON. It needs the Windows device's `100.x.y.z`, which the
   Windows setup shows once the device joins the tailnet, and the host's from
   `tailscale ip -4`:

   ```json
   {
     "hosts": {
       "pluto-host": "100.64.0.20"
     },
     "acls": [
       {
         "action": "accept",
         "src": ["100.64.0.10"],
         "dst": ["pluto-host:22"]
       }
     ]
   }
   ```

   `src` is the Windows device's tailnet address, and `pluto-host` is a name
   for the host's address defined in the `hosts` section. A new tailnet starts
   with a default allow-all policy. This snippet replaces it, so merge it with
   an existing policy if the tailnet has other devices. The rule above permits
   one source address to one destination on one port, which is the narrowest
   grant the demo needs. Enable MagicDNS if the run will use a host name;
   otherwise use the `100.x.y.z` address directly.

## Windows client setup

1. Check the in-box OpenSSH client.

   ```powershell
   ssh -V
   ```

   Expected `OpenSSH_for_Windows_x.y`, at least 7.6, which is what
   `StrictHostKeyChecking` and `--` before the hostname require. If
   it is missing, run an Administrator PowerShell:

   ```powershell
   Get-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0
   Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0
   ```

   The same feature is under Settings, System, Optional features, Add a
   feature, OpenSSH Client. Open a new terminal afterwards.

2. No key setup is needed. `device add` reuses an existing default key or
   generates `%UserProfile%\.ssh\id_ed25519` and installs its public half with
   one password prompt. To pre-create a key for a host with password login
   disabled:

   ```powershell
   ssh-keygen -t ed25519 -f "$env:USERPROFILE\.ssh\id_ed25519"
   Get-Content "$env:USERPROFILE\.ssh\id_ed25519.pub"
   ```

3. Download the release artifact. It is already named `pluto.exe`.

   ```powershell
   $tag = (Invoke-RestMethod https://api.github.com/repos/Siddhj2206/pluto/releases/latest).tag_name
   Invoke-WebRequest "https://github.com/Siddhj2206/pluto/releases/download/$tag/pluto.exe" -OutFile pluto.exe
   Invoke-WebRequest "https://github.com/Siddhj2206/pluto/releases/download/$tag/checksums.txt" -OutFile checksums.txt
   (Get-FileHash .\pluto.exe -Algorithm SHA256).Hash
   Select-String -Path .\checksums.txt -Pattern 'pluto.exe'
   ```

   The checksum is of the artifact bytes. Put `pluto.exe` on the user `PATH`
   and open a new terminal:

   ```powershell
   $dir = "$env:LOCALAPPDATA\Programs\pluto"
   New-Item -ItemType Directory -Force $dir | Out-Null
   Move-Item -Force .\pluto.exe "$dir\pluto.exe"
   $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
   if ($userPath -notlike "*$dir*") { [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User') }
   pluto.exe version
   ```

   Expected `pluto <version>`. Record it in the acceptance record.

4. For the cross-network step, install Tailscale from
   <https://tailscale.com/download/windows>, sign in from the tray icon, then
   record the Windows tailnet address for the ACL:

   ```powershell
   tailscale up
   tailscale status
   tailscale ip -4
   ```

   The tailnet is reachability only. SSH key auth and host-key checking stay
   in force, and Pluto neither manages the tailnet nor ships a relay.

## LAN baseline

Run the Windows and Linux machines on the same network. Set the host's LAN
address (`ip -4 addr` or `hostname -I` on the host) and user for the session:

```powershell
$hostUser = 'siddhant'
$hostLan  = '192.168.1.20'
```

1. Register the host. This first connection records the host key.

   ```powershell
   pluto.exe device add neptuno-lan "$hostUser@$hostLan" --trust accept-new
   ```

   Expected stdout `saved device neptuno-lan (siddhant@192.168.1.20)` followed
   by `connected: pluto at <path>`, and exit 0, with no `warning:` on stderr.
   On a first run the account password is requested once to install the key;
   the transcript records that prompt and the `installing an ssh key` line. A
   warning means the probe failed (wrong address, password, or missing host
   binary); the device is still saved, so remove it and add it again for a
   clean record.

2. List the registry.

   ```powershell
   pluto.exe device ls
   ```

   Expected a `NICKNAME  DESTINATION` header and one row for `neptuno-lan`.

3. Round-trip a host command.

   ```powershell
   pluto.exe --device neptuno-lan version
   pluto.exe --device neptuno-lan ls
   ```

   `--device` takes precedence over the client's local commands, as it does on
   Linux: both `version` and `ls` are replayed on the host. `version` prints
   the host's `pluto <version>`; `pluto.exe version` without `--device` prints
   the Windows client's version.

   ```powershell
   pluto.exe version
   ```

   `ls` is replayed on the host and prints the host's boxes.

4. Choose a box for the argv and exit checks. Take an id from `ls`, or create
   one from the repository URL if the host has none, then destroy it when the
   baseline is done so the cross-network run provisions fresh.

   ```powershell
   pluto.exe --device neptuno-lan up --repo https://github.com/you/app.git
   $box = '<box id or prefix>'     # from the `created box` line, or from ls
   ```

5. Check argument preservation and streamed output. The `sleep` proves output
   arrives as it is produced rather than buffered until exit.

   ```powershell
   pluto.exe --device neptuno-lan run $box -- sh -c 'echo first; sleep 2; echo second'
   pluto.exe --device neptuno-lan run $box -- sh -c 'echo out; echo err >&2'
   ```

   Expected `first` then `second` two seconds apart on stdout; `out` on stdout
   and `err` on stderr; both exit 0. The `--` ends pluto's own flags; quote it
   as `'--'` if your shell version drops a bare one.

6. Check exit-status propagation.

   ```powershell
   pluto.exe --device neptuno-lan run $box -- sh -c 'exit 7'
   $LASTEXITCODE
   ```

   Expected `7`. `$LASTEXITCODE` is the native process exit status.

7. Clean up a baseline box you created.

   ```powershell
   pluto.exe --device neptuno-lan destroy $box --yes
   ```

   Expected `destroyed box <id> (...)`.

## Full workflow over Tailscale

Run this with Windows and the Linux host on different networks, so the only
route is the tailnet. Use the host's MagicDNS name or its `100.x.y.z` address.

1. Confirm reachability before SSH.

   ```powershell
   tailscale status
   tailscale ping <host-tailnet-name-or-100.x.y.z>
   ```

   Record whether the path is direct or relayed.

2. Register the tailnet destination under its own nickname, so its known_hosts
   entry is created over the tailnet.

   ```powershell
   $hostTs = 'neptuno'             # MagicDNS name, or 100.x.y.z
   pluto.exe device add neptuno-ts "$hostUser@$hostTs" --trust accept-new
   pluto.exe device ls
   ```

   Expected `saved device neptuno-ts (...)` with no warning. This is the
   first-use host-key acceptance the run is recording.

3. Round-trip a host command.

   ```powershell
   pluto.exe --device neptuno-ts ls
   ```

4. Provision from a repository URL. The host clones it into its own worktree
   under the daemon state directory and provisions the box there.

   ```powershell
   pluto.exe --device neptuno-ts up --repo https://github.com/you/app.git
   $box = '<box id or prefix>'     # from the `created box` line
   ```

   Expected `created box <id> for <project>/<branch>`, then `box <id> running`
   with a `phases:` line. Record the box id. On failure, `logs $box` carries
   the provisioning output.

5. Inspect the box.

   ```powershell
   pluto.exe --device neptuno-ts ls
   pluto.exe --device neptuno-ts status $box
   pluto.exe --device neptuno-ts logs $box
   ```

   `status` prints `worktree: <host path>`; record it for the wake step.

6. Exercise the lifecycle: pause, see the state, then wake.

   ```powershell
   pluto.exe --device neptuno-ts pause $box        # box <id> paused
   pluto.exe --device neptuno-ts ls                # state paused
   pluto.exe --device neptuno-ts up --repo https://github.com/you/app.git
   ```

   Expected `box <id> already exists for <project>/<branch>` and then
   `box <id> running`. Wake by the host worktree path instead if you like,
   using the path from `status`:

   ```powershell
   pluto.exe --device neptuno-ts up --worktree '<host worktree path>'
   ```

   Always pass `--repo` or `--worktree` when waking from Windows. A bare `up`
   would use the ssh session's directory (the host user's home), not the box.

7. Run a declared job. List the box's contract first, then replace `<job>`
   with a name it declares.

   ```powershell
   pluto.exe --device neptuno-ts job ls $box
   pluto.exe --device neptuno-ts run $box <job>
   pluto.exe --device neptuno-ts run $box -- sh -c 'echo one; sleep 2; echo two'
   pluto.exe --device neptuno-ts run $box -- sh -c 'exit 7'
   $LASTEXITCODE
   ```

   Expected: `job ls $box` lists the jobs from the box's host worktree (a bare
   `job ls` would read the ssh session's directory, the host user's home), the
   job's output streams to the terminal as the host produces it, and
   `$LASTEXITCODE` is the job's exit status. The slow command proves
   streaming; the `exit 7` command proves status propagation through the
   Windows client and the tailnet.

8. Inspect durable work. Create a task so the views have content, then read it
   back. If the repository already produced runs from a trigger, use those
   instead of creating one.

   ```powershell
   pluto.exe --device neptuno-ts task create --job <job> --prompt "acceptance run" $box
   pluto.exe --device neptuno-ts task ls
   pluto.exe --device neptuno-ts task show <task-id>
   pluto.exe --device neptuno-ts task logs <task-id>
   pluto.exe --device neptuno-ts run ls
   pluto.exe --device neptuno-ts run show <run-id>
   pluto.exe --device neptuno-ts run logs <run-id>
   ```

   `task create` prints `task <id> accepted` with the first run's id and state.
   Task and run ids accept unambiguous prefixes, and the state lives on the
   host.

9. Attach interactively from a Windows terminal. Use Windows Terminal or a
   PowerShell console, not a pipe or a redirected stream, because the client
   only requests a remote terminal when stdin is a console.

   ```powershell
   pluto.exe --device neptuno-ts attach $box
   ```

   At the shell prompt run `uname -a`, then `pwd`, then `exit`. Expected: input
   reaches the box, the box's output returns, and `exit` ends the session and
   returns control to PowerShell with exit 0. The shell lives only for the
   connection. A declared session is entered, not created, with
   `attach $box --session <name>`.

10. Pause the box and record the state.

    ```powershell
    pluto.exe --device neptuno-ts pause $box
    pluto.exe --device neptuno-ts ls
    ```

    Expected `box <id> paused` and then a `paused` row. After recording that
    result, destroy the box as cleanup:

    ```powershell
    pluto.exe --device neptuno-ts destroy $box --yes
    ```

    Expected `destroyed box <id> (...)`, which removes the record, the disk,
    and the job history on the host.

## Host-key policy check

The client uses `StrictHostKeyChecking=yes` by default. The first connection
to a host records its key in `%UserProfile%\.ssh\known_hosts` only when
`device add` is given `--trust accept-new`; that choice is recorded in the
profile. A later change is rejected. The client never passes
`StrictHostKeyChecking=no` and never uses a throw-away `known_hosts`.

First use: the `device add neptuno-ts --trust accept-new` probe in the workflow
above is the first connection to the tailnet target, so it records the key.
Show the entry.
The host argument is the host part of the saved destination, `neptuno` or
`100.x.y.z`, not the nickname `neptuno-ts`.

```powershell
ssh-keygen -F <host>
ssh-keygen -F <host> -l
Get-Content "$env:USERPROFILE\.ssh\known_hosts"
```

Compare the fingerprint with the host's own key:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

A later change is rejected. The safest check tampers with the client's saved
key rather than the host's, so no host change is needed. The client sees a
known host with a different key, which is the same rejection a real key
change produces.

```powershell
Copy-Item "$env:USERPROFILE\.ssh\known_hosts" "$env:USERPROFILE\.ssh\known_hosts.acceptance-backup"
ssh-keygen -R <host>
ssh-keygen -t ed25519 -f "$env:TEMP\pluto-fake-host"    # press Enter twice for no passphrase
$parts = (Get-Content "$env:TEMP\pluto-fake-host.pub").Split(' ')
"$($parts[0]) $($parts[1]) <host>" | Add-Content "$env:USERPROFILE\.ssh\known_hosts"
pluto.exe --device neptuno-ts ls
$LASTEXITCODE
```

Expected: exit 255, and stderr contains
`WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!` and
`Host key verification failed.` Match the fake key's type to a type the host
offers (`ssh-ed25519` on a current Linux host; use `-t rsa` if the host's
saved entry is `ssh-rsa`). Restore the file and confirm the host works again:

```powershell
Move-Item -Force "$env:USERPROFILE\.ssh\known_hosts.acceptance-backup" "$env:USERPROFILE\.ssh\known_hosts"
Remove-Item "$env:TEMP\pluto-fake-host"*
pluto.exe --device neptuno-ts ls
```

A stronger alternative is a real rotation on a throwaway host: back up
`/etc/ssh`, remove `/etc/ssh/ssh_host_*`, run `sudo ssh-keygen -A`, restart
ssh, observe the same rejection, then restore the files and restart ssh again.
Record which method was used.

A host rebuild is the legitimate reason to remove a stale entry:

```powershell
ssh-keygen -R <host>
```

Then reconnect and accept the new key on first use. Never use
`StrictHostKeyChecking=no` to get past a change.

First-use acceptance is only allowed over the authenticated tailnet or an
owner-controlled LAN. Never accept a first key over a plain public path, where
an on-path attacker could present it. If the host is also reachable on a
public port, do not register that target.

## Recovery check

A network interruption ends the current connection. Pluto has no retry queue
and does not resume a session.

1. Confirm a read works.

   ```powershell
   pluto.exe --device neptuno-ts ls
   ```

2. Interrupt the Windows network. Turn off Wi-Fi or unplug Ethernet, or
   enable airplane mode (closing the terminal is not a network interruption).
   Confirm `tailscale status` shows the host offline, or `tailscale ping`
   fails.

3. Run a new operation and observe the failure.

   ```powershell
   pluto.exe --device neptuno-ts ls
   $LASTEXITCODE
   ```

   Expected: ssh's connection error after about 10 seconds (the client's
   `ConnectTimeout`), and exit 255. It fails rather than hanging.

4. Restore the network, wait for `tailscale status` to show the host, then run
   a fresh operation.

   ```powershell
   pluto.exe --device neptuno-ts ls
   ```

   Expected: exit 0. This is a new operation; nothing about the failed one
   resumed.

5. Show that an `attach` session is not resumed. Open
   `pluto.exe --device neptuno-ts attach $box` in a terminal, break the
   network the same way, and watch the session end with an ssh error as
   control returns to PowerShell. Restore the network and open a new `attach`
   or `ls`. Expected: the old session does not come back; only a new one
   works. The box keeps its disk, so work already on the box is not lost.

## Acceptance record

> **Status: unfilled.** This table has not been completed. The implementer had
> no Windows 11 x64 machine, so the acceptance has not been performed. #162
> stays open until a Windows operator fills these fields. Windows CI and the
> Wine rehearsal are not substitutes. Cite the exact branch and commit tested.

Run metadata:

| Field | Value |
| --- | --- |
| Date (with timezone) | |
| Operator | |
| Windows edition and version | |
| Windows build (`winver`, `Get-ComputerInfo \| Select WindowsProductName, WindowsVersion, OsBuildNumber`) | |
| Windows architecture (`$env:PROCESSOR_ARCHITECTURE`, expect AMD64) | |
| Windows client release tag | |
| `pluto.exe version` | |
| `pluto.exe` SHA-256 | |
| Linux host hostname and `uname -srm` | |
| Linux distribution (`cat /etc/os-release`) | |
| Host `pluto version` | |
| Host branch under test (`git rev-parse --abbrev-ref HEAD`) | |
| Host commit under test (`git rev-parse HEAD`) | |
| Windows network and LAN address | |
| Host network | |
| Tailnet name and `100.x.y.z` address | |
| `tailscale ping` result (direct or relayed) | |
| Tailnet ACL rule or revision | |

Checks:

| Check | Expected | Observed | Pass |
| --- | --- | --- | --- |
| Windows OpenSSH at least 7.6 | `ssh -V` reports 7.6+ | | |
| Host prerequisites | `ssh <user>@<host> 'command -v pluto || echo ~/.local/bin/pluto'` prints an existing absolute path | | |
| `device add` over LAN | saved and connected; `--trust accept-new` records the first host key; one password prompt on a fresh laptop | | |
| `device ls` | lists the nickname and destination | | |
| LAN host round trip | `--device neptuno-lan ls` prints host boxes | | |
| `--device <nick> version` | prints the host version; bare `pluto.exe version` prints the client version | | |
| LAN argv, streams, exit status | command args preserved, both streams, exit 7 returns | | |
| Tailnet ACL | only the Windows device reaches the host on TCP 22 | | |
| `device add` over Tailscale | saved and connected, no password prompt (key already installed) | | |
| First use records the key | `ssh-keygen -F <host>` finds the entry | | |
| Changed key rejected | exit 255 with `REMOTE HOST IDENTIFICATION HAS CHANGED` | | |
| Provision from repo URL | `created box` and running over the tailnet | | |
| Inspect box | `ls`, `status`, `logs` return host state | | |
| Lifecycle | pause then wake by repo URL or host worktree | | |
| Declared job | output streams and exit status returns | | |
| Durable inspection | `task ls/show/logs` and `run ls/show/logs` return results | | |
| Interactive attach | shell input and output work from a Windows terminal | | |
| Recovery | new operation succeeds after reachability returns | | |
| Attach not resumed | dropped session does not reconnect | | |
| Final pause | box state is paused | | |
| Destroy cleanup | `destroyed box <id>` after the pause was recorded | | |

Keep the transcripts, `tailscale status` output, `ssh-keygen -F` output, the
box id, and the release tag with the record.

## Where next

- [windows-client.md](windows-client.md): the client guide and the commands
  this run drives.
- [testing.md](testing.md): the Windows CI job and the Wine rehearsal, and
  what each of them does not cover.
- [remote-access.md](remote-access.md): the SSH recipes and the Linux-side
  details.
