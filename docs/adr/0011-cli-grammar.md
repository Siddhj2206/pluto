# CLI grammar: verb-first, one target, prefix ids, one next step

ADR 0009 fixed the CLI's *manners* (the `pluto:` / `next:` / `warning:` voice, the exit
table, grouped help). This ADR fixes the *grammar* around those manners, records pluto's
north star once, and amends 0009 rather than replacing it. It follows the comparables
research in `docs/research/cli-comparables.md` (#60) and is deliberately narrower than the
M5 information-architecture overhaul, briefed in `docs/m5-cli-overhaul.md`.

## The north star

**pluto is a verb-first CLI for one worktree's box, with noun managers for the collections
a host or client owns.** The hot path is a verb on the current worktree's box — `up`, `run`,
`attach`, `pause`, `status`, `ls`, `jobs`, `logs`, `destroy` — where the box is inferred
from the directory and, where named, accepted as an id, an unambiguous id prefix, or a
worktree path. Collections that grow actions get a noun manager with subverbs: `image`
(`import`, `ls`) and `device` (`add`, `ls`, `rm`) already are, and M5 extends the pattern to
jobs, schedules, and sessions. A noun manager may offer a shorthand for its hot path
(`mise run` for `mise tasks run`) but **pluto will pick one spelling and keep it**; docker's
`ps`/`images` legacy aliases are the cautionary tale.

Concretely, the target grammar is:

```
pluto [--socket PATH|--state-dir PATH|--device HOST] <verb> [target] [-- child argv...]
```

- Global flags come **before** the command, as in git, cargo, docker, and mise.
- A target defaults to the current worktree for the verbs that act on a box.
- `--` fences the work's argv from pluto's target (`pluto run -- pnpm test`,
  `pluto attach box -- uname -a`), the git convention.
- Ids are opaque; any unambiguous prefix of a printed short id resolves, and ambiguity is a
  resolution error (exit 1) that names how to disambiguate, as git exits 1 for an ambiguous ref.
- Human output goes to stdout as a table or `key: value`; warnings and errors keep the
  0009 voice. Machine output is deferred (below).
- Help stays grouped; `--version` is an alias of `version`, and `-h`/`--help` of `help`.

This is a north star, not a ceremony: it changes nothing about the verb set, which ADR 0002
owns, and it does not rename a single existing command.

## Bounded fixes this justifies now

Three fixes are safe to land before `v0.1.0` because they add a spelling or correct wording
without changing an existing successful path. Each has tests in `internal/cli`.

1. **`pluto --version` (and `-version`) is accepted, matching `pluto version`.** Every
   comparable ships it and mise lists `mise version`, `mise --version`, `mise -v`, `mise -V`
   as equals. pluto adds the long flag now; a short form is deferred because `-v` is the
   conventional `--verbose` (mise) and pluto has not decided its verbose flag.
2. **A per-command flag parse error speaks in the CLI's voice with the command's usage
   line.** 0009 promised "a usage error … usage is printed"; today the standard `flag`
   package prints a bare `flag provided but not defined: -x` and its own `Usage of status:`
   block with no `pluto:` line. A shared parse helper corrects this for every command.
3. **An ambiguous box-id prefix names the next step.** `resolveBox` returned a bare
   "matches more than one box" error, which fell through to 0009's generic daemon-log
   fallback — the wrong hint. It now names the fix, as git does ("asks you to disambiguate").

## Considered options

- **Noun-first everywhere (`pluto box ls`, `pluto box run`, …)**: docker's model and the
  likely M5 end state, but it is a rename of the whole surface. Doing it now would churn
  the verbs ADR 0002 fixed and destabilise the release; deferred as M5 work with a brief.
- **`pluto box` as a hidden alias now**: adds docker's two-spellings debt (the legacy
  aliases it is trying to retire) without adding capability. Rejected.
- **Default every read verb to the current worktree now** (`status`, `jobs`, `logs` with no
  target, like `up`/`attach`): coherent, but it changes what a bare invocation means and the
  errors-for-no-target suite is load-bearing. Deferred to M5 with the rest of the grammar.
- **`-v` as the short `--version`**: git does this, but `-v` is verbose in mise and cargo,
  and pluto has no verbose flag yet. Adding `--version` only keeps the short letter free.
- **A `--json` switch on reads now**: the right M5 direction (mise, gh, docker), but it is
  a whole output contract — field names, stability, `--jq` — and not a coherence fix.
  Deferred; the brief specifies it.
- **A CLI framework (cobra) to get prefix/id/flags for free**: already rejected in 0009 and
  unchanged; the hand-rolled `flag` package plus a parse helper is enough for these fixes.

## Consequences

- A user who types `pluto --version` gets the same answer as `pluto version`; scripts that
  probe a binary's version have the conventional spelling.
- Every usage error now has the same shape: `pluto: <what>`, then a `usage:` line; the
  per-command `next:` policy suite (0009) is untouched.
- The ambiguous-prefix failure is recoverable from the terminal alone, closing the one
  `resolveBox` path that gave the generic daemon-log hint for a local mistake.
- The verb set, help layout, exit codes, and all successful output are unchanged.
- **Deferred, with reasons, to M5:** noun-first managers for jobs/schedules/sessions;
  default targets for read verbs; `--json`/`--format`, `--quiet` (ids only), `--name-only`,
  `--no-header`; inherited target flags (`--box`, `--socket`, `--device` on subcommands);
  interspersed global flags after the command; a `sessions` surface; and renaming `ls`.
  Each is a compatibility decision, not a bug, so none belongs in a pre-release pass.
