# Task and run command model

M5 introduces `task` and `run` as separate nouns: a task is durable requested work, while a run is one execution attempt belonging to a task. The CLI exposes `pluto task create|ls|show|follow|logs` and `pluto run ls|show|logs`; creation and follow-up accept prompts, and inspection accepts task/run IDs with the existing unambiguous-prefix convention. Keep the existing `pluto run` lifecycle forms working during the additive migration, and expose declared-job discovery as `pluto job ls`; do not silently repurpose a successful existing invocation. Any later deprecation needs a warning and a published removal version.

Every read command added for tasks and runs supports `--json`. Its output is a single JSON document with named domain fields and a schema version; field names and meaning are a public interface, and incompatible changes require a new schema version. Human output follows ADR 0009. Global options remain before the command. Review/diff is a separate capability; task output must describe shared box changes honestly and may show task-specific diffs only for isolated boxes.

## Considered options

- Reuse `job` for both a contract declaration and an execution record: rejected because it blurs configured work with durable task history.
- Reinterpret `pluto run` as task creation immediately: rejected because the current command already runs declared jobs and ad-hoc commands; an additive migration is safer.
- Treat JSON as incidental output: rejected because automation depends on stable field names and cannot recover from undocumented changes.

## Consequences

The existing `run` verb and the new `run` noun manager coexist by argument shape (`pluto run <job>` versus `pluto run ls`). Existing invocations remain compatible while new workflows gain explicit task/run identities. Exact JSON fields follow the API types for each resource and are documented with the commands that emit them.
