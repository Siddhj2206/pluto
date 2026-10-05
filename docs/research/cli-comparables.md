# CLI grammar comparables — facts for pluto's coherence session (#60)

Fetched 2026-10-05. Primary sources only: vendor CLI references and man pages, quoted
verbatim unless marked. This note is the input to ADR 0011 (CLI grammar) and
`docs/m4-cli-overhaul.md`; it records what the comparables do, not what pluto should do.

Sources:

- mise — <https://mise.jdx.dev/cli/>, <https://mise.jdx.dev/cli/run.html>,
  <https://mise.jdx.dev/cli/tasks.html>, <https://mise.jdx.dev/cli/version.html>.
- Amp Orbs — <https://ampcode.com/docs/orbs>, <https://ampcode.com/docs/cli/spawning-orbs>,
  <https://ampcode.com/what-are-orbs>.
- GitHub Actions — <https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax>.
- git — <https://git-scm.com/docs/git>, <https://git-scm.com/docs/gitcli>.
- cargo — <https://doc.rust-lang.org/cargo/commands/cargo.html>.
- docker — <https://docs.docker.com/reference/cli/docker/>, <https://docs.docker.com/reference/cli/docker/container/ls/>.
- gh — <https://cli.github.com/manual/>, <https://cli.github.com/manual/gh>, <https://cli.github.com/manual/gh_issue_list>.

---

## 1. mise — the stated inspiration

**Shape.** `mise [FLAGS] [COMMAND | TASK] [ARGS]…`. The synopsis itself teaches the
grammar: "Square brackets mark optional input, angle brackets mark required input, and `…`
means the argument can repeat." The docs are explicit about placement: "Put mise flags
before a task name; arguments after the name are passed to that task."

**Flat verb space, with noun managers where a family needs one.** Most top-level commands
are verbs (`use`, `install`, `uninstall`, `upgrade`, `ls`, `outdated`, `run`, `watch`,
`exec`, `env`, `shell`, `set`, `activate`, `doctor`, `version`, `self-update`, `implode`).
A smaller set are **nouns that manage a collection**: `mise tasks`, `mise config`,
`mise settings`, `mise cache`, `mise plugins`, `mise backends`, `mise dotfiles`. The split
is not verb-vs-noun by rule; it is "the common actions are verbs, the families are nouns."

**The task family is the closest precedent for pluto's run/jobs tension.** `mise tasks`
is a noun manager with subcommands `add`, `deps`, `edit`, `graph`, `info`, `ls`, `run`,
`validate`. `mise run` is a top-level **shorthand** for `mise tasks run`. So mise keeps
both a verb (`run`) and a noun manager (`tasks`) and points them at the same object
without renaming either. `tasks ls` lists declared tasks; `run` executes them.

**Output flags are first-class.** `mise tasks` takes `-J --json`, `--name-only`,
`--no-header`, `--sort <column>`, `--sort-order`, `--hidden`, `-x --extended`. `mise version`
takes `-J --json`. Scripts get a structured and a bare-names mode; humans get a table.
Read commands are labelled `**Effect:** read-only` in the reference.

**Version is supported four ways.** The `mise version` page lists examples
`mise version`, `mise --version`, `mise -v`, `mise -V`. So a subcommand, a long flag, and
two shorts all resolve to the same thing.

**Global flags.** `-C --cd <DIR>`, `-E --env`, `-j --jobs`, `-q --quiet`, `-v --verbose`
(`-vv` for more), `-y --yes`, `--raw`, `--locked`, `--silent`. The reference warns:
"A command can define its own flag with the same name, so consult that command's page for
placement and meaning." `-y/--yes` is global, not per-command.

