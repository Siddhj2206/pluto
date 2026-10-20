# Windows client: drive a Linux host from Windows 11

The Windows client is a native `pluto.exe` that drives a Linux host over SSH.
It has no daemon and hosts no boxes: it saves an SSH destination, then runs
the host's pluto CLI for you, preserving arguments and streaming output and
exit codes. The host stays headless; see [remote-access.md](remote-access.md)
for the Linux side and ADR 0006 for the SSH access boundary.

## Prerequisites

- Windows 11 x64.
- The Windows OpenSSH client. It ships with Windows 11; check with
  `ssh -V` in PowerShell. If it is missing, add the "OpenSSH Client" optional
  feature.
- Tailscale (optional) if the host and client are not on the same network.
- An SSH key authorized on the host. pluto stores no secrets; SSH keys are the
  authentication.

The Linux host must be ready before the client connects:

- the pluto daemon is running (`pluto install` for the systemd user service,
  or `pluto daemon` in the foreground);
- the base image is imported (`pluto image import images/out`);
- the `pluto` executable is on the `PATH` that ssh sees on the host, which is
  not the same as an interactive shell's `PATH`. The check is:

  ```sh
  ssh user@host pluto version
  ```

- passwordless key auth into the host. The client also accepts a host key on
  first use (below), so the first `device add` probe just needs a reachable
  host.

## Install

