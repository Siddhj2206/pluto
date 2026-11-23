# A push runs tasks in the repo's warm box

This amends ADR 0010 and adds a trigger to ADR 0007. A task with `push: true` runs when a commit is pushed to any branch of a connected repo, in the repo's durable box, and its result is reported on the commit in GitHub. M3 measured why: finpilot's build takes 36 s in a warm box and 12 min in a fresh one, and until now nothing connected a warm task to a push.

ADR 0010 refused a writable cache shared by CI jobs, because a branch is less trusted than the default branch and a later job would build real artifacts from what the branch left behind. A push-triggered task shares the box across branches anyway. What differs is the output: a check produces pass or fail, so a poisoned box can give a wrong answer but can't ship anything. For a solo developer the only pusher is the owner. For a small team, anyone who can push can already edit the workflow file, so the box adds little.

The rules that keep that true:

- **Only branches of the repo run.** Pluto watches `refs/heads` of the connected repo. A fork's pull request never appears there, so it starts nothing.
- **A bot's branch doesn't run.** On any branch but the default one, a commit whose author name ends in `[bot]` is recorded as refused, and no status is put on the commit, so the bot's pull request isn't marked as failing. A dependency update is code nobody has reviewed, and it would run in the box that later tests the default branch. On the default branch it runs, because merging it was a person's decision.
- **Branch runs get no secrets.** A push to any branch but the default one runs with the task's secrets unset, and the run's note says so. Runs a person starts by hand, scheduled runs and pushes to the default branch get them as before.
- **Anything that publishes stays in a fresh box.** Releases, signing and deploys belong in CI jobs (ADR 0002). `docs/tasks.md` says so.
- **`pluto box reset` clears the box**, as before.

How it works:

- **Connecting the repo is the switch.** `pluto ci connect github` already gives the host the repo and a token. There's no separate command to enable pushes. A repo with no `push: true` task costs one `git ls-remote` per check, and one small fetch per push to read the commit's `.pluto/`.
- **Pushes are found with `git ls-remote`,** not the REST API, so watching costs none of the token's request budget. The first check of a repo records its branches and runs nothing.
- **The config comes from the pushed commit.** A branch can add or change a task and see it run before it merges.
- **A newer push replaces a queued one.** A run still queued for the same branch and task is cancelled when a newer commit arrives. A run that has started finishes.
- **The result is a commit status** named `pluto/<task>`, which works with a personal token. The Checks API needs a GitHub App. If the token can't write statuses, the run still runs and its note says what the token needs.
- **A run records who it's for:** the commit's author, as `actor`.

## Considered options

- **`on: push`, as Actions spells it.** ADR 0007 left `on` out so the file doesn't read as a pipeline, and YAML 1.1 parsers read a bare `on` as true. `push: true` sits beside `schedule:` as one more flat key, and can grow into a list of branches.
- **A separate box for the default branch.** It removes the risk the bot rule covers, and ADR 0010 already allows a second box. It doubles a repo's disk and warm-up for a solo developer, who has no risk to remove. It's the first thing to offer a team.
- **A command to enable pushes per repo.** One more step between committing a task and seeing it run, to save a cheap `ls-remote`.
- **Asking the API who pushed.** The pusher is more exact than the commit's author, but costs a request per push, and the only decision that depends on it is the bot rule. A bot can't choose its author name, and a person who fakes one already has push access.
- **Webhooks.** Dropped on 2026-11-07: every stable public URL needs an account.

## Consequences

- Withholding secrets protects them from a branch's own steps, and the box is stopped after every run, so no process outlives the run that held them. Files are different. A branch can leave a replaced tool under `/root` for a later default-branch run, and a run that writes a secret to disk, such as a login file, leaves it for the next branch run to read. A task that holds a secret worth stealing shouldn't run on push in a shared box.
- A check appears up to one watch interval after the push, 20 seconds by default.
- A self-hosted bot that commits under a person's name isn't recognised as a bot.
- A branch that changes `image` replaces the box's container when it runs, and the default branch replaces it back. The volumes stay, so the cost is the image pull.
- The host fetches every pushed commit of a connected repo to read its `.pluto/`, so a private repo's token needs `Contents: read` even if the repo only uses CI. Without it, `pluto ci list` shows the problem and CI jobs still run.
- After the host has been off, every branch that moved gets one run for its newest commit.

**Revisit when:** a team joins a host (offer the default branch its own box), a branch run leaves state that changes a default-branch result, a bot's update needs a warm check before a person looks at it, or `git ls-remote` turns out to be rate-limited in practice.