**Adoptable for pluto:** the verb-plus-noun-manager split with a shorthand, `--json` on
every read command, `--name-only`/`--no-header` for scripts, and `--version`/`-v` as
aliases of the `version` subcommand. Also the synopsis legend ("square brackets optional,
angle required") as documentation voice.

## 2. Amp Orbs — the durable-work-item ergonomics

**Shape.** Amp's primary handle is a **thread**, not an orb; an orb is the machine a thread
runs on. "Every orb thread gets a fresh, isolated environment…"; `amp -ox "prompt"` starts
an execute-mode thread in an orb, "prints the thread URL, and exits right away. The agent
keeps working after the command returns." Continuation is explicit:
`amp threads continue T-… -ox "…"` and "the message runs on that thread's own orb." The
durable handle is an opaque id (`T-…`) surfaced as a URL.

**Sleep and wake are the product.** "When the agent is done, the orb goes to sleep, and a
sleeping orb costs nothing, even if it sleeps for weeks. When you send another prompt, the
orb wakes up with your conversation, files, and services still in place." The wake trigger
is the next interaction, not a CLI verb.

**Targeting accepts several spellings.** `--project` takes "an Amp project
(`namespace/name`), a GitHub `owner/repo`, or a repository URL." Default is inferred from
the current directory's git remotes; ambiguity or no match is a warning, and the orb starts
without a repository rather than failing.

**Mode separates bounded from interactive.** Execute mode (`-x`, `-ox`) starts work and
returns; the TUI/`--executor orb` waits for a prompt. `--stream-json` keeps a caller
attached for a turn; `--attach`, `--mode`, `--fast`, `--title`, `--orb-size` shape the
thread. `amp sync <thread>` mirrors changes back to the local checkout.

**Adoptable for pluto:** a bounded run and an interactive attach are distinct verbs (pluto
already has `run`/`attach`); print a durable handle on create and re-enter by it; accept
id-or-path-or-name for the target; let identity be inferred from the worktree's git remote;
and treat sleep as a non-event the next interaction wakes.

## 3. GitHub Actions — the contract grammar, not the CLI

Actions has no general-purpose CLI; its comparable is the **declarative workflow grammar**.
A workflow is `name`, `run-name`, `on`, and `jobs.<job_id>`. `on` lists events
(`push`, `pull_request`, `schedule`, `workflow_dispatch`, `workflow_call`);
`on.schedule` uses "POSIX cron syntax… By default, scheduled workflows run in UTC" with an
optional IANA `timezone`, and "The shortest interval you can run scheduled workflows is
once every 5 minutes." Jobs have string ids that are the reference key: `needs`,
`jobs.<job_id>.outputs`, and `jobs.my_job.outputs.job_output1` all speak the same id.
`workflow_dispatch.inputs` are **typed** (`boolean`, `choice`, `number`, `environment`,
`string`) with `description`, `default`, `required`, `options`.

**Adoptable for pluto:** this is the shape `.pluto.toml` already borrowed (`[jobs.<name>]`,
`[[schedule]] name/cron/job`, ADR 0007). The lesson for the CLI is that the **name is the
shared key** across declaration, invocation, and history, and that typed, described inputs
are the natural extension for manual runs (M3/M4). It is not a CLI-grammar comparable and
should not be cited as one.

## 4. git — verb-first, options-first, `--` as the fence

**Shape.** `git [-v | --version] [-h | --help] [-C <path>] … <command> [<args>]`. Global
options precede the command. `-v/--version` "Prints the Git suite version"; `-h/--help`
prints the synopsis. Both flags are **aliases for subcommands**: "This option is internally
converted to `git version` …" and `--help` becomes `git help`. Help wins ties: "If `--help`
is also given, it takes precedence over `--version`."

**gitcli conventions.** The rules are unusually explicit and worth quoting:

- "Options come first and then args. … You SHOULD give dashed options first and then
  arguments."
- "Revisions come first and then paths."
- "When an argument can be misunderstood as either a revision or a path, they can be
  disambiguated by placing `--` between them." Without it, git "makes a reasonable guess,
  but errors out and asks you to disambiguate when ambiguous."
- "Options trump configuration and environment."
- Long options accept a **unique prefix**: "Commands that support the enhanced option
  parser accepts unique prefix of a long option as if it is fully spelled out, but use this
  with a caution" — a later option sharing the prefix can break a previously-unique one.
- `-h` gives "a pretty printed usage of the command"; magic `--help-all` shows hidden
  plumbing options.

**Naming.** High-level "porcelain" is mostly verbs (`add`, `commit`, `status`, `branch`,
`log`, `diff`, `push`), with noun managers for multi-action families (`remote`, `stash`,
`config`, `worktree`, `submodule`) that take subverbs (`git remote add`).

**Ids.** Object names are hashes, and git accepts a unique abbreviation; `--abbrev[=<n>]`
controls display length (`git describe -h` shows `--abbrev[=<n>]`).

**Adoptable for pluto:** global flags before the command; `--` as the unambiguous fence
between pluto's target and the work's argv; options-first in scripts; any unique prefix of a
printed short id resolves; `--version`/`-h` as aliases of the `version`/`help` subcommands,
with help winning.

## 5. cargo — one binary, grouped help, coarse exit codes

**Shape.** `cargo [options] <command> [args]`, plus `cargo --version`, `--list`, `--help`,
`--explain <code>`. `-V/--version`; `-v/--verbose` "May be specified twice for 'very
verbose'"; `-q/--quiet`; `--color when`. `-C <path>` "must appear before the command name."

**Help groups commands by category** (Build, Manifest, Package, Publishing, Report,
General), each with a one-line description. `cargo version` and `cargo help <command>` are
the General commands. Running a binary passes args after `--` (`cargo run -- args`).

**Exit codes are deliberately coarse:** `0` success, `101` failure — not `1`. This is the
opposite of Unix convention and cargo owns it.

**Adoptable for pluto:** grouped help (pluto already groups); `-V/--version`; explicit
`--` for a child process's argv (pluto's `run --`/`attach --`); a documented, small exit
table. Do **not** copy 101 — pluto's 0/1/2 plus work-code pass-through is the honest
surface (ADR 0009).

## 6. docker — noun-first, with a documented migration off shortcuts

**Shape.** Management commands are noun-first: `docker container`, `image`, `network`,
`volume`, `context`, `system`, `service`, `stack`, `node`, `secret`, `config`, `plugin`,
`builder`, `manifest`, `trust`. Subverbs are `ls`, `inspect`, `rm`, `create`, `run`.

