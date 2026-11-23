# Tasks in `.pluto`

A task is a list of shell commands that Pluto runs in a box on your machine. You define tasks in a repo's `.pluto` directory, run them with `pluto run`, and optionally schedule them.

## Files

- `.pluto/pluto.yml` holds the repo's box settings and, optionally, tasks.
- `.pluto/tasks/<name>.yml` holds one task. The file name is the task name.

Only these two places are read. Other files in `.pluto`, and subdirectories of `tasks/`, are ignored. A `pluto.yaml` or `tasks/<name>.yaml` is an error, so a misnamed file can't be silently skipped.

A step containing `: ` must be quoted or written as a block (`- |`), because YAML would otherwise read it as a key.

```yaml
# .pluto/pluto.yml
image: registry.example.com/team/buildstream:2   # Optional. Defaults to the GitHub Actions runner image.
requires:                                         # Optional. The only value so far is privileged.
  - privileged
tasks:
  test:
    run:
      - make test
```

Task names use letters, digits, `-` and `_`, and must start with a letter or digit. A task can be defined in `pluto.yml` or in `tasks/`, but not both. Unknown keys are errors, so a typo fails instead of being ignored.

## Task keys

| Key | Meaning |
| --- | --- |
| `run` | Required. A list of steps. Each step is a shell script. |
| `schedule` | Optional. A five-field cron expression, such as `0 3 * * *`. See Schedules. |
| `push` | Optional. `true` runs the task when a commit is pushed to a branch of the repo. It can sit beside `schedule`. See Running on push. |
| `timeout` | Optional. How long a run may take, as a Go duration such as `90m`, `6h` or `1h30m`. Defaults to 12 hours. |
| `secrets` | Optional. Names of the repo's secrets to pass to this task, such as `[API_TOKEN]`. |

Set `image` once per repo in `pluto.yml`. Tasks can't set their own image.

## The box

Each repo gets one durable box. It is created the first time the repo runs a task, and it is kept between runs.

- `/workspace` and `/root` are kept between runs, and between image changes. If you change `image` or `requires`, Pluto replaces the container and keeps those directories. `pluto box reset` deletes them.
- A box stops when its run ends. A box you opened with `pluto box enter` or `pluto enter` stops within about 10 minutes once nothing you started in it is still running, and its files stay. The host also removes a box container it has no record of, keeping its volumes.
- Paths that the image declares with `VOLUME` are kept the same way, except those under `/workspace` or `/root`, which are already kept. Pluto mounts a volume at each one when it creates the box. They survive a new image like `/root` does, count in `pluto box list`, and `pluto box reset` deletes them. If a new image stops declaring a path, its volume stays, unmounted, until a reset, so changing the image back loses nothing. This is how an image such as `quay.io/podman/stable`, which keeps its image store in `/var/lib/containers`, keeps that store between runs.
- A repo's box runs one thing at a time. A task that comes in while the box is busy waits in the queue.
- The host runs one thing at a time in all, CI jobs included. When a slot frees up, work someone is waiting on (CI jobs, `pluto run`, `pluto schedule run`) goes before scheduled runs. Within each group, the oldest goes first. A scheduled run also holds back while GitHub has a CI job queued for this host, unless that repo's CI has a problem starting boxes (`pluto ci list` shows it). After 30 minutes in the queue it stops holding back and counts as waited on, so a busy repo can't keep an overnight build from running.
- A queued run says why it's waiting, for example which run holds its box or the slot, and names the command that shows more, such as `pluto logs` for the run in the way. `pluto history` and `pluto task list` show this in the note column, `pluto run` prints it while it waits, and `pluto ci list` says when CI jobs are waiting for a slot.
- Pluto pulls the image when it creates the box. If the pull fails and an older copy is on the machine, it uses that copy.
- The image must contain `sh` and `sleep`, and the other commands Pluto's scripts call: `tail`, `head`, `cat`, `mv`, `rm` and `kill`, plus `xargs` when files are deleted from the repo. Without `date`, step times show as `--:--:--` and durations as `?`. The script uses `bash` when the image has it and `sh` otherwise.

## What a run gets

