# M5 task and run CLI decision (#103)

Parent spec: [#101](https://github.com/Siddhj2206/pluto/issues/101). Research notes: `/tmp/pluto-m5-research-103.md` (2026-10-07). Decision recorded in [ADR 0012](../adr/0012-task-run-cli-and-output.md).

## Decision

- Use distinct nouns: **task** is the durable requested-work identity; **run** is one execution attempt under a task. A declared **job** remains the reusable command configuration.
- Add `pluto task create|ls|show|follow|logs` and `pluto run ls|show|logs`. `task create` starts a task and first run; `task follow` adds a serialized run. IDs support the established unambiguous-prefix convention.
- Add `pluto job ls` for declared job discovery. Preserve existing `pluto run` execution behavior and other existing command forms during this additive step. Deprecation requires a warning and published removal version.
- New task/run read commands support `--json` as one public, versioned JSON document with named fields. Human output follows ADR 0009. Keep global flags before the command.
- Keep review/diff a separate design concern. Shared branch changes are box state; only isolation can support task-specific change attribution.

## Why

The current CLI uses `pluto run` both to list job declarations and execute them, while `pluto jobs` lists execution history. Task and run identities are new M5 domain concepts, so giving them explicit noun managers resolves the ambiguity without conflating declared jobs, historical jobs, and requested-work identity. A stable JSON contract supports scripts and clients without forcing them to parse terminal output.

## Compatibility and follow-up

Do not change the meaning of existing invocations silently. The old no-argument `pluto run` behavior can gain a deprecation notice only when a migration/removal version is agreed; it must continue working during the compatibility window. Add the new commands to the existing help/completion source. State persistence and API migration decisions belong with the task/run lifecycle design; the CLI uses those API resource shapes rather than inventing a separate data model.

This research resolves the task/run CLI vocabulary and output promise needed by #108. Hands-on CLI integration tests remain part of #108.
