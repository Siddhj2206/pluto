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
| `timeout` | Optional. How long a run may take, as a Go duration such as `90m`, `6h` or `1h30m`. Defaults to 12 hours. |
| `secrets` | Optional. Names of the repo's secrets to pass to this task, such as `[API_TOKEN]`. |

Set `image` once per repo in `pluto.yml`. Tasks can't set their own image.

## The box

Each repo gets one durable box. It is created the first time the repo runs a task, and it is kept between runs.

- `/workspace` and `/root` are kept between runs, and between image changes. If you change `image` or `requires`, Pluto replaces the container and keeps those directories. `pluto box reset` deletes them.
- A repo's box runs one thing at a time. A task that comes in while the box is busy waits in the queue.
- Pluto pulls the image when it creates the box. If the pull fails and an older copy is on the machine, it uses that copy.
- The image must contain `sh` and `sleep`, and the other commands Pluto's scripts call: `tail`, `head`, `cat`, `mv`, `rm` and `kill`, plus `xargs` when files are deleted from the repo. The script uses `bash` when the image has it and `sh` otherwise.

## What a run gets

- The repo's files are copied into `/workspace`. `.git` is not copied, and neither are files that git ignores.
- A manual run (`pluto run`) copies your working tree, including uncommitted changes and new files that aren't ignored. A scheduled run uses the committed branch.
- Files you deleted from the repo are deleted from `/workspace` on the next run. Ignored files stay where they are.
- Each run gets these environment variables: `PLUTO_RUN` (the run ID), `PLUTO_TASK` (the task name), `PLUTO_REF` (the branch, or `working tree` for a manual run) and `PLUTO_SHA` (the commit, empty for a manual run). Listed secrets are set too.
- `pluto run -- <command>` runs a one-off command in the same box. One quoted argument is shell, so `pluto run -- 'make && make test'` works. Several arguments keep their boundaries.
- Steps run in order from `/workspace`. The log shows each step's commands, prefixed with `$`. The run stops at the first step that fails.
- The timeout counts from when the run starts. Time the machine spends asleep doesn't count. A log stops growing at 256 MiB.

## Schedules

1. Commit a task with a `schedule:` key to a branch of the repo.
2. In the repo, run `pluto schedule enable`. It follows the repo's default branch. Use `pluto schedule enable --branch <name>` for another branch. The repo needs an `origin` remote.
3. Check with `pluto schedule ls`. Turn schedules off with `pluto schedule disable`. The box and its runs stay.

Rules:

- Times are the machine's local time. A `TZ=` prefix is an error.
- If the machine was asleep or the host was stopped, all the slots that passed collapse into one run. The run's note says how many slots it covers.
- If a slot comes while the same task is still running or queued, Pluto records a `skipped` run and doesn't queue another.
- Pluto checks the branch about every 15 minutes, so a change to the branch takes up to that long to apply. A new or changed schedule starts from the time Pluto notices it, so it doesn't run for slots that passed before.
- If the branch can't be fetched, Pluto runs the last commit it fetched and says so in the run's note. If the new config is broken, the last good config keeps running, and `pluto schedule ls` shows the problem.

## Staying awake

On Linux with systemd, Pluto asks the machine not to sleep while a run is active or queued, or while a scheduled slot is less than five minutes away. It does this only on mains power. On battery, the machine sleeps as usual. While the request is held, closing the lid doesn't sleep the machine.

This is a request, not a guarantee. If the machine sleeps anyway, Pluto records the gap in the affected runs' notes.

## Secrets

```sh
pluto secret set API_TOKEN     # Prompts for the value, or reads it from a pipe.
pluto secret ls                # Lists names only.
pluto secret rm API_TOKEN
```

Secrets are stored in the host's database, unencrypted. The file is readable only by your user. A secret reaches the run as an environment variable. If a task prints a secret, the secret appears in the log.

## Privileged boxes

A task that needs to run containers or sandboxes, as BuildStream does, can ask for `requires: [privileged]`. Pluto refuses that unless two things are true: the machine's engine is rootless, and you have allowed it with `pluto box grant privileged`. Code in the repo then acts with your user's rights on that machine. `pluto box revoke privileged` takes the grant back.

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

## Commands

```
pluto doctor                   Check this machine and print what to do next
pluto host start|stop|status   Run the host as a background service
pluto ci connect github        Connect the current repo
pluto ci ls                    List connected repos and stuck jobs
pluto run <task>               Run a task from .pluto in this repo's box
pluto run -- <command>         Run a one-off command in this repo's box
pluto runs [--since 24h] [--json]  List runs
pluto logs [-f] <run>          Show a run's log
pluto cancel <run>             Stop a task's run
pluto enter <run>              Open a shell in a run's box
pluto schedule enable|disable|ls  Run this repo's scheduled tasks
pluto box ls|reset             Show a repo's durable box, or start it over
pluto box grant|revoke privileged  Let this repo's box use privilege
pluto secret set|ls|rm         Manage this repo's secrets
pluto version                  Show the version
```