- The repo's files are copied into `/workspace`. `.git` is not copied, and neither are files that git ignores.
- A manual run (`pluto run`) copies your working tree, including uncommitted changes and new files that aren't ignored. A scheduled run uses the committed branch. A run started by a push uses the pushed commit.
- Files you deleted from the repo are deleted from `/workspace` on the next run. Ignored files stay where they are.
- Each run gets these environment variables: `PLUTO_RUN` (the run ID), `PLUTO_TASK` (the task name), `PLUTO_REF` (the branch, or `working tree` for a manual run) and `PLUTO_SHA` (the commit, empty for a manual run). Listed secrets are set too, except on a push to a branch other than the default one (see Running on push).
- `pluto exec <command>` runs a one-off command in the same box. One quoted argument is shell, so `pluto exec 'make && make test'` works. Several arguments keep their boundaries.
- Steps run in order from `/workspace`. The log shows each step's commands, prefixed with the time and `$` (`[14:03:07] $ bst build ...`), and after each step a line with how long it took, or its exit code if it failed (`[14:05:12] took 2m05s`, `[14:05:12] failed with code 2 after 2m05s`). The run stops at the first step that fails.
- The timeout counts from when the run starts. Time the machine spends asleep doesn't count. A log stops growing at 256 MiB.

## Schedules

1. Commit a task with a `schedule:` key to a branch of the repo.
2. In the repo, run `pluto schedule enable`. It follows the repo's default branch. Use `pluto schedule enable --branch <name>` for another branch. The repo needs an `origin` remote.
3. Check with `pluto schedule list`, or `pluto task list` in the repo.
4. To run a scheduled task now, exactly as the schedule would, from the committed branch: `pluto schedule run <task>`. `pluto run <task>` uses your working tree instead. Turn schedules off with `pluto schedule disable`. The box and its runs stay.

Rules:

- Times are the machine's local time. A `TZ=` prefix is an error.
- If the machine was asleep or the host was stopped, all the slots that passed collapse into one run. The run's note says how many slots it covers.
- If a slot comes while the same task is still running or queued, Pluto records a `skipped` run and doesn't queue another.
- Pluto checks the branch about every 15 minutes, so a change to the branch takes up to that long to apply. A new or changed schedule starts from the time Pluto notices it, so it doesn't run for slots that passed before.
- If the branch can't be fetched, Pluto runs the last commit it fetched and says so in the run's note. If the new config is broken, the last good config keeps running, and `pluto schedule list` shows the problem.

## Running on push

A task with `push: true` runs in the repo's box whenever a commit is pushed to one of the repo's branches, and its result appears on the commit in GitHub.

1. Commit a task with `push: true` to a repo you connected with `pluto ci connect github`. There is nothing else to turn on.
2. Push. Within about 20 seconds Pluto sees the new commit and queues the task.
3. The result shows on the commit as a status named `pluto/<task>`: pending while the run waits or goes, then success, failure or error.

```yaml
# .pluto/tasks/check.yml
run:
  - make test
push: true
```

What runs:

- The task runs from the pushed commit, and its config is read from that commit too. A branch can add or change a task and see it run before it merges.
- Each task with `push: true` gets its own run, with the commit's author as the run's actor. `pluto history` shows the branch and the actor.
- If the commit's `.pluto` can't be read, Pluto records one failed run named `config` with the error, and reports it as `pluto/config`.
- A commit on a repo with no `push: true` task starts nothing.
- A newer push to the same branch cancels a run of the same task that is still queued. A run that has started finishes.
- The first time Pluto looks at a repo it only records where the branches are. After the host has been off, each branch that moved gets one run, for its newest commit.

Rules:

- Only branches of the repo run. A fork's pull request is not a branch of the repo, so it starts nothing.
- A branch whose commit is authored by a bot (a name ending in `[bot]`, such as `dependabot[bot]`) is refused and shown as `refused` in `pluto history`, unless it is the default branch. Nothing is put on the commit, so the bot's pull request isn't marked as failing. The box is shared with the default branch's runs, and a bot's change hasn't been reviewed.
- A push to any branch but the default one runs without the task's secrets, and the run's note says so. Pushes to the default branch, manual runs and scheduled runs get them.
- The box is shared between branches, so a branch can leave files that a later run sees. A secret a run writes to a file stays there for the next run too, whatever its branch. Use it for checks. Anything that publishes, signs or deploys belongs in a CI job, which gets a fresh box. `pluto box reset` clears the box.