**The top-level shortcuts are explicitly legacy.** `docker ps` is an alias of
`docker container ls` (`Aliases: docker container list, docker container ps, docker ps`),
and `DOCKER_HIDE_LEGACY_COMMANDS` exists to hide "legacy" top-level commands "in favor of
`Management commands` per object-type." So docker is a live example of a CLI paying for
two spellings of the same action and then trying to retire one.

**Output is structured and truncates ids.** `docker container ls` prints a
`CONTAINER ID` header and a **truncated** id by default; `--no-trunc` prints the full id,
`-q/--quiet` prints "Only display container IDs," and `--format` takes a Go template,
`table`, or `json`. `docker --config … ps` shows global flags before the command. Help is
`docker <command> --help`, `Usage: docker run [OPTIONS] IMAGE [COMMAND] [ARG...]` with
short-and-long options (`-a, --attach`).

**Adoptable for pluto:** noun managers are the natural home for host/client collections
(images, devices, and later jobs/schedules/sessions); short ids in human output with a way
to get the full one; `-q` for ids only; `--format`/`--json`; short-and-long flag spellings.
The cautionary lesson is the legacy-alias debt — if pluto adds `pluto job ls` it should pick
one spelling for the shorthand, not accrete both.

## 7. gh — noun-first with inherited flags and JSON everywhere

**Shape.** `gh issue list|view|create|close|…`, `gh pr …`, `gh run …`, `gh workflow …`,
`gh repo …`, `gh release …`, `gh auth …`, `gh config …`, `gh secret|variable|label|gist`.
A few top-level verbs stand alone: `gh api`, `gh browse`, `gh search`, `gh status`,
`gh help`, `gh completion`. `gh --version` is the only listed global option on the root
page.

**Inherited flags.** Subcommand pages have "Options inherited from parent commands" —
`gh issue list` inherits `-R, --repo <[HOST/]OWNER/REPO>`. Flags flow down the command
tree rather than being repeated per leaf.

**JSON is universal on reads.** `gh issue list` takes `--json <fields>`, `-q/--jq`,
`-t/--template`; the manual lists the exact JSON field names. Its default output is a
compact numbered list with no table header ("Issues for owner/repo", then `#14  Title`).

**Adoptable for pluto:** noun-first families where an entity has several actions; inherited
target flags (`--box`, `--socket`) instead of repeating them; `--json` with named fields plus
a `--jq`-style filter (M4); compact default listing for humans.

---

## What the comparables agree on

1. **One binary, one grammar:** `<program> [global flags] <command> [subcommand] [args]`,
   with global flags before the command (mise, git, cargo, docker). `--` fences the child
   process's argv (git, cargo).
2. **Verb-first for common actions, noun-first for entity families.** git and mise keep a
   flat verb space and add noun managers only for multi-action families; docker and gh use
   noun managers broadly. The direction of travel when a family grows is noun-first
   (docker is migrating its `ps`/`images` shortcuts to `container ls`/`image ls`).
3. **A shorthand for the hot path coexists with the noun manager** rather than replacing it
   (`mise run` ↔ `mise tasks run`; `docker ps` ↔ `docker container ls`) — but docker shows
   the cost of never retiring the shorthand.
4. **Identity is an opaque id plus a human name/path; every command accepts either, and
   any unique prefix of the id works** (git abbreviations; docker name-or-id and short ids;
   Amp's `T-…` thread plus `namespace/name` or URL; pluto's box id plus worktree path).
   Ambiguity is an error that tells you how to disambiguate (git: "errors out and asks you
   to disambiguate").
5. **Read commands have a machine mode:** `--json` (mise, gh, docker `--format json`) and,
   in gh, `--jq`/`--template`. Human output is a header-ed table (docker) or a compact list
   (gh; mise `--no-header`/`--name-only` for scripts).
6. **Help is grouped, per-command, and example-bearing**, and `--version`/`-h` are aliases
   of the `version`/`help` subcommands (git, cargo, mise; gh has `--version`).
7. **Errors and confirmations have a consistent voice and a documented exit table.**
   git disambiguates; docker/mise/gh use a stable prefix; cargo documents 0/101; mise and
   docker make `--yes` available for destructive confirmation.

## What pluto could adopt (input to ADR 0011)

- Keep the verb set (ADR 0002 owns it) and **name a north star now**: verb-first for the
  lifecycle, noun managers for collections, with a shorthand for the hot path.
- **`--version` as an alias of `version`**, the one universally-agreed gap today.
- **Prefix resolution with a disambiguation next step** — pluto already shortens ids; the
  ambiguous case should say how to fix it, per git.
- **`--json` on reads in M4**, with named fields like gh, plus `--name-only`/`--no-header`
  (mise) for scripts.
- **Inherited target flags** (`--box`, `--socket`, `--device`) rather than per-command
  repetition, per gh.
- **Pick one spelling for shorthands** and never accrete both, per docker's legacy debt.
- Keep the `pluto:` / `next:` / `warning:` voice and the 0/1/2 exit table (ADR 0009); the
  comparables do not argue against it.
