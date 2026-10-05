# M4 brief — the CLI information-architecture overhaul

**Status:** brief for the M4 milestone, per `docs/VISION.md` ("A mise-grade pass over the
CLI: grammar, command taxonomy, help, output, and a review surface"). Not a design of
record; the recorded decision for the pre-M4 grammar is ADR 0011. Inputs:
`docs/research/cli-comparables.md` (#60), ADR 0011, ADR 0009, ADR 0002 (the verb set).

## Why M4 exists

M0–M3 grew the CLI one command at a time. Three seams are now visible and are safe to name:

1. **Taxonomy drift.** Verb-first lifecycle commands (`up`, `run`, `attach`, `pause`,
   `status`, `ls`, `jobs`, `logs`, `destroy`) sit beside noun managers (`image`, `device`),
   and the family that will grow most — jobs, schedules, sessions — has no noun manager.
   `pluto run` doubles as "list declared jobs", `pluto jobs` is run *history*, and
   `pluto status` lists sessions only incidentally.
2. **Output drift.** Human tables, `key: value` blocks, and bare lines coexist with no
   machine mode; scripts parse text. Every comparable offers `--json`; pluto does not.
3. **Grammar gaps deferred by ADR 0011** (default targets, inherited flags, `--quiet`,
   `--name-only`/`--no-header`, noun-first managers). None is a bug; together they are the
   overhaul.

## Target grammar (the north star ADR 0011 records, made concrete)

```
pluto [global flags] <verb|noun> [subverb] [target] [-- child argv...]
```

- **Verb-first for the lifecycle**, unchanged: `up`, `run`, `attach`, `pause`, `status`,
  `destroy`. The verb set is ADR 0002's and is not re-opened here.
- **Noun managers for collections that grow actions.** Candidate final shape:
  - `pluto box ls | status | pause | destroy | rm` (with the verb spellings kept as
    documented shorthands, or dropped — see open questions),
  - `pluto job ls | run | history | logs` (declared jobs vs recorded history made
    explicit; `pluto run` stays the shorthand for `pluto job run`),
  - `pluto schedule ls | run | logs` (M3 triggers land here),
  - `pluto session ls | attach` (M2 sessions get a real surface),
  - `pluto image import | ls`, `pluto device add | ls | rm` (already noun-first).
- **One shorthand per hot path, chosen once.** `pluto run` and `pluto attach` are the two
  hot verbs; if a noun manager offers them, it keeps the *same* spelling and the old one is
  either kept deliberately or retired on a published schedule. Docker's `ps`/`images`
  legacy aliases are the explicit anti-pattern.
- **One target model.** `<box-id|worktree>` everywhere, plus any unambiguous id prefix;
  `--box` as an inherited flag for when a positional is ambiguous. Kind inference from the
  current worktree (Amp's `namespace/name`, `owner/repo`, or URL pluralism) is the model.
- **Global flags before the command** (git/cargo/docker/mise). M4 decides whether to also
  accept them interspersed (clap/mise), which needs an argument-aware pre-pass.

## Migration shape

1. **Freeze and specify.** Land the output contract and the target grammar as ADR(s) before
   touching commands; derive help and completion from one table (pluto's `commandDocs` is
   already that table — M4 should generate usage, completion, and `--help` from it).
2. **Additive first.** Introduce noun managers and `--json` as *new* spellings; keep the
   current verbs working. No output changes to existing commands without a `--json` opt-in.
3. **Deprecate on a schedule, not silently.** Any rename prints `warning: <old> is
   deprecated, use <new>` once per invocation and is documented with a removal version; the
   alias is then removed. This is the docker lesson applied.
4. **Extract a command tree.** Replace the `switch cmd` + per-file flag parsing with a small
   declared tree (name, summary, usage, flags, run) that produces dispatch, help, and
   error/`next:` handling uniformly. Keep the hand-rolled `flag` package; do not add cobra
   (ADR 0009 considered and rejected it).
5. **Only then rename.** `ls` → `list` (and `rm` → `remove`) only if the deprecation path is
   accepted; otherwise keep the short classic names and stop answering the question twice.

## Output contract (the part scripts depend on)

- `--json` on every read command: a stable top-level object, named fields echoing the
  domain (`id`, `project`, `branch`, `worktree`, `state`, `phases`, `jobs`, `sessions`),
  one JSON document (not JSONL) unless `--stream` is asked for.
- `--format`/`-t` (Go template) and `--jq`/`-q` only if a real consumer appears; gh shows
  the pair, but it is surface area, not a promise to make now.
- `--quiet`/`-q` prints identifiers only (`pluto box ls -q`, `docker ps -q`); `--name-only`
  and `--no-header` for mise-style scripting.
- Human output stays: header-ed tables for lists, `key: value` for one entity, and the
  existing `pluto:` / `next:` / `warning:` voice (ADR 0009, unchanged).
- Errors keep the 0/1/2 exit table plus work-code pass-through; do not adopt cargo's 101.

## Help and discovery

- Grouped top level (already), per-command usage + examples (already), closest-match for
  unknown commands (already).
- Add: generated shell completion from `commandDocs`; `pluto help <noun> <subverb>` parity
  with `<noun> <subverb> -h` (already works, keep it); `--version` (landed in ADR 0011);
  and a `pluto help --all` for hidden/advanced commands (git's `--help-all`).

## Open questions for M4

1. **How far to noun-first?** Keep the lifecycle verbs as the primary surface (git/mise) or
   move to `pluto box`/`pluto job` managers (docker/gh)? ADR 0011 chose verb-first for now;
   M4 must decide the end state and the deprecation budget.
2. **Job vocabulary.** `job` is both a declaration (`[jobs.<name>]`) and a recorded run.
   Does M4 split them (`job` = declaration, `run` = a recorded instance) or keep one word?
   ADR 0007 deliberately avoided a second noun; reversing that needs a reason.
3. **Default targets.** Should `status`, `jobs`, and `logs` infer the current worktree like
   `up`/`attach`? Where a target is destructive (`pause`, `destroy`) it must stay explicit.
4. **Interspersed global flags.** Accept `pluto status box --socket X`, or keep options
   before the command? The former needs an argument-aware pre-pass and a rule for `--`.
5. **JSON stability promise.** Is the JSON a public schema (versioned, breaking-change
   policy) or a convenience? gh publishes field names; pluto would need the same discipline.
6. **Output on empty.** `ls`/`jobs` currently print only a header when empty; should they
   print a "no boxes" line and a `next: pluto up` hint like `run` does?
7. **A review/diff surface.** VISION names one; it is a new capability, not a rename, and
   needs its own design (Amp's changes view is the comparable, not a CLI grammar one).
8. **Session surface.** `pluto session ls` vs folding sessions into `status` and `attach`;
   M2 shipped the latter, M4 decides whether a first-class noun is earned.

## Non-goals

- Changing the verb set (ADR 0002) or the contract grammar (ADR 0007).
- A CLI framework; completion beyond what `commandDocs` supports.
- Re-litigating ADR 0009's voice, exit codes, or hint philosophy.
- Any output change without an opt-in before the deprecation window.
