# The CLI: daily verbs on top, things you manage as noun then verb

Commands people type every day are short verbs at the top level: `run`, `exec`, `history`, `logs`, `cancel` and `enter`. Everything they manage is a noun with verbs under it: `task`, `schedule`, `box`, `secret`, `ci` and `host`. Docker and the GitHub CLI both work this way, and it follows the guidance at clig.dev.

- **No look-alike commands.** The run list is `history`, not `runs`, because `run` and `runs` differ by one letter and a typo would start work instead of listing it. `runs` points to `history`.
- **Two ways to run a task.** `pluto run <task>` uses the working tree. `pluto schedule run <task>` uses the committed branch, exactly as the schedule would, and says which commit it used. A one-off command is `pluto exec`, so a task name and a command can't be mistaken for each other.
- **One name per idea.** Every list is `list`, with `ls` as an alias, and every list takes `--json`, wrapped as `{"schema": 1, ...}`. Deleting is `delete`, with `rm` as an alias. Flags have long names, with short ones only for common flags such as `-f`.
- **Flags anywhere.** `pluto run build --detach` works. Anything after `--` is never read as a flag.
- **Prompts are optional.** A prompt only appears in a terminal. `--no-input` turns every prompt into a failure that names what to pass instead, and destructive commands accept `--yes`.
- **Streams and exit codes.** Results go to stdout, and messages and prompts go to stderr. Exit codes are 0 for success, 1 for failure, 2 for misuse and 3 when the host isn't running. Every error names the next command to try where one exists.

## Considered options

- **Noun groups for everything, such as `pluto run list`.** More uniform, but `run` would mean both a run and starting one, and `list` couldn't be a task name.
- **`pluto ps` for the run list.** Familiar from Docker, but it suggests processes rather than a history of results.

## Consequences

- New commands go in the table in `internal/cli/commands.go`, which also produces help.
- Renaming a command later needs an entry that points to the new name.

**Revisit when:** remote devices or teammates need a different top level, for example commands aimed at another host, or people repeatedly reach for a command that doesn't exist.
