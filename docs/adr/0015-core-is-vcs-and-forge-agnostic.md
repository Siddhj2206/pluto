# Core is agnostic to version control and forges

The core daemon and store model only repos, workspaces, layers, volumes, boxes, tasks, and runs. Git lives behind a vcs adapter and GitHub behind a forge adapter; the core never names a branch, workflow, or job. This keeps scheduling, task/run state, contract parsing, and reporting substrate- and forge-neutral, and lets ephemeral CI be expressed as ordinary runs rather than a parallel system. Resolving a CI job's revision from git — the pre-checkout step — happens in the vcs adapter before a box boots.

## Consequences

- A future forge (GitLab, Gitea) is a new adapter, not a core change.
- The core cannot depend on worktree or branch vocabulary; a workspace is a path, and anything finer is the adapter's business.
