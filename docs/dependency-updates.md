# Dependency updates

pluto keeps its dependencies current with **self-hosted Renovate**: a scheduled
GitHub Actions run reads [`renovate.json`](../renovate.json) and opens pull
requests. There is no Mend-hosted app and no `mend.io` account — the run is a
job in this repository, using a token you control. The shape is adapted from
the projectbluefin self-hosted pattern
([`projectbluefin/renovate-config`](https://github.com/projectbluefin/renovate-config)
and finpilot's wrapper): a token-check job that reads the secret, then a run
job pinned to `renovatebot/github-action`.

Two workflows back it:

- [`.github/workflows/renovate.yml`](../.github/workflows/renovate.yml) — the
  scheduled/manual run that opens PRs.
- [`.github/workflows/validate-renovate.yml`](../.github/workflows/validate-renovate.yml)
  — `renovate-config-validator --strict` on `renovate.json`. Needs no token.

## What Renovate manages

| Manager | File | What it updates | Datasource |
| --- | --- | --- | --- |
| `gomod` (built in) | `go.mod` | `golang.org/x/sys`, `gopkg.in/yaml.v3`, the `go` directive | `go`, `golang-version` |
| `github-actions` (built in) | `.github/workflows/*.yml` | action refs, pinned to commit digests by `helpers:pinGitHubActionDigests` | `github-tags` / digests |

`config:best-practices` (the single `extends` entry in `renovate.json`) already
extends `helpers:pinGitHubActionDigests` and `docker:pinDigests`, so GitHub
Action refs are pinned to commit digests without a separate `extends` entry —
verified against the Renovate 44 `config:best-practices` preset, whose
`extends` list contains both helpers. The `github-actions` row above is that
helper at work.

**RE2 constraints.** If you add a regex manager, Renovate's regex engine is
RE2, which has no backreferences and no lookahead/lookbehind, and matches per
file — `^` and `$` mean the start and end of the whole file, not a line. Test
any change with the validator below.

## Automerge policy

Automerge starts narrow and widens as trust grows. The policy is one
`packageRules` array in `renovate.json`; **last matching rule wins**, so the
hold rules come after the automerge rules.

| Class | Match | Automerge |
| --- | --- | --- |
| Go modules, minor/patch | `gomod` + `minor`/`patch` | yes |
| First-party GitHub Actions digest/pin | `github-actions` + `actions/**`, `github/**` + `pin`/`digest`/`pinDigest` | yes |
| Third-party GitHub Actions | any other `github-actions` | no |
| Any major update | `major` | no — always |

`platformAutomerge` is `true`, so an automerged PR uses GitHub's native
auto-merge: Renovate enables auto-merge and GitHub merges once the required
checks pass. That needs two repository settings:

- **Allow auto-merge** enabled (Settings → General → Pull Requests).
- Branch protection on the default branch with required status checks (CI).
  Without required checks, GitHub merges the moment Renovate enables
  auto-merge.

Third-party actions are held deliberately: the Renovate run itself executes
`renovatebot/github-action`, and the release workflow holds
`contents: write`, so a compromised release must never land unreviewed.

**Reversing it.** The automerge policy is the `packageRules` block plus the
`platformAutomerge` key. Delete or comment those two sections and every update
waits for a human again.

## `RENOVATE_TOKEN`

The run workflow needs a GitHub token with write access to this repository,
stored as the Actions secret `RENOVATE_TOKEN`. A **fine-grained personal
access token** is enough:

1. GitHub → Settings → Developer settings → **Fine-grained tokens** →
   **Generate new token**.
2. **Resource owner**: `Siddhj2206` (or the org, if this repo ever moves).
   **Repository access**: *Only select repositories* → `Siddhj2206/pluto`.
   Do not grant access to other repositories.
3. **Repository permissions**:
   - **Contents: Read and write** — push branches and commits.
   - **Pull requests: Read and write** — open and update PRs, enable
     auto-merge.
   - **Issues: Read and write** — the dependency dashboard issue.
   - **Workflows: Read and write** — required to change files under
     `.github/workflows/`, which is where the action digest pins land.
   - **Metadata: Read** — mandatory, added automatically.
4. Set an **expiration** and rotate the token before it lapses; a run with an
   expired token fails loudly at the first API call.
5. Add it: repo → Settings → Secrets and variables → Actions → **New
   repository secret** → name `RENOVATE_TOKEN`, value the token.

How the workflow consumes it: the `token` job reads
`secrets.RENOVATE_TOKEN`, publishes only a `configured` boolean, and the
`renovate` job runs only when it is set, passing the secret to the
`renovatebot/github-action` `token:` input. If the secret is missing the run is
skipped, not failed, so a fork or a fresh clone does not break the schedule.

Because the token writes to the repository, treat it as a credential: never
print it in logs, never add it as a workflow `env` at the top level, and do not
share it with fork PRs (GitHub does not expose actions secrets to pull requests
from forks, so those runs simply skip).

## Running and validating

From the Actions UI: **Actions → Renovate → Run workflow**, set `dry_run` to
`true` for a log-only run. Runs on pull requests that touch the config are
forced to dry-run, so a broken config is caught before merge.

Locally, validate the config exactly as the validate workflow does (Node 24;
Renovate requires `^24.11`):

```sh
npx --yes --package renovate@44.138.0 renovate-config-validator --strict renovate.json
```

A local dry-run against the checkout needs no `RENOVATE_TOKEN`, because it does
not write. `--platform=local` does not auto-discover the config, so point it at
`renovate.json` with `RENOVATE_CONFIG_FILE` (the action sets the same input):

```sh
# Optional: a read-only token raises the GitHub API rate limit.
export GITHUB_COM_TOKEN="$(gh auth token)"
RENOVATE_CONFIG_FILE=renovate.json npx --yes --package renovate@44.138.0 renovate \
  --platform=local --dry-run=lookup --onboarding=false
```

The full authenticated run is what a maintainer runs when they want Renovate to
open PRs from a workstation:

```sh
RENOVATE_TOKEN=... npx --yes --package renovate@44.138.0 renovate --dry-run=lookup
```

Without `RENOVATE_TOKEN`, that command stops with `You must configure a GitHub
token` — expected, and the reason the scheduled run keeps the token in Actions
secrets.

## Tuning

`prHourlyLimit` (10) and `prConcurrentLimit` (20) cap the flow. The schedule is
every six hours (`.github/workflows/renovate.yml`); `renovate.json` runs in
`Etc/UTC`. To pause Renovate entirely, disable the `Renovate` workflow — the
dashboard issue goes stale but nothing else changes.
