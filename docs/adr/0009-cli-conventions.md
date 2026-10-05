# CLI conventions: errors name the next step

The CLI is the only control surface (ADR 0006), so its manners are part of the product: a failure should be recoverable from the terminal alone. Every failure prints `pluto: <what happened>` followed by one or more `next: <concrete step>` lines, with commands single-quoted so they can be pasted. Warnings print as `warning: <what happened>` and do not fail the command. Usage errors print usage.

Hints live in the CLI, not behind the socket: the daemon and the guest agent send facts, and the CLI adds a curated hint where it has one and a generic fallback otherwise. A contract parse error carries `file:line`, because the fix is an edit at that spot. For example:

```
pluto: no box for worktree /home/sid/src/app
next: create it with 'pluto up'
```

Help follows the same manners. Parsing stays hand-rolled on the standard `flag` package. `pluto help` prints grouped top-level usage on stdout and exits 0; `pluto help <command>` and `<command> -h/--help` print per-command details with examples; a bare `pluto` prints usage on stderr and exits 2; an unknown command gets the closest match plus a next step. `box` and `vsock` are internal and stay out of the listing.

Exit codes are intentionally few:

- `0` — success, including `pluto help` and `pluto version`.
- `1` — the command failed; the first line is `pluto: <what happened>`.
- `2` — a usage error: a missing, unknown, or malformed invocation; usage is printed.
- Work commands — `pluto run` and `pluto attach` — pass through the exit code of the work they ran, so scripts and pipelines see what the command saw.

Each command gets a test asserting a `next:` line for a representative failure, so the policy cannot silently erode.

## Considered options

- **A CLI framework (cobra/urfave)**: adds a dependency and help formatting that would fight the voice; the standard `flag` package plus a usage function is small enough.
- **Hints from the daemon and agent**: they know the failure but not the caller's terminal context; hints at the CLI edge stay curated, testable, and string-only over the wire.
- **A richer exit-code table (sysexits-style)**: no script consumes it yet; 0/1/2 plus pass-through is the honest surface, and more codes can be added later.
- **Bare `pluto` as help on stdout, exit 0**: invites scripts to invoke it by accident; a bare invocation is a usage error and exits 2.

## Consequences

- Error output is consistent and copy-pasteable; `grep '^next:'` works as an affordance for humans and tests.
- The daemon/agent protocol stays advice-free: hint wording changes do not ride the wire.
- Hidden commands keep working for internals and debugging but are not discoverable.
- A run command's exit code can collide numerically with usage (2) and failure (1); the work's code wins, because that is what scripts need.
