# The Actions subset and the parity-tail boundary — facts for the workflow front-end

Fetched 2026-10-10. Primary sources: GitHub Actions docs (workflow syntax, metadata syntax,
workflow commands, dependency caching, limits, events), the `actions/cache` and
`actions/upload-artifact` source bundles, `actions/toolkit` (`@actions/artifact`), the
`nektos/act` and `gitea/actionslib` source, the Gitea / Forgejo / Woodpecker / Concourse docs
and repos. Evidence is tagged:

- **[docs]** — documented behaviour on the vendor's own pages.
- **[vendor claim]** — the vendor's own claim, not independently verified.
- **[repo]** — source or repo README.
- **[inference]** — this author's reading, not a vendor statement.

This note answers the five questions in #120 and ends with a supported-subset spec, the
parity-tail boundary, and an interop plan for ghcr and the Actions cache. It builds on and
does not repeat:

- `docs/research/devex-and-comparables.md` §1.3–1.5 (CI runners, self-hosted CI, caching) —
  the landscape map. §6.5 already rules out build-system semantics in the mechanism and
  substrate leakage; this note extends the CI line specifically.
- `docs/research/m4-event-delivery-and-trust.md` — webhook intake, the
  `pull_request` / `pull_request_target` trust split, log masking. It already concluded
  `GITHUB_TOKEN` is "Not applicable — Pluto is not GitHub Actions" (line 168).
- `docs/research/m4-queue-and-hooks.md` — the current scheduler (busy-box skip,
  missed-occurrence coalescing, at-least-once) and the job-record shape a workflow run must
  land in.
- `docs/research/cli-comparables.md` §3 — already established that the Actions *workflow
  grammar* is the contract grammar, not a CLI comparable, and that `.pluto.toml` borrowed its
  shape. This note goes one level down: which parts of that grammar survive contact with a
  small self-hosted fleet.
- `docs/research/m5-task-run-cli.md` / ADR 0012 — task / run / job vocabulary, the model a
  translated workflow must map onto.

Scope, per the moonshot: individuals and teams, trusted code, hardware they own, a handful of
machines. Everything below is judged at that scale. Where a feature only pays off at fleet
size, it is in the parity tail.

---

## 1. What the Actions grammar actually is, measured

Before choosing a subset it is worth measuring the surface. The reference page for workflow
syntax has **74 top-level or `jobs.<job_id>`-level keys**. That is the number the parity tail
grows from. [repo] (`content/actions/reference/workflows-and-actions/workflow-syntax.md`,
`grep -c '^## \`'` → 74).

Breakdown, from the same file:

| Area | Keys | Character |
|---|---|---|
| `on.<event>` and `on.<event>.<filter>` | 25 | Forge-shaped; the biggest single block. |
| `jobs.<job_id>.container.*` and `jobs.<job_id>.services.*` | 22 | Substrate-shaped; assumes a Docker runner. |
| `jobs.<job_id>.strategy.*` | 4 | Fan-out. |
| `jobs.<job_id>.steps[*]` | 13 | The step; the part real repos use. |
| `jobs.<job_id>.uses` / `with` / `secrets` (reusable workflows) | 6 | Cross-repo composition. |
| `permissions`, `env`, `defaults`, `concurrency`, `cache-mode`, `outputs`, `needs`, `if`, `runs-on` | 12 | The middle. |

The expression language is small by comparison: **15 functions** (`contains`, `startsWith`,
`endsWith`, `format`, `join`, `toJSON`, `fromJSON`, `hashFiles`, `case` × 6 booleans plus
`success`/`always`/`cancelled`/`failure`) across **14 contexts** (`github`, `env`, `vars`,
`job`, `jobs`, `steps`, `runner`, `secrets`, `strategy`, `matrix`, `needs`, `inputs`,
`job`/`jobs` containers). [repo] (`workflow-syntax.md`, `expressions.md`, `contexts.md`)

**[inference]** This shapes the decision. The expensive part of Actions parity is not the
grammar; it is (a) the ~25 forge event keys, (b) the ~22 container/service keys, and (c) the
tiny operational interfaces the actions in the ecosystem depend on — the cache service, the
artifact service, the `GITHUB_*` file protocol, and the toolkit. Those operational interfaces
are finite and reimplementable (act reimplemented the cache service in a few hundred lines of
Go). So a subset that keeps `on`/`jobs`/`steps`/`uses`/`env`/`needs`/artifacts and reimplements
the operational interfaces is a real subset, not a gesture.

---

## 2. The subset that must be supported to be useful

Ordered by how much of real-repo coverage each buys. The first four are non-negotiable; a
workflow front-end without them cannot run a checkout-and-test workflow at all.

### 2.1 `on:` — five event keys, not 25

| Key | Verdict | Why |
|---|---|---|
| `push` (+ `branches`, `branches-ignore`, `tags`, `tags-ignore`) | **Support** | The default CI trigger; every repo has it. |
| `pull_request` (+ the same filters, `types`) | **Support** | The other one every repo has. |
| `schedule` (+ `cron`, `timezone`) | **Support** | Already the M1 shape; the cache-tier reuse is directly relevant to this note. |
| `workflow_dispatch` (+ typed `inputs`) | **Support** | The trigger-from-CLI path; see §6. |
| `workflow_run` | **Tail** | Chains on another workflow's outcome; `needs` covers the real cases. |
| `issues`, `issue_comment`, `release`, `pull_request_review`, `pull_request_review_comment`, `label`, `milestone`, `deployment`, `deployment_status`, `page_build`, `gollum`, `fork`, `public`, `create`, `delete`, `check_run`, `check_suite`, `merge_group`, `branch_protection_rule`, `discussion`, `discussion_comment`, `registry_package`, `repository_dispatch`, `image_version` | **Tail** | Forge bookkeeping. pluto's M4 events (push, PR, issue, generic webhook) already cover the useful subset at the box level. |

`branches`/`tags`/`paths` filters use GitHub's glob dialect with `!` negation and ordered
evaluation: "A matching negative pattern (prefixed with `!`) after a positive match will
exclude the Git ref." [docs] [workflow syntax]. `paths` is diff-based and has documented
edge behaviour: "If a push contains more than 1,000 commits, the workflow will **always**
run. If generating the diff times out, the workflow will **always** run. If the generated
diff contains more than 3,000 files and the files the workflow filter matches are not in the
first 3,000 returned by the filter, the workflow will **not** run." [docs]

**[inference]** Reimplement the glob filter (it is small); for `paths`, implement two-dot and
three-dot diffs over the repo's own refs and keep the same three edge rules verbatim, because
they are observable behaviour a repo may depend on. Cost: a day. Do not implement the other
20 event keys.

Note `pull_request`'s merge-branch behaviour, because a subset that gets it wrong produces
confusing test results: "`GITHUB_REF` is set to `refs/pull/PULL_REQUEST_NUMBER/merge`" and
"`GITHUB_SHA` is the SHA of the merge commit on the merge branch". [docs] Gitea could not
copy this — "Gitea has no such reference. Therefore, the `ref` in Gitea Actions is
`refs/pull/:prNumber/head`, which points to the head of pull request rather than the preview
of the merge commit." [docs] Gitea's FAQ is the honest precedent: pluto checks out the head
and says so.

### 2.2 `jobs` — job ids, `needs`, `if`, `runs-on` mapped to a box

`needs` is the only graph primitive a normal repo uses. "All jobs listed here must complete
successfully before this job is considered for execution." [docs] (via the Gitea reference,
which quotes the same semantics). `if` uses the standard expression language.