Token permissions: the token you gave `pluto ci connect github` needs `Contents: read` to let Pluto read each pushed commit (even for CI only, on a private repo), and `Commit statuses: write` to put results on commits. Without `Contents: read`, `pluto ci list` shows the problem and CI jobs still run. Without `Commit statuses: write`, runs still happen, and each run's note says what the token needs.

## Staying awake

On Linux with systemd, Pluto asks the machine not to sleep while a run is active or queued, or while a scheduled slot is less than five minutes away. It does this only on mains power. On battery, the machine sleeps as usual. While the request is held, closing the lid doesn't sleep the machine.

This is a request, not a guarantee. If the machine sleeps anyway, Pluto records the gap in the affected runs' notes.

## Secrets

```sh
pluto secret set API_TOKEN     # Prompts for the value, or reads it from a pipe.
pluto secret list              # Lists names only.
pluto secret delete API_TOKEN
```

Secrets are stored in the host's database, unencrypted. The file is readable only by your user. A secret reaches the run as an environment variable. If a task prints a secret, the secret appears in the log.

## Privileged boxes

A task that needs to run containers or sandboxes, as BuildStream does, can ask for `requires: [privileged]`. Pluto refuses that unless two things are true: the machine's engine is rootless, and you have allowed it with `pluto box grant --privileged`. Code in the repo then acts with your user's rights on that machine. `pluto box revoke --privileged` takes the grant back. The box's container is removed within seconds, or as soon as no shell is open in it, and its next run gets an unprivileged one. Its directories stay.

## Example: a BuildStream build

```yaml
# .pluto/pluto.yml
image: registry.example.com/team/buildstream:2   # An image with bst installed.
requires:
  - privileged
tasks:
  build:
    run:
      - bst build hello.bst
    timeout: 6h
```

```yaml
# .pluto/tasks/nightly.yml
run:
  - bst build --all
schedule: "0 2 * * *"
timeout: 12h
```

Run the build with `pluto run build`. Commit the nightly task, then run `pluto schedule enable` once. BuildStream's cache lives under `/root`, so it is kept between runs in the same box.

## Example: podman inside a privileged box

An image such as `quay.io/podman/stable` defaults to fuse-overlayfs, and in testing a single layer commit took over 14 minutes. Write a `storage.conf` that uses the kernel's overlay driver and keeps the image store under `/root`, which is kept between runs. Do it in the first step, or bake the file into the image.

```yaml
# .pluto/pluto.yml
image: quay.io/podman/stable:latest
requires:
  - privileged
tasks:
  build:
    timeout: 3h
    run:
      - |
        mkdir -p /root/containers/storage
        cat > /etc/containers/storage.conf <<'CONF'
        [storage]
        driver = "overlay"
        runroot = "/run/containers/storage"
        graphroot = "/root/containers/storage"
        [storage.options.overlay]
        mountopt = "nodev"
        CONF
        podman info --format 'driver={{.Store.GraphDriverName}} root={{.Store.GraphRoot}}'
      - podman build -t localhost/app .
```

There is no `mount_program` line, so podman mounts layers with the kernel instead of fuse-overlayfs. The `podman info` line prints the driver and root that podman ended up with, so the log shows whether the file took effect.

## Commands

```
pluto run <task> [--detach]          Run a task from .pluto, using your working tree
pluto exec [--detach] <command...>   Run a one-off command in this repo's box
pluto history [--since 24h] [--json] List runs, newest first
pluto logs <run> [--follow]          Show a run's log
pluto cancel <run>                   Stop a task's run
pluto enter [<run>]                  Open a shell in a run's box, or this repo's box

pluto task list                      Tasks in .pluto, with schedules, pushes and last results
pluto schedule list|enable|disable   Run tasks on a schedule, from a committed branch
pluto schedule run <task>            Run a task now from the committed branch
pluto box list|reset                 This repo's durable box
pluto box grant|revoke --privileged  Let this repo's box run privileged
pluto secret set|list|delete         This repo's secrets on this host
pluto ci connect github | ci list    Run GitHub Actions jobs here
pluto host start|stop|status         The background service
pluto doctor | version
```

Every list takes `--json`. `ls` and `rm` work as aliases for `list` and `delete`. `--yes` skips a confirmation, and `--no-input` makes a command fail instead of prompting. `pluto help <command>` explains any command.