Download `pluto.exe` from the
[Releases](https://github.com/Siddhj2206/pluto/releases) page and put it in a
directory on your `PATH`. `checksums.txt` sits next to it. There is no
installer, and Windows ARM and 32-bit are not built.

```powershell
.\pluto.exe version
```

## Configure

Saved devices live in a client-side registry:

```text
%AppData%\pluto\devices.toml
```

It maps nicknames to SSH destinations and holds no secrets. `device` is a
local command: it never needs `--device`.

```powershell
.\pluto.exe device add neptuno siddhant@neptuno
.\pluto.exe device ls
.\pluto.exe device rm neptuno
```

`device add` saves the nickname and then probes the host with
`ssh <target> pluto version`. A host that does not answer is a warning, not a
failure: it may simply be offline. A raw `user@host` target works without
saving, too: `pluto.exe --device siddhant@neptuno ls`.

## Reach the host

### LAN

The host and client are on the same network. Use the host's LAN address or its
mDNS/DNS name:

```powershell
ssh siddhant@neptuno
.\pluto.exe device add neptuno siddhant@neptuno
```

### Tailscale

Install Tailscale on both machines, sign in on both, and use the host's
MagicDNS name or its `100.x.y.z` tailnet address:

```powershell
tailscale up
.\pluto.exe device add neptuno siddhant@neptuno
```

Tailscale ACLs can restrict who reaches the host's port 22. pluto does not
manage the tailnet and ships no relay; it is transport for the SSH connection
you already have.

## Create a box from a repository URL

Creating a box starts on Windows with only a repository URL. The client sends
the command to the host; the host clones the repository into its own worktree
under the daemon state directory and provisions the box from that clone. The
host reads and evaluates the project's `.pluto.toml` in the clone, so contract
evaluation stays on the Linux side. No project folder is uploaded from
Windows, and there is no synchronization step: the Windows machine does not
need a local checkout.

Git runs on the host with the host's configured credential helper or SSH
agent. The URL must not embed credentials.

```powershell
.\pluto.exe device add neptuno siddhant@neptuno
.\pluto.exe --device neptuno up --repo https://github.com/you/app.git
.\pluto.exe --device neptuno ls
.\pluto.exe --device neptuno status <box>
.\pluto.exe --device neptuno pause <box>
.\pluto.exe --device neptuno up --repo https://github.com/you/app.git
.\pluto.exe --device neptuno destroy <box> --yes
```

The first `up --repo` clones the repository, creates the box, and prints
`created box ...`; a later one with the same URL selects the same host
worktree, prints `box ... already exists`, and wakes the paused box. Always
pass the URL when you wake a box from Windows: a bare `up` would use the
host's current directory.

`ls` lists the boxes with their ids. `status <box>` shows one box's state,
phases, and host worktree path. When `up` fails, `logs <box>` carries the
provisioning output. `destroy <box> --yes` removes the box record, its disk,
and its job history on the host.

## Run jobs and inspect durable work

Declared jobs come from the box's repository contract. The host worktree holds
the `.pluto.toml`, and job names resolve there when the command runs. The
client reads no contract itself; it replays the command on the host.

```powershell
.\pluto.exe --device neptuno job ls
.\pluto.exe --device neptuno job ls <box>
.\pluto.exe --device neptuno run <box> test
.\pluto.exe --device neptuno run <box> -- pnpm test
```

`job ls` lists the jobs declared in the host worktree the command runs in: the
ssh session's directory. On its own that is the host user's home, so name the
box (or its host worktree path) to list the contract the box runs from:
`job ls <box>` resolves the box's worktree on the host. `run <box> <job>` runs
a declared job in the box and resolves the job name against the box's worktree
contract on the host. `run <box> -- <command>` runs a one-off command in the
box; the `--` keeps the command's own flags from being read as pluto's. Job
output streams back to the Windows terminal as the host produces it, and the
process exit status returns, so a failing job makes `pluto.exe` exit with the
job's status.

Durable work started by triggers is inspected with the task and run views:

```powershell
.\pluto.exe --device neptuno task ls
.\pluto.exe --device neptuno task show <task-id>
.\pluto.exe --device neptuno task logs <task-id>
.\pluto.exe --device neptuno run ls
.\pluto.exe --device neptuno run show <run-id>
.\pluto.exe --device neptuno run logs <run-id>
```

`task ls` lists tasks with their states, `task show` prints one task and its
runs, and `task logs` prints a run's recorded output. `run ls`, `run show`,
and `run logs` are the same views from the run side. Task and run ids accept
unambiguous prefixes, and the state lives on the host, so every read goes to
the daemon that owns the box.

## Run host commands

Every host command needs `--device` and is replayed on the host exactly as
given, so box ids and worktree paths resolve there:

```powershell
.\pluto.exe --device neptuno up --repo https://github.com/you/app.git
.\pluto.exe --device neptuno ls
.\pluto.exe --device neptuno status <box>
.\pluto.exe --device neptuno logs <box>
.\pluto.exe --device neptuno run <box> test
.\pluto.exe --device neptuno run <box> -- pnpm test
.\pluto.exe --device neptuno task ls
.\pluto.exe --device neptuno run ls
.\pluto.exe --device neptuno attach <box>
.\pluto.exe --device neptuno pause <box>
.\pluto.exe --device neptuno destroy <box> --yes
```

`up --repo` follows the provisioning flow above. Job output streams back and
the process exit status returns to Windows.

A host command without `--device` is a usage error naming `pluto device add`:
this client has no local daemon.

## Attach to a box

`attach` opens the selected box's default shell in the Windows terminal:

```powershell
.\pluto.exe --device neptuno attach <box>
```

The command is replayed on the host, which opens an ssh session to the box's
sshd and runs the box user's default shell. Typing goes to that shell and its
output returns to the Windows terminal as it is produced. Exiting the shell
ends the connection and returns control to the Windows prompt with the shell's
exit status. Nothing has to be configured on Windows, and the shell is not
persistent: it lives for the connection and ends with it.

When stdin is a Windows console, the client asks ssh for a remote terminal
(`ssh -t`), so the host and the box both see a terminal. A redirected or piped
stdin does not request one.

To enter a session declared in the project's `.pluto.toml`:

```powershell
.\pluto.exe --device neptuno attach <box> --session agent
```

`--session` enters the declared tmux session; it does not create one. A plain
attach needs no declared session, so the demo does not depend on persistent
session setup. If the name is not declared, tmux reports that the session does
not exist.

Terminal behavior after the connection is up, including window resize, Ctrl+C,
and raw-mode input, depends on the Windows terminal and the Windows OpenSSH
client. The Windows CI job drives `pluto.exe` against a fake ssh and checks the
arguments, both directions of the stream, and the returned exit status. The
terminal behavior itself is part of the Windows-to-Linux acceptance run.

## Host key policy

The client accepts a host key on first use only. The first connection to a
host records its key in `%UserProfile%\.ssh\known_hosts`; a later change is
rejected, the same as a plain `ssh` would reject it. The client never passes
`StrictHostKeyChecking=no` and never uses a throw-away `known_hosts` file.

The option applies on every route, not only the tailnet: the client cannot
tell a tailnet address from a LAN one, and the LAN baseline needs first-use
acceptance too. Keeping the address out of any plain public path is the
operator's job; a changed key is rejected everywhere.

To inspect or clear a saved key:

```powershell
Get-Content $env:UserProfile\.ssh\known_hosts
ssh-keygen -R neptuno
```

## Recovery

- If the network drops, the running command fails. A new command works once
  reachability returns; there is no retry queue.
- A dropped SSH or `attach` session is not resumed. Run the command again, or
  attach again, to get a fresh session. A box keeps its disk across a pause and
  wake, so work already on the box is not lost.

## Where next

- [windows-client-acceptance.md](windows-client-acceptance.md): the manual
  Windows 11 to Linux acceptance procedure and its record.
- [remote-access.md](remote-access.md): the SSH recipes, `ssh -L` into an
  in-box web UI, and the Linux-side details.
- [contract.md](contract.md): the `.pluto.toml` a provisioned repository
  declares.