`runs-on` needs a decision, not an implementation: it is a *runner label* on GitHub, and on
Gitea/Forgejo a label selects the **image** the job runs in: "each label of a runner names an
environment, like `my_custom_label:docker://centos:7`". [docs] Gitea design doc. pluto's
analogue is already there: a box is the runner, and `runs-on` should select which **substrate
shape** (the runner seam from #119) and which **environment layer** the ephemeral box boots
from. Do not parse `ubuntu-latest` as anything other than "the default box"; support
`runs-on: [self-hosted, linux]` as a label set that pluto's own host answers to, since that is
what real self-hosted repos write.

**[inference]** A job maps onto a **task** (durable requested-work identity) and its steps
map onto **runs** — exactly ADR 0012's vocabulary. `needs` maps onto task-to-task ordering.
This is the cheapest possible landing zone for the front-end and it reuses the data model
already built.

### 2.3 `steps` — the part everyone uses

Supported keys: `id`, `name`, `if`, `run`, `working-directory`, `shell` (bash/sh only),
`env`, `continue-on-error`, `timeout-minutes`, `uses`, `with`.

Two behaviours matter and are cheap:

- "Each step runs in its own process in the runner environment and has access to the
  workspace and filesystem. Because steps run in their own process, changes to environment
  variables are not preserved between steps." [docs] So `GITHUB_ENV` / `GITHUB_PATH` /
  `GITHUB_OUTPUT` / `GITHUB_STATE` are the *only* cross-step channels, and they are file
  protocols — pluto already has a guest agent that can host them (M4 research, `guest
  environment`).
- `run` caps at "21,000 characters using the operating system's shell". [docs] Worth
  enforcing so a translation error is legible.

The newer `steps[*].background`, `wait`, `wait-all`, `parallel`, and `cancel` keys [docs,
workflow syntax] are a long-running-services feature inside one job. Tail.

### 2.4 `uses` and `env` — see §3 and below

`env` is trivially supported at all three levels (workflow, job, step) with the documented
merge order. One subtlety worth copying: composite actions do **not** get `INPUT_*` env vars
automatically — "If the action is written using a composite, then it will not automatically
get `INPUT_<VARIABLE_NAME>`." [docs] That is a real divergence in the ecosystem and a
translation that ignores it breaks half the locally-authored actions in a repo.

### 2.5 Artifacts — the one GitHub feature with a clean local equivalent

`upload-artifact`/`download-artifact` are the files-between-jobs mechanism, and the split from
caching is documented and worth quoting because it justifies the local design:

> Use caching when you want to reuse files that don't change often between workflow runs...
> Use artifacts when you want to save files produced by a job to use or view after a workflow
> run has ended, such as built binaries or build logs, or when you want to pass files between
> jobs in a workflow. [docs] (dependency-caching, "Artifacts versus dependency caching")

Constraints worth honouring because they are observable: a job is capped at 500 artifacts
[repo] (`upload-artifact` README, "Within an individual job, there is a limit of 500
artifacts that can be created for that job"), "Artifacts created by upload-artifact@v4 are
immutable" and names must be unique within a run, retention defaults to 90 days [docs], and
**file permissions are not preserved** — "All directories will have `755` and all files will
have `644`" [repo]. That last one bites anyone uploading a binary; copy it rather than being
"nicer".

The newer manifest channel — write `oci://ghcr.io/...@sha256:...` to `$GITHUB_ARTIFACTS` and
read the aggregated JSON from `$GITHUB_ARTIFACTS_LIST` [docs] (workflow commands) — is
interesting: it treats an image reference as an artifact. pluto's environment layers are
OCI-shaped, so this is a natural fit. Version-gated on GitHub, so it is Tail at first, but
it is the *shape* pluto should adopt for layer-as-output.

---

## 3. `uses:` — how third-party actions are handled

This is the decision with the largest consequences, so it gets the most evidence. There are
three *implementation* choices on the table (container images, an act-style JS runner, a
curated allowlist) and they are not mutually exclusive — they are tiers of the same ladder.

### 3.1 What the reference target actually is

An action's metadata file is `action.yml`, and GitHub states plainly: "You can build Docker
container, JavaScript, and composite actions." [docs] (metadata syntax). Three kinds:

| Kind | Metadata | Execution |
|---|---|---|
| Docker | `using: docker`, `image` (a `Dockerfile` or `docker://ref`), `args`, `entrypoint`, `pre-entrypoint`, `post-entrypoint`, `env` | `docker run` of that image |
| JavaScript | `using: node20` \| `node24`, `main`, `pre`, `post`, `pre-if`, `post-if` | Node executing the bundle on the runner host |
| Composite | `using: composite`, `steps` (run or uses) | Inlined into the job as steps |

Inputs arrive as env: "When you specify an input, GitHub creates an environment variable for
the input with the name `INPUT_<VARIABLE_NAME>`." [docs] Outputs are declared in metadata and
set via the `GITHUB_OUTPUT` file; "Outputs can be a maximum of 1 MB per job. The total of all
outputs in a workflow run can be a maximum of 50 MB." [docs]

**The Docker kind has a hard platform requirement.** "Docker container actions can only
execute on runners with a Linux operating system. Self-hosted runners must use a Linux
operating system and have Docker installed to run Docker container actions." [docs] (About
custom actions). That requirement is inherited from GitHub's runner, not from Docker-in-Docker
availability — and it is the single reason a container-action tier is *cheap* for pluto: pluto
runs a substrate seam (#119) and the container backend is already a substrate option. It is
also the reason the JS tier is not free: it needs a Node runtime on `PATH` inside the box.

### 3.2 What each implementation option costs

**Option A — container actions (`docker://` and `using: docker`).**
The reference syntax is fully documented, including a ghcr example that matters directly for
§4: `uses: docker://ghcr.io/OWNER/IMAGE_NAME` [docs] (workflow syntax, "Using the GitHub
Container registry"). Local Docker actions have exactly one extra requirement — "To reference
a `Dockerfile` local to your repository, the file must be named `Dockerfile` and you must use
a path relative to your action metadata file." [docs]

Cost: pull an image, run it with `args` as `CMD`-equivalent and `env` set. This is what
Woodpecker does for *every* step, and what act does for every step: "It uses the Docker API to
either pull or build the necessary images, as defined in your workflow files... then uses the
Docker API to run containers for each action based on the images prepared earlier." [docs]
(act README)

**Option B — an act-style JS runner.**
This means: clone the action repo at a ref, put its bundle on `PATH`, and run Node with the
`GITHUB_*` env and files faked. act does exactly this and documents the cost honestly:

> `PATH` of container / of act must contain node for nodejs actions, github runner has their
> own copy for both container and host. [docs] (act, Unsupported functionality)

and it changes the default runner images to carry Node: `ubuntu-latest` runs on
`node:16-buster-slim` in the "micro" size, and act's own installer asks the user which of
three sizes to use, describing micro as "<200MB, contains only NodeJS required to bootstrap
actions, doesn't work with all actions." [docs] (act, Runners / default image survey)

Gitea inherited this whole design and documents the residual: "Many actions require node to
run. Using a custom container image that does not contain node may cause these actions to
break." [docs] (Forgejo reference, `jobs.<job_id>.container.image`)

**[inference]** Option B is the *expensive* option at pluto's scale, and the expense is not
the runner. It is that the Node runtime must live inside every ephemeral box, so the "warm
base" has to carry Node, so every image and every layer gets larger, and the substrate has to
allow executing untrusted-ish third-party JS with the job's credentials in `env`. The
actionable cost is on the warm base — the exact thing the moonshot optimises.

**Option C — a curated allowlist.**
Gitea's `builtin:` prefix is the precedent: "`builtin:` runs an action built into Gitea
Runner, which needs no download and no Node in the job image, e.g.
`uses: builtin:checkout`. `builtin:checkout` accepts the `repository`, `ref`, `token`, `path`
and `fetch-depth` inputs of `actions/checkout` with the same defaults, and fails on any other
input." [docs] (Gitea, Compared to GitHub Actions)

That is the design worth copying: a tiny set of first-class actions, implemented natively,
each accepting a *documented, bounded* input set and failing loudly outside it. It is cheap
(no Node, no download, no marketplace), it is honest (the input set is the contract), and it
degrades in a way the user can see.

### 3.3 What the marketplace actually gives you, and what it costs

The Marketplace is a discovery surface over git repos. Three reference forms exist for
pointing at one:

- `{owner}/{repo}@{ref}` — a public GitHub repository at a branch, tag, or SHA.
- `{owner}/{repo}/{path}@{ref}` — a subdirectory of one.
- `$/path/to/action` — "the self repository reference... resolves to that repository at the
  running commit", recommended over `./` because `./` "resolves against the runner's checked-out
  workspace". [docs]

And GitHub is explicit about version-safety: "Using the commit SHA of a released action
version is the safest for stability and security." [docs] The container-and-microvm-hardening
note already reached the same conclusion for the moonshot — pin to SHAs, because tags are
mutable — and concluded supply chain is the dominant realistic risk at this scale.

So the marketplace buys **discovery and versioning**, not execution. Execution is either a
container or a JS bundle. pluto can get the versioning benefit without the discovery surface.

**Recommendation: a two-tier `uses`, allowlisted in the middle.**

1. **Tier 1 — native (implemented in pluto).** `checkout`, plus the setup actions a repo
   actually uses (`setup-node`, `setup-python`, `setup-go`, `setup-java`) — Gitea's
   `builtin:` pattern with a bounded input set each. These are the actions that appear in
   nearly every real workflow, and each one is a download-and-cache step whose real work pluto
   already does for the warm base. Emit a clear error on unknown inputs.
2. **Tier 2 — container actions (`docker://`), digest-pinned.** Direct map to the substrate.
   Real, unbounded coverage: anything published as an image works, including
   `docker://ghcr.io/OWNER/IMAGE_NAME`. This is where third-party actions land, and it needs
   no Node in the box.
3. **Tier 3 — Node actions: opt-in, per-action, with a trust declaration.** A workflow that
   uses a JS action must name the action in the repo's pluto config with a pinned SHA and an
   explicit acknowledgement. Only then does pluto provision the box with Node and run the
   bundle. This is the trust gate the moonshot's DX section already calls for, applied
   specifically to the JS tier.

**Not supported:** the Marketplace as a surface (no index, no verified badges, no ratings);
`owner/repo` JS actions resolved silently at run time; mutable refs (require a resolved SHA
at import time and store it).

**Why not allow everything?** Because "trusted code" in the moonshot means trusted by the
person who owns the hardware. A third-party JS action is code those people did not review,
running with their credentials in `env`. Every self-hosted CI that opened this door ended up
gating it: Gitea "always run jobs with isolated containers. While it is possible to do this
directly on the host, we choose not to for more security." [docs] (Gitea FAQ) and Woodpecker
gates `privileged` behind "Trusted mode" in repo settings [docs] (project settings). Both
converged on the same line: untrusted-by-default for third-party code, with an explicit
per-repo opt-in.

### 3.4 The secrets problem this creates

An action that needs a credential reads it from `INPUT_*` or `${{ secrets.X }}`. pluto has no
GitHub secrets store; the M4 trust note already flagged that secrets must be masked in job
logs, copying GitHub's automatic masking. **[inference]** The allowlist gives a clean place to
put the rule: a native action declares its credential inputs explicitly, and only those are
masked. A JS action's secret surface is unknowable, which is another argument for the
tier-3 gate rather than a default.

---

## 4. Registries and caches — ghcr and the Actions cache

Two different interop questions. ghcr is a standard OCI registry and interop is nearly free.
The Actions cache is a proprietary service with a *documented-on-GitHub* set of semantics and
an *undocumented* HTTP API, which changes what is possible.

### 4.1 ghcr — pull works, push is a workflow-authorisation problem

ghcr is the GitHub Container registry: "The Container registry stores container images within
your organization or personal account, and allows you to associate an image with a repository.
You can choose whether to inherit permissions from a repository, or set granular permissions
independently of a repository. You can also access public container images anonymously." [docs]

Formats are standard: "Docker Image Manifest V2, Schema 2" and OCI. [docs] Limits that matter
to a self-hosted runner: "The Container registry has a 10 GB size limit for each layer. The
Container registry has a 10 minute timeout limit for uploads." [docs]

**What interops cleanly:**

- Pulling public images, including `docker://ghcr.io/OWNER/IMAGE_NAME` as a container-action
  step reference. Anonymous, standard OCI. [docs]
- Digest-pinning anything pluto builds or pulls — "To ensure you're always using the same
  image, you can specify the exact container image version you want to pull by the `digest`
  SHA value." [docs] This is the same discipline the hardening note already demands.
- Treating ghcr as the **shared environment-layer store** for a team. This is the real win and
  it is why the moonshot's "shared environment layers" idea has a ready-made transport.
  `org.opencontainers.image.source` and the "link a package to a repository" flow exist for
  exactly this: "If you publish a package that is linked to a repository, the package
  automatically inherits the access permissions of the linked repository." [docs]

**What does not interop:**

- Pushing with the automatic token, in the general case. "The `GITHUB_TOKEN` will not have
  permission to push the package if you have previously pushed a package to the same
  namespace, but have not connected the package to the repository." [docs] And for anything
  outside the workflow's own repo, a "personal access token (classic) with at least
  `read:packages` scope" is required. [docs] So a self-hosted runner pushing to ghcr from a
  workflow needs a PAT the user supplies — which is fine and honest, but it must be a declared
  secret, not magic.
- Any assumption that ghcr is the cache. It is an image registry, not a key/value object store.

### 4.2 The Actions cache — documented semantics, undocumented API

The semantics are documented and are good. The API is not documented by GitHub at all, but it
is *de facto* public because act implements it. That distinction decides what pluto can build.

**Documented semantics (the parts worth copying):**

- Restore order: "First, it searches for an exact match to your provided `key`. If no exact
  match is found, it will search for partial matches of the `key`. If there is still no match
  found, and you've provided `restore-keys`, these keys will be checked sequentially for
  partial matches." [docs]
- Branch scoping: "The `cache` action first searches for cache hits for `key` and the cache
  _version_ in the branch containing the workflow run. If there is no hit, it searches for
  prefix-matches for `key`... If there are still no hits in the current branch, the `cache`
  action retries the same steps on the default branch." [docs] And the boundary:
  "Access restrictions provide cache isolation and security by creating a logical boundary
  between different branches or tags." [docs]
- Immutability: "You cannot change the contents of an existing cache. Instead, you can create
  a new cache with a new key." [docs]
- Miss behaviour: "When `key` doesn't match an existing cache, this is called a _cache miss_,
  and a new cache is automatically created if the job completes successfully." [docs]
- A cache-version stamp: "Cache version is a way to stamp a cache with metadata of the `path`
  and the compression tool used while creating the cache. This ensures that the consuming
  workflow run uniquely matches a cache it can actually decompress and use." [docs] — this is
  the trick that keeps stale-format caches from silently poisoning a run.
- Eviction: "{% data variables.product.github %} will remove any cache entries that have not
  been accessed in over 7 days. There is no limit on the number of caches you can store, but
  the total size of all caches in a repository is limited. By default, the limit is 10 GB per
  repository." [docs] And the order: "the cache eviction policy will create space by deleting
  the caches in order of last access date, from oldest to most recent." [docs]
- Rate limits: "200 cache entry uploads per minute" and "1500 cache entry downloads per
  minute" per repository. [docs]

**The cache-poisoning restriction is the most interesting thing on the page**, because it is a
security property pluto should copy rather than rediscover:

> To reduce this risk, only these workflow triggers can create or overwrite caches in the
> default branch's scope: `push`, `workflow_dispatch`, `repository_dispatch`, `delete`,
> `registry_package`, `page_build`, `schedule`. Runs triggered by any other event that
> resolves to the default branch are given read-only access to caches in the default branch's
> scope. [docs]

And the failure mode is deliberately soft: "When a run with read-only cache access tries to
save a cache, the save fails but the step and the job do not. The workflow continues, and the
failure is reported as a warning in the workflow log." [docs] That is a good default: a cache
write is an optimisation, not a correctness requirement.

Newer GitHub has made this explicit as a first-class key (`cache-mode`: `read`, `write`,
`write-only`, `none`, with "a skipped save is simply not performed") [docs] — which is the
right shape for pluto too, since a cache-tier/cache ownership decision (#121) is coming.

**The API is undocumented but reverse-engineerable, and act proves it.** From the
`nektos/act` source: it starts an in-process HTTP handler at `/_apis/artifactcache` with
routes `GET /cache`, `POST /caches`, `PATCH /caches/:id`, `POST /caches/:id`,
`GET /artifacts/:id`, `POST /clean`, backed by a BoltDB store, and sets
`ACTIONS_CACHE_URL` to point the actions at it. [repo] (`pkg/artifactcache/handler.go`,
`cmd/root.go`) The request/response shapes, the `Accept: application/json;api-version=
6.0-preview.1` header, and the `ACTIONS_RUNTIME_TOKEN` bearer auth all appear in the
`actions/cache` bundle: `getCacheApiUrl` builds `${baseUrl}_apis/artifactcache/${resource}`
and `createHttpClient` reads `process.env['ACTIONS_RUNTIME_TOKEN']`. [repo]
(`actions/cache` `dist/restore/index.js`)

**[inference]** This is the single most useful technical finding in the note. The GHA cache
*protocol* is small, stable, and already reimplemented by a third party. That means pluto can
run **unmodified `actions/cache`** against its own cache service. But note the service
*version* split: `getCacheServiceURL()` returns `ACTIONS_CACHE_URL` or `ACTIONS_RESULTS_URL`
for `v1`, and only `ACTIONS_RESULTS_URL` for `v2`, selected by `ACTIONS_CACHE_SERVICE_V2`;
`v2` is forced off on GHES. [repo] pluto should implement v1 and treat v2 as the version to
watch, because v1 is what act and the installed base of actions speak.

**The one place the stock actions refuse to play: they check the server host.** Both
`actions/cache` and `@actions/artifact` compute `isGhes()` from `GITHUB_SERVER_URL` and refuse
if the host is not `github.com`, `*.ghe.com`, or `*.localhost`. [repo] (`actions/cache`
`dist/restore/index.js` `actionUtils_isGhes`; `actions/toolkit`
`packages/artifact/src/internal/shared/config.ts`) The consequence, in Gitea's words:

> Those actions refuse any host they do not take for GitHub. Rather than misreport the server
> URL, the runner edits that check out of the action's own JavaScript bundle and keeps the
> untouched copy beside it; a bundle it does not recognise is left alone and keeps to v1. The
> same edit lets the stock `actions/upload-artifact` and `actions/download-artifact` work
> from `v4.4.0` on, without the `gitea-upload-artifact` fork. [docs] (Gitea, Caching)

**This is the sharpest lesson in the whole comparison and it is a warning, not a template.**
Gitea makes stock `actions/upload-artifact` work by monkey-patching a minified bundle inside
the action's own `dist/`. That is a permanent maintenance tax on an opaque artifact — the
patcher must recognise each bundle version, and an unrecognised one silently keeps v1
behaviour. **[inference]** pluto should not copy this. It is exactly the "permanent tax" the
moonshot names. The alternative is pluto's own native artifact steps (§2.5) plus the
$GITHUB_ARTIFACTS channel, and letting `upload-artifact@v4` be unsupported with a clear
error, rather than emulating it by patching bundles.

**Using your own machines as the cache.** Gitea's answer is the model and its own docs state
the limitation of the default: "Every runner starts its own cache server, so `actions/cache`
works without any configuration. Cache entries are local to that runner: two runners do not
share a cache unless you make them." [docs] The three documented sharing options are: one
dedicated cache server every runner points at (with a shared `external_secret`); a shared
filesystem (NFS/CIFS) at `cache.dir`; or object storage (S3/MinIO) mounted as a FUSE
filesystem. [docs] For a handful of machines that is the whole menu, and it maps directly onto
the moonshot's "shared store is opt-in" line and onto #121's cache-ownership question. See §9.

---

## 5. Prior art — what each supports, where it bled, what it cut

### 5.1 act — "Run your GitHub Actions locally"

**Supports:** the whole workflow grammar as an input format, Docker and Node actions, the
cache service, the artifact service. It is the closest thing to a reference implementation of
"read a workflow and run it somewhere else".

**Where it bled — its own list, which is a ready-made parity-tail inventory.** act maintains a
documented "Unsupported functionality" page, and it is the most useful artifact in this whole
comparison because it is a maintainer saying *these are not worth doing*:

> `concurrency` is ignored · `run-name` is ignored · Step summary not processed · Problem
> matcher ignored · Annotations ignored · Incomplete `github` context · Run steps cancellation
> not implemented · `job.permissions` ignored · `job.timeout-minutes` ignored ·
> `job.continue-on-error` ignored · `PATH` of container / of act must contain node for nodejs
> actions... · Openid Connect url is not defined · `job.environment` ignored and scoping
> secrets by deployment environment is not supported
>
> Functionality that is not going to be worked on: Docker context. [docs]

**[inference]** Every item on that list is either forge-specific (permissions, environments,
OIDC) or control-plane-shaped (concurrency, cancellation, annotations). None of them is a
grammar feature. That is strong evidence the cut line belongs where §8 puts it. It also tells
you what act *does* implement that is invisible from the outside: the cache service, the
artifact service, and the expression interpreter including `hashFiles` — `hashFiles` is
implemented in act's interpreter [repo] (`pkg/exprparser/functions.go`), contradicting
community tutorials that claim it is unsupported on the Gitea line.

**The cost it pays, in one paragraph from its own docs:**

> These default images do **not** contain **all** the tools that GitHub Actions offers by
> default in their runners. Many things can work improperly or not at all while running those
> image. Additionally, some software might still not work even if installed properly, since
> GitHub Actions are running in fully virtualized machines while `act` is using Docker
> containers (e.g. Docker does not support running `systemd`). [docs]

**[inference]** That is the crux for pluto. act runs workflows in containers because the
workflow grammar assumes a Linux runner host. pluto's substrate decision (#119) determines
whether this same gap applies. If pluto goes microVM-first, the Node-on-PATH requirement for
JS actions becomes a guest-image requirement — a real cost on the warm base, and a second
argument for tiering `uses` as §3 recommends rather than making JS actions the default path.

### 5.2 Gitea Actions — the fork-of-act that is now a product

**Supports:** the workflow grammar; workflows read from both `.gitea/workflows` and
`.github/workflows` ("`WORKFLOW_DIRS`: `.gitea/workflows,.github/workflows`", the first one
that exists wins) [docs]; runner label→environment mapping (`label:docker://image` or
`:host`); `workflow_dispatch`, `workflow_call`, schedule with IANA timezones, and a long list
of issue/PR/release events. Its runner "is based on a hard fork of `nektos/act`". [docs]

Its own comparison page is a precise account of the tail:

> `jobs.<job_id>.environment` is ignored. · Complex `runs-on`: supports static strings, arrays
> of labels, string expressions, arrays containing expressions. · `GITEA_TOKEN` cannot publish
> to the package registry of its repository, e.g. to push OCI images. · Problem Matchers are
> ignored. · Error annotations are ignored. · Pre and post steps have no section of their own
> in the job log. · Service steps have no section of their own in the job log. [docs]

And it adds things GitHub does not have, which is instructive — a fork is free to improve:

> `uses:` accepts an absolute URL to any git host... `self:` references your own Gitea
> instance... `builtin:` runs an action built into Gitea Runner, which needs no download and
> no Node in the job image... `$/` references the repository and commit of the workflow...
> Actions written in Go... `schedule` also accepts `@yearly`, `@monthly`, `@weekly`,
> `@daily` and `@hourly`, which GitHub does not. [docs]

**Where it bled:** the bundle-patching described in §4.2 to make stock artifact actions work,
and the cache-local-by-default behaviour. Both are documented costs of trying to be
binary-compatible with an implementation whose internals you do not control.

**[inference]** Gitea's `builtin:` prefix and `$/` self-reference are the two ideas to steal.
`builtin:` because it is the shape of a first-class action with a bounded contract (§3.3), and
`$/` because it removes the worst ordering trap in the grammar — `./` needs a checkout first,
`$/` does not. pluto should accept both spellings as *aliases* in its own dialect, since a
workflow that says `uses: builtin:checkout` is a strong signal the author was already thinking
about a non-GitHub runner.

### 5.3 Forgejo Actions — same lineage, different call

Same act fork, so the same compatibility envelope, with its own explicit hedge:

> The syntax and semantics of the YAML file describing a `workflow` are *partially* explained
> here. If something is not documented here, the GitHub Actions documentation may be helpful.
> However, GitHub Actions and Forgejo Actions *are not the same* and things might not work
> right away. [docs]

Three details are directly relevant to pluto:

- Container images are never refreshed: "When starting a container, Forgejo Runner does the
  equivalent of `docker run <image>`. That means that container images, once downloaded, are
  **never updated**." [docs] pluto's environment-layer content-addressing must decide this
  deliberately, because "warm base" invites staleness.
- It recommends digest pinning as a reproducibility prerequisite: "It is generally recommended
  to pin image versions instead of relying on tags, for example `debian:
  docker://node@sha256:...` instead of `debian:docker://node:lts`. This is a prerequisite for
  reproducible jobs." [docs] Consistent with the hardening note.
- Its timezone-aware schedule has a documented DST divergence from GitHub: "Workflows that are
  scheduled to run when the clock jumps forward **are skipped**. When the clock moves backward,
  workflows **run twice**." [docs] A caution for pluto's own schedule-timezone support, and a
  reminder that DST is where every scheduler bleeds (cf. `trigger-comparables.md` §5).

Also notable: `concurrency` is best-effort only — "Forgejo makes a **best-effort** to ensure
that only a single workflow with the same concurrency group is executed at one time... in
exceptional circumstances this cannot be guaranteed." [docs] At pluto's scale, honest
best-effort is the correct answer and worth stating plainly in docs rather than over-promising.

### 5.4 Woodpecker — the one that cut the actions ecosystem entirely

**Supports:** `steps` with `image` + `commands`, `services`, `workspace`, `matrix`, `labels`
for agent selection, `when` conditions (event/branch/repo/status/platform/matrix/path/ref/
evaluate), `depends_on` DAGs, `variables` (YAML anchors), `clone`/`skip_clone`, cron, secrets,
`pull`.

**What it cut:** `uses:` and the marketplace. A Woodpecker step is *either* a container with
commands *or* a plugin image — there is no third-party action resolution at all. This is the
cleanest possible answer to "how do you handle third-party actions" and it is a legitimate
product position: zero supply-chain surface, zero Node in the box, nothing to patch.

**What it costs:** you inherit nothing. Every repo must translate its workflows into the
Woodpecker dialect. Woodpecker knows this, which is why it ships a migrations page and a
linter.

**What it gets right and pluto should copy:**

- Local execution is a first-class feature, not a flag: `woodpecker-cli exec
  .woodpecker/my-first-workflow.yaml`, with `--backend-engine docker|local`, explicit metadata
  (`--pipeline-event push --commit-branch main --commit-sha "$(git rev-parse HEAD)" --repo
  octocat/hello-world`), `--env`, and `--secrets-file`. [docs] The local backend "runs
  commands directly on your host and does not reproduce the container image environment" —
  stated plainly, including the limitation. And the warning on the metadata replay file is the
  right instinct: "The metadata file is not a stable, portable API." [docs]
- The plugin model is the same bounded-contract idea as Gitea's `builtin:`, with a registry of
  its own (`woodpeckerci/plugin-*`).
- Privileged is gated: "Privileged mode is only available to trusted repositories and for
  security reasons should only be used in private environments. See project settings to
  enable trusted mode." [docs]

**[inference]** Woodpecker is the proof that a workflow front-end without `uses:` can be a
good product. The reason it is still the wrong *primary* answer for pluto is the moonshot's
stated position: "workflow YAML as a front-end" over existing repos. Reading what is already
in `.github/workflows` is the whole point, and workflows in the wild are full of
`actions/checkout@v4`. Woodpecker's answer to that is "translate your workflows", which is a
migration cost the moonshot explicitly wants to avoid paying for the common subset. But its
`uses:`-free design is exactly right for the *tail*: the actions pluto will not support are
the ones Woodpecker never had.

### 5.5 Concourse — the one that replaced `uses` with resources

**Supports:** a task is "a function from `task-config.inputs` to `task-config.outputs` that can
either succeed or fail" [docs]; `platform`, `image_resource`, `inputs`, `outputs`, `caches` (a
path per cache), `params`, `run`; jobs with `serial`, `serial_groups`, `max_in_flight`,
`build_log_retention`, `public`, `disable_manual_trigger`, `disable_reruns`,
`interruptible`, `tags`, and five job hooks (`on_success`, `on_failure`, `on_error`,
`on_abort`, `ensure`). Resources are the extension point, with a whole resource-types
mechanism.

**What it cut:** the action model. There is no `uses:` and no marketplace; reuse happens
through `resource` types and `fly execute` for local runs.

**The detail most worth stealing is a security decision, stated as a design principle:**

> Another relevant bit of configuration is `task` step `privileged`, which determines whether
> the user the task runs as will have full privileges (primarily when running as `root`).
> This is intentionally *not* configurable by the task itself, to prevent privilege escalation
> by submitting pull requests to repositories that contain task configs. [docs]

**[inference]** That is precisely the rule pluto needs, and it settles a question the
allowlist raises. Privilege is a property of the *host owner's* configuration, never of the
workflow file. A workflow cannot ask for a privileged box; only the repo's pluto config can.
Copy the principle and put it in the subset spec as a rule, not a preference.

**What it gets right for the trigger question:** `fly execute` runs a task from a local
checkout with the same metadata-override flags as Woodpecker, plus `inputs-from`, `--image`,
`--output`, `--tag`, and `-p/--privileged`. [docs] Also `fly trigger-job`, `fly rerun-build`,
`fly pause-job`, and `fly clear-task-cache` — note that clearing the task cache is an
explicit, named operation, which is what a cache-ownership design (#121) will need.

### 5.6 The four compared

| | act | Gitea/Forgejo | Woodpecker | Concourse |
|---|---|---|---|---|
| Input grammar | GitHub Actions YAML | GitHub Actions YAML | own YAML (`.woodpecker/`) | own YAML (pipeline) |
| Third-party actions | yes (`uses:`) | yes (`uses:` + `builtin:`/`self:`/`$/`) | **no** | **no** (resources instead) |
| Runtime | container + Node on PATH | container + Node (act fork) | container or host shell | container |
| Cache | own service, local dir | own service per runner, optional shared | none built in | per-task path cache on workers |
| Local run | `act` | (runner is act) | `woodpecker-cli exec` | `fly execute` |
| Documented "won't do" | yes, a whole page | comparison page | by omission | by design |

**[inference]** The pattern across all four: everyone who reads GitHub workflow files
reimplements the operational interfaces (cache, artifacts, expressions), and everyone who does
not read them cuts the problem at the input grammar. Nobody attempts full parity, and the
three public "unsupported" lists are almost the same list. That is the strongest available
evidence that the parity tail is real, bounded, and not worth climbing.

---

## 6. Trigger-from-CLI vs forge-triggered runs

The moonshot asks for "trigger from the CLI, not only from a forge". GitHub's own answer to
this is `workflow_dispatch`, and reading its exact semantics is what makes the difference
between a CLI trigger and a forge trigger legible.

**`workflow_dispatch` is the CLI-shaped event.** "When using the `workflow_dispatch` event, you
can optionally specify inputs that are passed to the workflow. This trigger only receives
events when the workflow file is on the default branch." [docs] Invoked with
`gh workflow run WORKFLOW --ref BRANCH`, with `-f`/`-F` for inputs or `--json` on stdin. [docs]
Inputs are typed — `boolean`, `choice`, `number`, `environment`, `string` — with caps: "The
maximum number of top-level properties for `inputs` is 25" and "The maximum payload for
`inputs` is 65,535 characters." [docs]

So a CLI trigger on GitHub is *structurally identical* to a forge trigger, with three
differences: (a) the actor is a person rather than an event, (b) there is no webhook payload,
and (c) the workflow definition comes from the default branch, while `--ref` selects what is
checked out. That is why the docs say the input lands in `inputs` and also in
`github.event.inputs`, with the note that they "identical except that the `inputs` context
preserves Boolean values as Booleans". [docs]

**This is the same split pluto already has.** `pluto run <name>` is the manual path, an event
from a forge is the other path, and a schedule is the third — the glossary defines exactly
this: a **trigger** is "Anything that wakes a box to do work: a manual `run`, a schedule, and
later events such as git pushes or webhooks", and an **event** is "a repository-sourced
trigger... It is a kind of trigger; 'webhook' names only the inbound transport, not the work."
[GLOSSARY.md]

**The differences that matter for a workflow front-end:**

1. **Which workflow file the definition comes from.** Forge-triggered runs must read the
   workflow from the *pushed commit* (that is what makes the run reproducible and what the
   M4 trusted-default-branch policy already gates). A CLI run on a working checkout should use
   the checkout's file, exactly as act does. These are different sources and a design that
   conflates them is the classic "works on my machine" bug. Woodpecker makes this explicit and
   gives the user the flag: `--metadata-file` plus overrides, with the warning that the replay
   format "is not a stable, portable API". [docs]
2. **The `github` context degrades honestly.** act lists "Incomplete `github` context" as a
   known gap [docs] and ignores `job.permissions`, `run-name`, and `job.environment`. pluto
   should populate what it can (`github.sha`, `github.ref`, `github.event_name`,
   `github.actor`, `github.workflow`, `github.run_id`) and leave the rest empty rather than
   inventing values. Gitea took the interesting route of providing *both* spellings — "You can
   use `github.xyz` and Gitea will work fine" while recommending `gitea.xyz` [docs] (FAQ) —
   which is worth copying: accept `github.*` because that is what workflows say, and expose a
   `pluto.*` alias for the fields that are pluto's own.
3. **A CLI-triggered run needs an identity for its inputs.** ADR 0012 already gives it: the
   task is the durable identity and the run is one attempt under it. A `workflow_dispatch`
   with inputs is a task with a parameterised job; `pluto run` on a workflow is a
   `task create` in ADR 0012 terms. **[inference]** This is the cheapest place to land the
   front-end: no new vocabulary, no new store shape.
4. **Dry-run/plan is a first-class feature, not a flag.** Woodpecker's `--metadata-file`
   replay and `fly execute` both exist because the local case is the debugging case. pluto
   should expose `pluto workflow run <file> --job <id>` and a plan that prints the translated
   DAG (box per job, ordering, cache tiers, resolved `uses` SHAs) without running anything.
   The devex note's core claim — the failure path is the product — applies directly: the
   failure mode of a translation layer is a silently mistranslated workflow.

**One trap worth naming.** `workflow_dispatch` reads the workflow from the default branch,
while `--ref` chooses what to check out. A user who edits a workflow on a branch and runs it
from the CLI on GitHub gets the *default branch's* version of that workflow. pluto's CLI path
should do the opposite and use the local file, because that is what "trigger from the CLI"
means for a tool whose workflow files live in the working tree — and it should say which file
it used, in the run record.

---

## 7. Supported-subset spec

The rules, in order. A translation that violates any of them is a bug, not a judgement call.

### 7.1 What is read

Workflow files are discovered in `.github/workflows/*.yml` and `*.yaml`, plus `.pluto/
workflows/` as pluto's own home. A file larger than 500 KB is rejected before parsing, matching
GitHub's "Each workflow file in the `.github/workflows` directory must be 500 KB or smaller to
trigger a run." [docs] The workflow `name` defaults to the file path relative to the repo
root, as on GitHub.

### 7.2 The supported subset (normative)

**Top level:** `name`, `run-name` (evaluated, or derived from the event as GitHub does), `env`,
`defaults.run.{shell,working-directory}`, `on`, `jobs`. Unknown top-level keys are an error
with the key named, never a silent ignore — Gitea's `environment` case shows how silent
ignoring reads to the user as a bug that only shows up in production.

**`on`:** `push`, `pull_request`, `pull_request_target`, `schedule`, `workflow_dispatch`.
Filters: `branches`, `branches-ignore`, `tags`, `tags-ignore`, `paths`, `paths-ignore`,
`types`. Cron with five POSIX fields and an optional IANA `timezone`. `schedule` fires the
workflow at the latest commit of the default branch.

- The intervals GitHub itself allows are the floor: "The shortest interval you can run
  scheduled workflows is once every 5 minutes." [docs]
- Missed-occurrence policy is inherited, not reinvented: the existing scheduler's
  "missed-occurrence coalescing... producing exactly one late run" (`m4-queue-and-hooks.md`
  §1.2) is the behaviour, and it is also what systemd and anacron do
  (`trigger-comparables.md` §6 "What the comparables agree on" item 4).

**`jobs.<job_id>`:** `name`, `needs`, `if`, `runs-on`, `env`, `defaults.run`,
`timeout-minutes`, `continue-on-error`, `strategy.matrix`, `outputs`, `steps`.
`runs-on` accepts a string or label array and resolves to a substrate shape plus an
environment layer from the repo's pluto config; `ubuntu-latest` and `self-hosted` both mean
"the default box" and are not distinguished.

- `needs` is all-must-succeed and admits a string or a list. The graph must be acyclic and
  unknown ids are an error. Matrix fan-out is capped at 256 jobs per run, matching GitHub's
  "A matrix will generate a maximum of 256 jobs per workflow run. This limit applies to both
  GitHub-hosted and self-hosted runners." [docs]
- `timeout-minutes` is enforced per job (pluto already has a job timebox) and step
  `timeout-minutes` per step.

**`jobs.<job_id>.steps[*]`:** `id`, `name`, `if`, `run`, `working-directory`, `shell`
(`bash`/`sh`; `pwsh`/`powershell`/`cmd` are rejected as unsupported shells), `env`,
`continue-on-error`, `timeout-minutes`, `uses`, `with`. The `GITHUB_ENV`, `GITHUB_PATH`,
`GITHUB_OUTPUT`, `GITHUB_STATE`, and `GITHUB_STEP_SUMMARY` files are provided by the guest
agent, because they are the only cross-step channel.

- `run` longer than 21,000 characters is rejected. [docs]
- A step with both `run` and `uses` is an error.
- Steps with no `id` are addressable by ordinal, matching the runner.

**`uses`** — the three tiers of §3, with resolution to a digest at import time:

| Form | Tier | Behaviour |
|---|---|---|
| `builtin:checkout` (or pluto's own `pluto:checkout`) | 1 native | Bounded input set; unknown input is an error. |
| `docker://ghcr.io/OWNER/IMAGE_NAME` | 2 container | Digest-pinned; requires the container substrate. |
| `docker://alpine:3.8` | 2 container | Same. |
| `owner/repo@v4` where the action is a *composite* | 2 | Resolved to a SHA, inlined; `INPUT_*` not set automatically. |
| `owner/repo@v4` where the action is a *Docker* action | 2 | Resolved to a SHA, run as a container. |
| `owner/repo@v4` where the action is a *JavaScript* action | 3 | Requires an explicit allowlist entry plus a pinned SHA; provisions Node in the box. |
| `./path/to/action` | 2 | Local; requires a prior checkout step. |
| `$/path/to/action` | 2 | Self-repo at the running commit; accepted as an alias, needs no checkout. |
| a bare owner/repo at a mutable ref with no allowlist entry | — | Error: `uses: x/y@main — resolve to a digest first (pluto actions pin x/y@main)` |

**`env`** at workflow, job, and step level, with the documented merge order and the composite
divergence honoured.

**Expressions:** the 15 standard functions and the 14 standard contexts, with `github`
populated for the fields pluto can fill and left empty otherwise. `hashFiles` is implemented
(it is in act's interpreter [repo] and it is what cache keys are built from). The `secrets`
context resolves against pluto's secret store, not GitHub's.

### 7.3 The rules that are not negotiable

1. **Privilege is never a property of the workflow.** Concourse's principle verbatim: it is
   "intentionally *not* configurable by the task itself, to prevent privilege escalation by
   submitting pull requests to repositories that contain task configs." [docs] A workflow
   cannot request a privileged box, extra mounts, or a host path.
2. **A resolved digest is stored with the run record.** Every `uses` in a run is recorded as
   the SHA it actually executed, so a re-run is reproducible and so "what ran" is answerable
   after the fact.
3. **Cache writes are optimisations.** A skipped or failed save does not fail the job, exactly
   as GitHub's read-only-trigger behaviour works. [docs]
4. **The translation is visible.** `pluto workflow plan` prints the DAG and the resolved
   actions before anything runs.
5. **Untrusted external CI is out of scope** per the moonshot; the trusted-default-branch
   policy from `m4-event-delivery-and-trust.md` §3 stays the gate for forge-triggered runs.

---

## 8. The parity-tail boundary

Said plainly, as the ticket asks. **Not supported, and will not be:**

**Forge-only control surface.**
`permissions` and `GITHUB_TOKEN` scoping (there is no GitHub token to scope; M4 already
concluded "`GITHUB_TOKEN` (Actions-only): Not applicable — Pluto is not GitHub Actions");
`jobs.<job_id>.environment`, protection rules, wait timers, `environment` secrets; OIDC
(`id-token`, `ACTIONS_ID_TOKEN_REQUEST_URL`) — pluto has no cloud federation target at this
scale; `concurrency` groups with cancellation (Forgejo's own best-effort is the honest
ceiling); the Marketplace as a surface — no index, no verified badges, no ratings.

**Cross-repo composition.**
Reusable workflows: `on.workflow_call`, `jobs.<job_id>.uses` pointing at a workflow file,
`secrets: inherit`, and the nesting rules — "You can connect a maximum of ten levels of
workflows" on GitHub.com [docs]. This is where the permanent tax starts: it needs a second
fetcher, a second trust decision, a second secrets model, and a nesting limit to police.
Composite actions and `$/`/`./` cover everything a small team actually needs.

**Operational interfaces pluto will not emulate.**
The step-summary channel beyond a plain text dump; problem matchers and annotations (act and
Gitea both ignore them [docs]); the artifact-v2 results API as a compatibility target — the
`isGhes()` bundle check is the reason (§4.2); and `GITHUB_ARTIFACTS`/`GITHUB_ARTIFACTS_LIST`
initially (Tail, revisited when the layer-as-output design lands).

**Runner-host features with no small-fleet analogue.**
`services` and job-level `container` (they need per-job network namespaces, health checks, and
volume plumbing — revisit when #119 picks the substrate; `services` is the most likely of
these to earn its way back, because postgres/redis in tests is common); `container.volumes`
and `container.options` (host-dependent `docker run` flags, and the Concourse rule forbids
them anyway); macOS/Windows runners and the Windows-only shells; `steps[*].background`/`wait`/
`wait-all`/`parallel`/`cancel`; `strategy.fail-fast` and `max-parallel` beyond a boolean and
a clamp to fleet size.

**Forge events beyond the five.** Everything in the §2.1 tail: `issues`, `issue_comment`,
`release`, reviews, comments, labels, milestones, deployments, Pages, `check_run`/
`check_suite`, `merge_group`, `branch_protection_rule`, discussions, `registry_package`,
`repository_dispatch`, `image_version`, `fork`, `public`, `create`, `delete`, `gollum`.
pluto's M4 event model already covers push/PR/issue/generic-webhook at the box level; the
workflow front-end reads those, not the other twenty.

**Why this line and not another.** Three independent sources drew almost the same line: act's
"is not going to be worked on" page, Gitea's comparison page, and the design of Woodpecker and
Concourse which never had these features. The features in the tail are all either
control-plane-shaped (they exist to coordinate many untrusted runs on other people's
infrastructure) or substrate-shaped (they assume a Linux Docker host). Neither applies to a
handful of machines owned by the people running the code.

---

## 9. Interop plan — ghcr and the Actions cache

### 9.1 ghcr: yes, fully, for pull; for push, as a declared credential

- **Pull, including container actions.** `docker://ghcr.io/OWNER/IMAGE_NAME` and any public
  OCI image works through the substrate with no pluto-specific code. Digest-pinning is the
  default for anything pluto itself references; a bare tag is allowed for a container action
  but is recorded as the digest it resolved to.
- **Private ghcr needs a user-supplied PAT** with `read:packages`, or `write:packages` to
  publish. [docs] That is a declared secret in the repo's pluto config, injected into the box
  at run time. It is not automatic, and it must not be — GitHub's own docs note the
  `GITHUB_TOKEN` gap when a namespace already exists unlinked, which means "just works"
  pushing is not a thing even on GitHub.
- **ghcr is the shared environment-layer transport.** A team's provisioned layers are OCI
  images; pushing them to ghcr and pulling on another machine is the interop path, and
  `org.opencontainers.image.source` plus repo-linking gives the permission inheritance for
  free. [docs] This is the concrete shape of the moonshot's "shared environment layers" and
  it is worth doing before any cache work, because it is the same mechanism the substrate
  already needs.

### 9.2 The Actions cache: speak the protocol, own the store

**The decision:** implement the v1 artifactcache API (`GET /_apis/artifactcache/cache`,
`POST /caches`, `PATCH /caches/:id`, `POST /caches/:id`, `GET /artifacts/:id`, `POST /clean`)
[repo] on the host daemon, with `Accept: application/json;api-version=6.0-preview.1` and a
bearer token. Then the stock, unmodified `actions/cache` works against pluto's own machines.

**What that buys:** any workflow that already uses `actions/cache` keeps working, with the
cache living on the user's hardware. No bundle patching, no fork, no Node in the box for it.

**The one caveat, stated up front:** the stock action computes `isGhes()` from
`GITHUB_SERVER_URL` and refuses if that host is not `github.com`, `*.ghe.com`, or
`*.localhost`. [repo] So this path is available when pluto is running against a GitHub-origin
repo and can honestly report `GITHUB_SERVER_URL=https://github.com` — i.e. the interop case
the moonshot actually wants. Against a Gitea/Forgejo origin, the stock action refuses, and the
only workaround is the bundle patch Gitea uses. Do not build the bundle patch. Print the
error instead.

**Semantics to copy verbatim**, because they are good and they are what the ecosystem
assumes:

- Restore order: exact key in branch → prefix in branch → `restore-keys` in branch → same
  sequence on the default branch. [docs]
- Scope: "Access restrictions provide cache isolation and security by creating a logical
  boundary between different branches or tags." [docs]
- Immutability: a new key is a new entry; you cannot mutate an existing cache. [docs] This is
  the property that makes cache ownership tractable, and it is the direct input to #121.
- Version stamp = hash of (paths, compression tool, salt) so a format change cannot poison a
  run. [docs]
- Eviction: 7 days since last access, 10 GB per repo default, LRU by last access. [docs]
- Read-only scope for low-trust triggers, with the same trigger allowlist GitHub publishes.
  [docs] A refused save warns and continues. [docs]
- Upload/download rate limits, per repository, so one noisy repo cannot starve the others.
  [docs]

**Using your own machines as the cache — the three documented options**, all small-fleet
sized: one dedicated cache server every host points at, with a shared secret; a shared
filesystem (NFS/CIFS) as the cache dir; or object storage (S3/MinIO) mounted as FUSE. [docs]
A recommendation for #121 rather than a decision here: start with the **dedicated server on
the one machine that is always on**, with `cache.dir` on local disk as the default and the
shared server as the opt-in. That matches the moonshot's "local-first by default; a shared
store is opt-in".

**Ownership question, deferred to #121 but shaped by the above.** GitHub's model is
single-writer-per-scope with LRU eviction, and it works because the scope boundary is the
branch. Two machines wanting the same key is the whole question, and the answer is in the
semantics already: entries are immutable, so a second writer creates a second entry, and
eviction picks the loser by last access. That makes "who owns it" a routing question rather
than a consensus one — which is the same shape as the rest of the moonshot's small-fleet model.

---

## 10. Open questions and what this note does not decide

1. **Substrate (#119) decides whether `services` and job `container` are cheap or expensive,
   and whether the JS-action tier requires Node in the guest image.** This note assumes
   container-first for `uses:` tier 2; if the answer is microVM-first, tier 2 needs a
   container runtime *inside* the box, which changes its cost but not its shape.
2. **`pull_request_target` vs `pull_request`.** pluto's existing trusted-default-branch
   policy is the gate; whether the workflow front-end honours the distinction *inside* a
   translated workflow (as GitHub does) or only at the event-admission layer is a
   `m4-event-delivery-and-trust.md` extension, not a new decision.
3. **Should `pluto.*` context aliases exist at all**, or is accepting `github.*` and filling
   what pluto can enough? Gitea's dual-spelling is the precedent for the former.
4. **Cache-mode as a first-class key.** GitHub now has one (`read`/`write`/`write-only`/
   `none`) [docs] and it is the right hook for #121. Adopting the vocabulary now is cheap.
5. **Whether environment layers double as the cache.** A layer is content-addressed and
   already shared; a cache is a mutable key/value store with LRU. They are different objects
   and merging them would be a mistake — but the *transport* (ghcr) is shared, which is why
   §9.1 recommends doing layers first.
6. **The honest limit of "the common subset".** §7 is a claim about what real repos use,
   derived from the four comparables' documented gaps rather than from a survey of real
   workflows. Before implementation, it should be checked against a handful of actual
   repositories — ideally this repo's own `.github/workflows/` and two or three others — at
   which point §7 gets amended or confirmed. Flagging this because it is the weakest part of
   the evidence.

---

## Sources

Primary sources fetched 2026-10-10.

**GitHub Actions docs**

- Workflow syntax for GitHub Actions —
  <https://docs.github.com/en/actions/reference/workflow-syntax-for-github-actions>.
- Events that trigger workflows —
  <https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows>.
- Metadata syntax for GitHub Actions —
  <https://docs.github.com/en/actions/reference/workflows-and-actions/metadata-syntax>.
- Workflow commands for GitHub Actions —
  <https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands>.
- Dependency caching — <https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching>.
- Caching dependencies to speed up workflows — <https://docs.github.com/en/actions/using-workflows/caching-dependencies-to-speed-up-workflows>.
- Workflow artifacts — <https://docs.github.com/en/actions/concepts/workflows-and-actions/workflow-artifacts>.
- Reuse workflows — <https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows>.
- Manually running a workflow — <https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow>.
- Expressions — <https://docs.github.com/en/actions/reference/workflows-and-actions/expressions>.
- Contexts — <https://docs.github.com/en/actions/reference/workflows-and-actions/contexts>.
- Usage limits — <https://docs.github.com/en/actions/reference/limits>.
- About custom actions — <https://docs.github.com/en/actions/creating-actions/about-custom-actions>.
- Working with the Container registry — <https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry>.
- Self-hosted runners — <https://docs.github.com/en/actions/concepts/runners/self-hosted-runners>.

**Action and toolkit source**

- `nektos/act` — <https://github.com/nektos/act>; `pkg/artifactcache/handler.go`,
  `pkg/exprparser/functions.go`, `cmd/root.go`.
- act user guide (Unsupported functionality, Runners) — <https://nektosact.com/not_supported>,
  <https://nektosact.com/usage/runners>.
- `actions/cache` — <https://github.com/actions/cache>; `dist/restore/index.js`
  (`getCacheServiceURL`, `isCacheFeatureAvailable`, `actionUtils_isGhes`).
- `actions/toolkit` — <https://github.com/actions/toolkit>;
  `packages/artifact/src/internal/shared/config.ts`, `packages/artifact/src/internal/client.ts`,
  `packages/artifact/src/internal/shared/errors.ts`.
- `actions/upload-artifact` — <https://github.com/actions/upload-artifact> (README, Limitations).

**Gitea and Forgejo**

- Compared to GitHub Actions — <https://docs.gitea.com/usage/actions/comparison>.
- FAQ — <https://docs.gitea.com/usage/actions/faq>.
- Design of Gitea Actions — <https://docs.gitea.com/usage/actions/design>.
- Caching (runner) — <https://docs.gitea.com/runner/cache>.
- Configuration cheat sheet — <https://docs.gitea.com/administration/config-cheat-sheet>.
- `gitea/actionslib` — <https://gitea.com/gitea/actionslib>;
  `pkg/model/workflow.go`, `pkg/exprparser/`.
- Forgejo Actions reference — <https://forgejo.org/docs/latest/user/actions/reference/>.

**Woodpecker and Concourse**

- Woodpecker: Intro — <https://woodpecker-ci.org/docs/intro>; Workflow syntax —
  <https://woodpecker-ci.org/docs/usage/workflow-syntax>; Terminology —
  <https://woodpecker-ci.org/docs/usage/terminology>; Local pipeline execution —
  <https://woodpecker-ci.org/docs/usage/local-execution>; Project settings —
  <https://woodpecker-ci.org/docs/usage/project-settings>.
- Concourse: Tasks — <https://concourse-ci.org/docs/tasks/>; Jobs —
  <https://concourse-ci.org/docs/jobs/>; Project — <https://concourse-ci.org/project/>.

**In-repo**

- `GLOSSARY.md` (trigger, event, task, run, job, box, environment layer).
- `docs/VISION.md`, `docs/contract.md`, `docs/adr/0007-box-contract.md`,
  `docs/adr/0012-task-run-cli-and-output.md`.
- `docs/research/cli-comparables.md` §3; `docs/research/devex-and-comparables.md` §1.3–1.5,
  §6.5; `docs/research/trigger-comparables.md`; `docs/research/container-and-microvm-hardening.md`
  (supply chain, SHA pinning); `docs/research/m4-event-delivery-and-trust.md`;
  `docs/research/m4-queue-and-hooks.md`; `docs/research/m4-work-item-and-remote-research.md`;
  `docs/research/m5-task-run-cli.md`.
