# Config lives in `.pluto/`, as flat named tasks

Pluto's own tasks and schedules live only in a repo's `.pluto/` directory, the way GitHub's live in `.github/`. Shared settings and small tasks go in `.pluto/pluto.yml`, and a larger task can have its own file at `.pluto/tasks/<name>.yml`, where the filename is its name. Those files are found automatically, so there's no `include:` key and no references between files. A task is a flat set of keys with a `run:` list that executes in order and stops at the first failure. Anything longer belongs in a script in the repo. We leave out Actions' `jobs`, `steps` and `on`, because this file names tasks and doesn't describe a pipeline. Dependencies and parallel steps stay out too. CI doesn't use this config at all (ADR 0002).

The repo's config can only request privilege, using `requires:`. The host decides whether to grant it, so a repo can't give itself root-equivalent access.

## Considered options

- **Actions-like `jobs` and `steps`.** Familiar, but it adds nesting with no benefit here, and invites requests for Actions compatibility.
- **A root `pluto.yml` as well.** More visible, but two locations raise the question of which one wins.
- **`include:` or typed file references.** Flexible, but they need merge rules and path checks, and the previous design's version of this grew complicated.

## Consequences

- Config is less visible at the repo root.
- Defining a task name twice is an error that names both files, since nothing merges.
- M2 settled the keys from a real BuildStream build in `fsdk-containers`: `image` and `requires` for the repo's box, and `run`, `schedule`, `timeout` and `secrets` for a task. `docs/tasks.md` describes them.

**Revisit when:** a real task can't be expressed without dependencies or parallel steps, or users repeatedly can't find their config.
