# Shared state and portable resume — research for ticket #121

Fetched 2026-10-09. Primary sources only. Each claim carries a link; quotes are short and
verbatim. Evidence tags:

- **[docs]** — behaviour documented on the vendor's own pages.
- **[vendor claim]** — the vendor's own claim, not independently verified.
- **[repo]** — this repository's source, contracts, ADRs, or research notes.
- **[inference]** — this author's reading, not a vendor statement.

This note answers ticket #121 of wayfinder map #117: portable state and cache ownership
for pluto's opt-in shared store, at pluto's scale — individuals and teams, trusted code, a
handful of machines they own.

**Steering constraints honoured here, stated once rather than repeated in every section.**

- celld.dev is inspiration for the **shape**: a bucket as the durable source of truth;
  ownership by conditional write; no membership protocol, failure detector, or consensus
  service; hosts self-fence; state restored from the bucket on takeover. It is explicitly
  **not** inspiration for the machinery: fleet-scale replication pipelines, epoch-as-a-system,
  LTX-style continuous replication, cell-density tuning. celld is built for massively
  distributed work; pluto is a handful of machines.
- #116's default stands: **portable state, not live migration**; "no leases, epochs, CAS
  ownership, or memory snapshots at this scale." Any machinery in this note is justified by a
  concrete need named in the text, or marked as a deferral with a revive trigger.

**Groundwork this note builds on rather than repeats** — `docs/research/durable-objects-leases.md`
on branch `research/durable-objects-leases` (read via
`git show origin/research/durable-objects-leases:docs/research/durable-objects-leases.md`),
`docs/research/store-substrate.md`, `docs/research/disk-state-pipeline.md`, and the deferral
rows in `docs/DEFERRED.md` ("Fleet layer: leases, epochs, CAS ownership, handoff", revive
trigger "a second host is actually in use").

---

## 1. The decision frame: one store, two promises

The shared store — if and when it is built — holds two different things, and conflating them
is the main design risk this note exists to prevent.

| | Durable dev box | Ephemeral CI box |
|---|---|---|
| Durable unit | the box: its disk and its record | the run: its record, logs, and artifacts |
| What the store owes it | the **previous** disk state, restorable elsewhere | the **warm base**: environment layers and caches |
| Promise if the host dies | the box resumes on another host, as if nothing had happened | the run is re-created from scratch against the cache |
| Resumes? | **yes** | **no** — it re-runs |
| Memory | never portable (see §2) | not a concept |
| Who writes it | one host at a time | many hosts, concurrently, into shared keys |

This is #116's "A durable dev box *resumes* elsewhere; an ephemeral CI box does not — it
re-runs against the shared cache. Same store, different promise." [repo: #116] The store's
layout should make the two halves separately addressable, because they have different
correctness requirements: the dev half is single-writer state with a commit point, the CI
half is an immutable content-addressed cache with many concurrent writers.

**Scale-honest framing.** The fleet is "a handful of machines, not a data centre" [#116]. At
that size the store is a small S3-compatible service (or one host's directory) that a few
daemons read and write. Nothing below assumes more than three or four hosts.

---

## 2. Portable state: the disk plus the record, and what "resume elsewhere" means

### 2.1 What a box is, today

pluto already has the two halves, both host-local: the state directory holds a versioned
record per box, and the box's disk lives on its host. [repo: `internal/state/state.go`,
`docs/adr/0004-storage-and-lease-substrate.md`]

- The record (`state.Box`) carries: `id` (an immutable uuid per incarnation), `project`,
  `branch`, `worktree`, `primary_repo_url`, `ref`, `image`, `state`,
  `trust_class`, `environment_layer` (frozen at disk creation), `remotes`, `resources`,
  `schedules`, `jobs`, `clean_stop`, and timestamps. [repo: `internal/state/state.go`]
- The disk is a rootfs image in the box directory, plus the box's own SSH keypair
  (`boxes/<id>/id`, `id.pub`) which the runner installs into the guest's
  `authorized_keys`. [repo: `internal/runner/disk.go`]
- `pause` is clean-stop-only: "disk state is as of the last graceful stop, a crash may lose
  recent writes, and git is the floor." [repo: `docs/adr/0002-box-lifecycle.md`]

`CleanStop` already records that the last stop was an observed clean guest shutdown, and
`EnvironmentLayer` is already frozen at creation so a later contract edit cannot move an
existing disk's key. [repo: `internal/state/state.go`] Portability is therefore a matter of
moving the record and the image, and of one more rule: *never treat a disk as restorable
unless its record says the stop was clean.*

### 2.2 The portability boundary is the disk, not the memory

The moonshot's choice of portable state over live migration is supported by the primary
sources rather than being merely a preference.

- **Firecracker snapshots are host-bound.** "Snapshots must be resumed on an software and
  hardware configuration which is identical to what they were generated on"; the docs'
  compatibility table is limited to identical instance types, and states that restoring
  across a different host kernel "is considered unstable in Firecracker." [docs:
  [snapshotting](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md),
  [versioning](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/versioning.md)]
- **The disk is explicitly outside the snapshot.** "The block devices attached to the microVM
  are not considered part of the state and need to be managed separately." [docs: same,
  versioning] So even a memory-snapshot design still has to ship the disk — which is the
  part we want anyway.
- **Resume does not preserve the network or the clock.** "Guest network connectivity is not
  guaranteed to be preserved after resume"; vsock connections are closed; a resumed guest's
  wall clock continues from the snapshot moment until the guest syncs it. [docs: same,
  snapshot-support] A resumed box is therefore *not* a box whose services never noticed.
- **The comparable products agree.** E2B's `keepMemory: false` gives a filesystem-only
  snapshot that cold-boots on resume; Fly suspend uses a full snapshot but documents that it
  "is not guaranteed to persist" and that network connections may need reconnecting, while an
  attached volume survives regardless. [docs: cited in
  `docs/research/durable-objects-leases.md` §3.1, §3.3]

**[inference]** The portable unit is the *disk plus the record*. The memory is not portable in
any of the systems surveyed, and even where it is "resumed", the things pluto's boxes care
about (services, tunnels, sessions, ports) are re-established on wake anyway. This is also the
cheapest correct answer: it needs no fencing of a memory tail, no epoch, and no snapshot
compatibility matrix.

### 2.3 "Resume elsewhere" for a paused box and its services

pluto's contract already contains the answer, in two verbs that exist today:

- `[services.<name>]` is "A long-lived process: supervised in the box and restarted on every
  wake." [repo: `docs/contract.md`]
- `[wake]` "Runs **every time the box starts**: restart services, refresh tunnels and
  credentials, repair what a pause discarded. Keep it short and idempotent." [repo:
  `docs/contract.md`]

**Proposed semantics (the load-bearing decision of this note).** "Resume elsewhere" means:

1. The **disk image and the record** are restored on the new host, byte-identical to the last
   clean stop.
2. The box **boots and runs `[wake]`**, which restarts services, refreshes credentials, and
   repairs whatever a pause discarded.
3. Sessions are **restarted, not continued**: they are "restarted on every wake and killed by
   pause: durability is disk-only … so a session's program resumes from its own on-disk
   state." [repo: `docs/contract.md`]
4. The guest's SSH keypair travels with the record, or is regenerated on the new host — see
   §2.5.
5. **A slightly slower start is accepted** [#116] — chunk download plus boot plus wake. pluto
   should publish a measured number, not a promised one.

Nothing in the guest survives as a running process, and nothing needs to: the contract's own
vocabulary already says a box is a disk plus a start procedure. The "as if nothing had
happened" promise is about **state and reachability**, not process continuity. Concretely it
means: the working tree, installed toolchain, git state, jobs and schedules, the record, and
the fact that the same box id answers to the same CLI command — on a different host.

**What must *not* be promised**: that in-flight network connections survive, that a session's
program kept running, or that a pause is atomic against a host crash. ADR 0002 already limits
the durability contract to "the last graceful stop" and git as the floor; resume-elsewhere
inherits that contract exactly, and should say so in user-facing help rather than implying
more.

### 2.4 The pause durability question, which is the real one

A resume-elsewhere that restores "the last clean stop" needs a definition of *when the clean
stop becomes durable somewhere another host can read*. The prior note called this the biggest
unresolved lever: "Does pluto guarantee 'all disk state at pause time survives', or 'last
checkpoint survives'?" [repo: `durable-objects-leases.md` §6 item 1]

Two honest options:

- **(a) Pause is a durability boundary.** `pause` returns only after the disk is in the store
  and the record's commit succeeded. This is celld's acknowledgement rule in miniature: hold
  the response until the write is proven. [docs: celld `guarantees.md`, "The acknowledgement
  rule" — "A gate holds each response until a durability proof covers every write it can
  reveal"] The cost is that pause takes as long as the upload, which for a multi-GB rootfs on
  a handful of machines' worth of disk is a real but bounded cost.
- **(b) Pause is fast and durability is asynchronous.** Pause returns after a local fsync;
  the upload happens in the background and resume-elsewhere is only valid up to the last
  completed upload. Cheaper and simpler, but "resume elsewhere" becomes best-effort and must
  be documented as such.

**[inference]** For a handful of machines, (a) is affordable and makes the promise true, and
it is the only option that does not require a second, separate "sync status" concept in the
CLI. If the upload takes too long at real image sizes, (b) with an explicit `pluto sync`
status is the fallback, and it should be a recorded decision rather than an accident. The
local-flush half of pause already exists in Firecracker: "The block device backing files are
drained and `fsync`'d to the host during snapshot creation, so their contents are committed to
the underlying persistent storage regardless of the `sync_snapshot_files` setting." [docs:
snapshot-support]

### 2.5 Record-level hazards the note should name

These are small, concrete, and each has a cheap answer; naming them now is cheaper than
discovering them during implementation.

| Hazard | Cheap answer |
|---|---|
| A box's SSH keypair lives in the box directory | Make it part of the record's blob set, or regenerate on takeover and rewrite `authorized_keys` in `[wake]`. Regenerating is simpler and avoids copying a private key into the store. |
| The record holds a host-absolute `worktree` path | Do not trust it on restore; the record's identity is `primary_repo_url` + `ref`, and the local path is re-derived on the new host. |
| The record's `image` version may not exist on the new host | Fail loudly at start with an "image not found, import it" error, or publish the image alongside the box as a second content-addressed object. |
| Resume on two hosts at once | See §5.4 — one conditional write, or an explicit operator act. |
| A stopped-but-not-clean box | `CleanStop == false` means no resume-elsewhere claim; local restart only. |


---

## 3. Cache correctness and ownership

### 3.1 What pluto already has, and why it is right

The environment cache is the existing precedent, and the ticket should not reinvent it.
[repo: `internal/envcache/cache.go`, `docs/contract.md` "Reusable environment layers"]

- **Content-addressed, scoped key.** The key is a SHA-256 over trust class, project identity,
  declared setup fingerprint, and resolved image — "Missing or unknown scoping inputs fail
  closed rather than collapsing unrelated environments." [repo: `envcache/cache.go`, `Key`]
- **Immutable entries.** "A key that already resolves is left untouched: a published layer is
  immutable." [repo: `envcache/cache.go`, `Publish`] Publish is temp-copy → scrub → fsync →
  rename, so a half-written layer can never be read. [repo: same]
- **Trust classes in the key.** "Layers are never shared across trust classes. A trusted box
  publishes and consumes trusted layers; an untrusted work-item box … resolves to a separate
  untrusted class and never publishes." [repo: `docs/contract.md`]
- **Scrub before publish.** The scrub "removes the worktree, agent state, logs, jobs, sessions,
  shell history, and ssh identity before the layer becomes visible." [repo:
  `docs/contract.md`] Plus a parse-time refusal: a cached provision "may not declare `[env]`
  or `[provision].env`: those values would be captured in the shared layer." [repo: same]
- **A build claim with a lease.** "Coordination is host-local and in-memory: a concurrent miss
  waits for the builder, and a build claim expires after a bounded lease so a builder that
  never publishes cannot starve its peers." [repo: `docs/contract.md`; the in-memory claim is
  `internal/runner/environment_cache.go`]

Everything above stays as the *semantics* of the cache. What changes with a shared store is
only **where the entries live and how the claim is taken**.

### 3.2 Concurrent writers to the same key: the Docker/Bazel precedent

The Docker/Bazel precedent named in the ticket is the load-bearing part of this section, and
both precedents say the same thing: **write-once, content-addressed, and let the second
writer be harmless.**

- **Bazel splits the cache in two.** "The remote cache stores two types of data: The action
  cache, which is a map of action hashes to action result metadata. A content-addressable
  store (CAS) of output files." [docs: [remote caching](https://bazel.build/remote/caching)]
  The CAS half is opaque bytes under a hash; the mapping half is the small mutable-looking
  thing, and it too is write-once.
- **Registries make the duplicate write a non-event.** The OCI distribution spec's
  deduplication use case: "If process A and B upload the same layer at the same time, both
  operations will proceed and the first to complete will be stored in the registry. Even in
  the case where both uploads are accepted, the registry may securely only store one copy of
  the layer since the computed digests match." [docs: [distribution-spec §Use
  Cases](https://github.com/opencontainers/distribution-spec/blob/main/spec.md)] And the
  image spec makes content the identity: the manifest contains "the content-addressable
  identity of one or more filesystem layer changeset archives that will be unpacked to make up
  the final runnable filesystem." [docs: [image-spec](https://github.com/opencontainers/image-spec/blob/main/spec.md)]
- **GitHub Actions makes immutability explicit.** "You cannot change the contents of an
  existing cache. Instead, you can create a new cache with a new key." [docs:
  [dependency caching reference](https://docs.github.com/en/actions/reference/dependency-caching-reference)]
  GitHub's answer to two writers is that there is no such thing as a second writer to an
  existing entry: misses create new entries.
- **Docker's own cache backend follows the same model.** BuildKit exports to a bucket as
  content-addressed blobs under prefixes, with `mode=min|max` controlling *which* layers
  export — the store is addressed, not mutated in place. [docs:
  [`--cache-to type=s3`](https://docs.docker.com/build/cache/backends/s3/)]

**Proposed rule for pluto.** Cache entries are addressed by the key and are immutable, and
the *only* mutable object in the cache half of the store is the claim that a build is in
flight. Two writers to the same absent key are therefore not a correctness problem — both
may build, and whichever publishes first wins; the loser's work is discarded on the next GC.
What the claim prevents is wasted work, not corruption. This keeps the cache half free of
ownership machinery entirely, which is exactly the shape the steering comment asks for.

**[inference]** This is why the ticket's "concurrent writers to the same key" worry has a
cheap answer that the prior lease machinery does *not* need to touch: the cache is
content-addressed, so a lost race costs CPU, not correctness. Ownership only has to exist for
the single-writer thing (the dev box's disk), not for the many-writer thing (the cache).

### 3.3 The claim, when the store is shared

Today's claim is in-memory and host-local, which is correct for one host. [repo:
`internal/runner/environment_cache.go`] With a shared store the choices are:

1. **A claim object in the store, created with a conditional write.** This is the celld shape
   at its smallest: "A conditional create: the write must fail when the object exists." [docs:
   celld [guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md)]
   The claim carries an expiry so a builder that never publishes cannot starve its peers —
   the same bounded-lease rule the host-local implementation already has. [repo:
   `docs/contract.md`]
2. **No claim at all; let both build and GC the loser.** Acceptable for a handful of machines.
   The cost is a duplicated provision, which is exactly the cost the feature exists to avoid.
3. **A per-host build namespace** ("only this host ever builds layer X"), which makes
   concurrency impossible and gives up the sharing the feature exists for.

**[inference]** (1) is worth it and is small: one conditional create plus an expiry, no lease
renewal loop, no failure detector. If a store without usable conditional writes is chosen
(§4), fall back to (2) and document that a shared miss may build twice.

### 3.4 Trust classes, and what "scrubbed" has to mean remotely

The trust-class boundary is already structural (the class is inside the key), so it survives
the move to a shared store unchanged. Two things do change.

**Publishing across a trust boundary.** GitHub's model is the relevant precedent for the
direction pluto chose: GitHub restricts which *triggers* can write to the default branch's
cache scope, and gives low-trust runs read-only access by default, with a `cache-mode` table
(`read` / `write` / `write-only` / `none`) as the override. [docs:
[dependency caching reference](https://docs.github.com/en/actions/reference/dependency-caching-reference)]
pluto's existing equivalent is the `share_untrusted` opt-in plus the scrub. [repo:
`docs/contract.md`] Keeping that shape — an explicit, per-contract opt-in for the
secret-free direction, and no path at all for the untrusted class to publish — is the right
default and needs no new mechanism.

**Scrubbing has to be enforced at publish time, not at read time.** GitHub's warning is the
one to internalise: "Cache contents are not signed or verified, and any workflow run that can
read a cache may extract its contents. Extracted caches may modify files that are
subsequently executed in a workflow run, leading to malicious code execution." [docs: same]
pluto's scrub already precedes publish and runs against a private copy before it becomes
visible [repo: `envcache/cache.go`, `Publish`], which is the correct order. **[inference]** For
a *team* store the honest statement is: the cache is trusted to the extent that the publisher
is trusted, and the boundary is "who may run a publishable provision", not "who may read the
store". The scrub is defence against accidental leakage (secrets, host paths, worktrees), not
against a hostile publisher; at pluto's scale that is the correct amount of claim, and it
should be written down rather than left implied.

**Verification on read.** One cheap addition worth taking from Bazel and the registry: a
cache entry can carry a digest of its content, verified on materialisation. This turns a
corrupt or half-written entry into a miss instead of a broken box, and it is the same thing
`pluto image import` already does for images ("The import re-verifies every hash against
`manifest.json`"). [repo: `images/README.md`]

### 3.5 Retention and eviction

- The precedent is LRU by access: GitHub "will remove any cache entries that have not been
  accessed in over 7 days" and evicts "in order of last access date, from oldest to most
  recent" once a repo hits its cap. [docs: dependency caching reference]
- Bazel's self-hosted cache "provides garbage collection to enforce an upper storage limit
  and clean unused artifacts." [docs: [remote caching](https://bazel.build/remote/caching)]
- Bazel also names the recovery verb plainly: delete content "to … create a clean cache after
  a cache was poisoned." [docs: same]

**[inference]** A handful of machines on disks they own do not need automatic retention to be
clever. A size cap plus access-time LRU plus a manual `pluto layer ls|prune` is enough (the
`layer ls|prune` surface is already proposed in `docs/research/devex-and-comparables.md`).
What *is* needed is that the policy is explicit and user-visible, because a silently evicted
layer looks exactly like a cold cache.

### 3.6 What the CI half of the cache is *for*

The ticket's third question — dev-resume versus CI-rerun — is answered by §1's table plus one
observation: **the CI box never reads the dev box's disk.** It reads the environment layers
and the run caches. [inference, from #116's model] That separation is worth preserving in the
store's layout, because it is what keeps the CI path free of the resume machinery: a CI box
restores an immutable layer by key, boots, runs, and is discarded. There is no record of a CI
box to resume and no disk to ship — its record is its outcome. Same store, different promise,
and the CI promise is the simpler one.

---

## 4. Store substrate: pick a path for when this is built, not a build now

### 4.1 The requirement set, derived rather than assumed

From §2 and §3, the store has to provide exactly five things:

1. **A conditional create and a conditional overwrite** — celld's bucket contract in two
   lines: "A conditional create: the write must fail when the object exists. A conditional
   overwrite: the write must fail when the object changed after the read." [docs: celld
   [guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md)]
2. **Read-after-write consistency.** "A read after a successful write must return that write."
   [docs: same]
3. **Whole-object GET and PUT.** That is enough for records, manifests, and layer blobs at
   this scale.
4. **List-after-write consistency — only for GC.** celld needs it "for epoch GC … only"; the
   same applies to layer GC. [docs: same] If GC is manual for a while, this can be deferred.
5. **Honest failure behaviour.** "A store that accepts the conditional headers but ignores the
   condition fails late and silently, so run the storage test." [docs: same]

Nothing else. No ranged reads (only needed if disk state is paged in, which it is not at this
scale), no quorum, no cross-region replication, no bandwidth tuning.

### 4.2 The candidates, judged against that set

| Option | Verdict for pluto | Evidence |
|---|---|---|
| **A local directory / SQLite on one host, and that host is "the store"** | **The local-first default.** Keeps today's model; makes that host special. Acceptable and honest, but loses the "box resumes on pc2 when pc2 has room" promise. | [repo: `docs/adr/0004`; `store-substrate.md` §1 records celld's own `celld dev` doing exactly this — an embedded SQLite `objects.sqlite3` with one `BEGIN IMMEDIATE` transaction per put, i.e. CAS by single-writer SQLite] |
| **MinIO CE** | Disqualified as a *forward* choice: the repo is `archived: true`, last release 2025-10-15. The conditional-write fix shipped 2025-09-07, so the final releases do have it. | [repo: `store-substrate.md` §4, verified against the GitHub API; upstream issue [#21526](https://github.com/minio/minio/issues/21526) / [PR #21567](https://github.com/minio/minio/pull/21567)] |
| **SeaweedFS** | **The leading candidate.** Single-node `weed server -s3`, Apache-2.0, conditional writes implemented above the store with cluster-wide atomicity. | [docs: [S3 Conditional Operations](https://github.com/seaweedfs/seaweedfs/wiki/S3-Conditional-Operations): "The conditional check and the write are atomic — cluster-wide."; `store-substrate.md` §2 records the version floor and the first-host gotchas] |
| **RustFS** | A live challenger: 1.0.1 (2026-10-03), conditional writes ported from MinIO, but with a visible recent bug trail. | [repo: `store-substrate.md` §4] |
| **Ceph RGW** | Correct if you already run Ceph; far too heavy for a 12-core/16 GB first host. | [repo: `durable-objects-leases.md` §4.5] |
| **Garage** | **Disqualified for this role, by its own design.** It cannot host the coordinating write, so any lease/CAS layer would have to live somewhere else — a second, competing layer, which is the thing celld deliberately deleted. | [docs: [known issues](https://garagehq.deuxfleurs.fr/documentation/reference-manual/known-issues/): "No conditional writes / locking / WORM support (`if-none-match`, …) … structurally impossible to implement in Garage due to the lack of a consensus algorithm"] |
| **etcd / Consul / Postgres advisory locks** | Rejected: an always-on consensus service, which is exactly what the steering rules out, plus a second failure domain. | [repo: `durable-objects-leases.md` §4.7] |
| **Litestream / LTX continuous replication** | **Explicitly not wanted.** It exists to bound RPO for a live database; pluto's pause is already a checkpoint, and Firecracker's raw-only drives rule out the dirty-bitmap mechanism that would make continuous capture cheap. | [docs: [Litestream how it works](https://litestream.io/how-it-works/); repo: `docs/adr/0001` "drives stay raw — no qcow2"; `docs/DEFERRED.md` "Continuous replication … Dead unless the VMM changes"] |

### 4.3 The path this note proposes

**When the shared store is built (revive trigger: a second host actually in use), build it as
one S3-compatible store with conditional writes, put both halves of the store in it, and prove
the store before trusting it.** [inference]

Concretely, and in dependency order:

1. **Keep the local-first state model exactly as it is** until the opt-in exists. [repo:
   `docs/adr/0004`]
2. **Name the store as an explicit opt-in** — `[shared] store = "…"` or equivalent — that
   turns on portability and nothing else. Default off stays the default. [repo: #116
   "local-first by default; a shared store is opt-in"]
3. **SeaweedFS ≥ 4.18, single-node, `weed server -s3`** as the default recommended store.
4. **Run celld's storage test before serving.** Its four writes are cheap and are the only
   honest check of a compatibility table: "ok bucket conditional write (create, reject-create,
   update, reject-stale)" — "Two of the four must fail; if the store accepts either, the
   command exits with an error that names the store." [docs: celld guarantees, "The storage
   test"]
5. **Layout, taking the shape and dropping the machinery:**

   ```
   boxes/<box-id>/own.json      # {host} — the one conditional-write record
   boxes/<box-id>/manifest      # committed pointer to the disk's chunk set
   boxes/<box-id>/chunks/<hash> # immutable, content-addressed
   layers/<layer-key>/rootfs    # immutable environment layers (§3)
   cache/<key>/…                # run caches, content-addressed (§3.2)
   ```

   The `own.json` record is the whole coordination layer. No `nodes/` prefix, no node lease,
   no wake entries, no follower ensemble, no LTX prefix, no epoch-GC.

**[inference]** The epoch-as-fence mechanism is the clearest example of machinery to drop.
celld stamps the epoch into the object key so a losing writer's writes "land in a superseded
prefix". [docs: celld guarantees, "The epoch prefix"] pluto gets the same protection for free
from content-addressed chunks: a stale writer can only *add* blobs nobody references, because
the chunks are immutable and the manifest is the only mutable thing — and the manifest is
behind the one conditional write. So the fence is the record, not the key layout. That is the
same guarantee at a fraction of the mechanism, which is the whole point of taking the shape.

### 4.4 What is deliberately deferred, with triggers

| Item | Why not now | Revive trigger |
|---|---|---|
| GC of superseded manifests/chunks | Needs list-after-write consistency; a handful of machines can prune by hand | A store without manual pruning becomes painful, or disk pressure appears |
| Chunked CAS / desync-style dedup for box disks | Pays only with many boxes or hosts [repo: `docs/DEFERRED.md`] | "Export is used enough to hurt" |
| Continuous replication (qcow2 + dirty bitmaps) | Impossible under Firecracker's raw-only drives [repo: `docs/adr/0001`] | The VMM changes |
| Any membership, failure detector, or consensus service | Ruled out by the steering; at pluto's scale the operator is the failure detector | A second host is in use **and** unattended resume is wanted |
| Epoch numbering on `own.json` | A single host field in one record is enough while moves are operator-initiated (§5.4) | Unattended takeover actually happens |
| `pluto host ls`, cross-host forwarding, placement policy | Not needed until two hosts share work | A second host is in use |

---

## 5. Access stays light: SSH plus a tailnet, coarse roles

### 5.1 What the current posture already is

"The CLI is the control surface and SSH is the only transport … Authentication is SSH keys
plus unix-socket permissions; pluto has no account system … Reachability is the user's choice:
a plain port-forward, WireGuard, or an existing tailnet. pluto ships no relay, no control
plane, and no DERP." [repo: `docs/adr/0006-access-is-ssh.md`] And #116's constraint:
"**Access stays light** — SSH plus a tailnet reaches a few people's machines; roles are
coarse ('the team can use these boxes'). No SSO, RBAC, audit, or compliance." [repo: #116]

Nothing in the shared-store work changes this, and the note should say so explicitly so it
does not get re-litigated: **the store is not an access system.** It is reached over the same
tailnet, by the daemon, with credentials that are not user credentials.

### 5.2 Coarse roles, concretely

At pluto's scale the whole role model is two facts about a person or a device [inference,
from #116's "the team can use these boxes"]:

- **Can this device reach the host?** Tailnet membership. Tailscale's policy file is
  deny-by-default and locally enforced: "ACLs are deny-by-default, directional, locally
  enforced, and don't affect local network traffic." [docs: [Manage permissions using
  ACLs](https://tailscale.com/kb/1011/acls)] Note the documented default: with no `acls`
  section, "Tailscale applies the default allow all policy" — worth stating to users, since a
  fresh tailnet is permissive by default. [docs: same]
- **Can this person use these boxes?** One coarse answer, expressed as an SSH key in the
  host's `authorized_keys` (or the tailnet's SSH policy), not as a role object in pluto.

If a third fact is ever needed — "this person may use the shared store's cache but not its
boxes" — that is the moment a role noun appears, and not before. Until then, per #116, no SSO,
no RBAC, no audit, no compliance. [repo: #116]

### 5.3 Where the *store's* credentials sit, which is the one new security surface

The one genuinely new trust boundary is not the network, it is the bucket. celld is explicit:
"The bucket credentials control the fleet." [docs: celld `security.md`, cited in
`durable-objects-leases.md` §1.8] **[inference]** For pluto the rule that follows is simple
and should be written down: **store credentials belong to the daemon on the host, never to a
box and never to a user's shell.** A box that could write the store could poison a cache (§3.4)
or claim another box's record (§5.4). Since the cache half is reachable only by the daemon
anyway, this costs nothing architecturally.

### 5.4 Ownership: the one conditional write, and why it is small

The only coordination object in the whole design is `boxes/<box-id>/own.json`, created with
`If-None-Match: *` and moved with `If-Match: <etag>`. It names the host that may attach the
box. This is celld's ownership record, unchanged in shape, and nothing else from celld comes
with it. [docs: celld guarantees, "The ownership record" — "A node acquires a cell with a
conditional create when no record exists, or a compare-and-swap on an existing record, so two
nodes cannot acquire the same cell."]

**[inference]** At pluto's scale the honest simplification is that **resume-elsewhere is
operator-initiated, not automatic.** The happy path is: the box is paused on pc1, the operator
says "run this on pc2", and pc2 conditionally writes `own.json` to itself and restores. There
is no need for a lease TTL, a renewal loop, or a failure detector, because the operator — or a
single explicit `pluto resume-elsewhere` command — *is* the failure detector. The unattended
case ("pc2 notices pc1 is gone and takes the box") is the concrete need that would revive the
fleet layer in `docs/DEFERRED.md`, and until a user asks for it, building it would be
over-engineering at this scale.

Two consequences worth stating:

- **Self-fencing stays.** Even with one record and no lease, a host should refuse to serve a
  box whose record does not name it. celld's fence triggers are the record-based ones, not
  the clock-based ones: "It also fences at once when its lease record is gone or no longer
  matches what it published." [docs: celld guarantees, "Self-fencing"] The clock-driven
  trigger (`expiry passes`) is the one we do not need. **[inference]** So: read the record
  before acknowledging a pause or a resume, and stop if it does not name you. That is
  celld's acknowledgement rule, and it is the single most transferable line of the protocol.
- **Two hosts, one disk, is prevented by the record, not by a locking service.** A box that
  is already attached elsewhere is refused, not queued.

---

## 6. Proposed decisions

Each is stated as a decision, with what it costs if wrong. These are proposals for review on
the map, not implemented choices.

**D1 — Portable state is the disk plus the record; memory is never part of it.**
Supported by Firecracker's snapshot host-binding, the comparable products, and #116's default.
Cost if wrong: none identified; this is the conservative end of the option space.

**D2 — "Resume elsewhere" means restore + boot + `[wake]`; services and sessions are restarted,
not continued.**
This is what the contract already says for wake; the note only extends it to a new host. Cost
if wrong: a user who expected a container's worth of process continuity is disappointed; the
contract's existing wording already sets that expectation.

**D3 — Pause is a durability boundary: `pause` returns only after the state is in the store
and the commit succeeded.**
The alternative (async durability) makes "resume elsewhere" best-effort and needs a second
status concept. Cost if wrong: pauses get slower; if image sizes make that intolerable, fall
back to async with an explicit `pluto sync` status, and record it.

**D4 — The CI half of the store never reads the dev disk.**
Layout separation in §4.3 (layers/cache vs boxes) enforces it structurally. Cost if wrong: a
CI box that could restore a dev box's disk would blur the promise and import the resume
machinery into the ephemeral path.

**D5 — Cache entries are immutable and content-addressed; a lost race is wasted work, not
corruption.** The only mutable cache object is the build claim. Cost if wrong: a duplicate
provision on a shared miss; acceptable.

**D6 — Trust classes stay two, the class is inside the key, the scrub stays before publish,
and the untrusted class never publishes.** Cost if wrong: none — this is the existing
implementation, only relocated.

**D7 — When the shared store is built: one S3-compatible store with conditional writes,
default recommendation SeaweedFS ≥ 4.18 single-node, with celld's four-write storage probe at
daemon startup.** Garage is disqualified for the coordinating role. MinIO is disqualified
forward. Cost if wrong: an operator who already runs Ceph/Garage is told to run something
new; the note allows both (Garage for blobs only if coordination lives in SQLite on one host,
which reintroduces a special host).

**D8 — Store credentials belong to the daemon; never to a box.** Cost if wrong: none.

**D9 — Exactly one coordination object (`own.json`), created and moved by conditional write;
no lease TTL, no epoch, no membership, no failure detector; resume-elsewhere is
operator-initiated for now.** Cost if wrong: unattended failover is not supported; that is the
revive trigger for `docs/DEFERRED.md`'s fleet row.

**D10 — The local-first path is not deleted by any of this.** `docs/adr/0004` stands; the
shared store is an opt-in that adds portability.

## 7. Open questions

1. **Pause durability:** D3 versus async. The deciding measurement is upload time for a
   realistic rootfs over a tailnet — worth measuring before the store is built, not after.
2. **Disk-state format.** Whole raw image per box, or chunked? Chunking only pays with many
   boxes or hosts [repo: `docs/DEFERRED.md`], so the default is whole-image-until-it-hurts.
   This interacts with D7's store choice and with GC policy.
3. **Box SSH identity:** regenerate on takeover (simpler, breaks `known_hosts`) or travel with
   the record (keeps identity, copies a private key into the store).
4. **Unattended resume:** is there a real request for "pc2 takes the box when pc1 is gone"? If
   yes, the fleet row in `docs/DEFERRED.md` revives and the design grows a lease.
5. **Clock and TTL margins** — only relevant if (4) becomes yes. `store-substrate.md` records
   that celld documents no lease-clock margin at all; safety comes from readback, not clocks.
6. **Image portability:** must the base image version travel with the box, or is "import it on
   pc2" acceptable?
7. **gc/manual-prune for a shared store:** list-after-write consistency is a store-selection
   constraint; if manual prune is fine for a long while, the constraint softens.

## 8. Sources

Primary sources fetched 2026-10-09. Prior in-repo research is cited, not re-fetched.

**celld / Deno**
- [What celld guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md) —
  bucket contract, storage test, ownership record, acknowledgement rule, self-fencing,
  epoch prefix
- [Security](https://github.com/denoland/celld/blob/main/docs/security.md) — bucket
  credentials control the fleet
- [celld.dev docs](https://celld.dev/docs) — cell lifecycle, `celld dev` local store

**VMM and snapshot**
- [Firecracker: Snapshotting](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)
  — host-bound restore, network not preserved, block files `fsync`'d on snapshot create
- [Firecracker: Snapshot versioning](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/versioning.md)
  — "block devices … not considered part of the state", kernel and CPU compatibility

**Cache and build**
- [GitHub Actions: dependency caching reference](https://docs.github.com/en/actions/reference/dependency-caching-reference)
  — immutability, cache-mode, low-trust read-only default, poisoning, eviction
- [Bazel: remote caching](https://bazel.build/remote/caching) — AC + CAS, backend choice,
  write-access warning, GC, poisoned-cache recovery
- [OCI distribution spec, Use Cases](https://github.com/opencontainers/distribution-spec/blob/main/spec.md)
  — digest verification, concurrent identical uploads
- [OCI image spec](https://github.com/opencontainers/image-spec/blob/main/spec.md) —
  content-addressable layer identity
- [Docker: S3 cache backend](https://docs.docker.com/build/cache/backends/s3/) — `mode=min|max`,
  prefixes
- [Litestream: how it works](https://litestream.io/how-it-works/) — WAL→LTX continuous
  replication (the machinery we are not taking)

**Object stores**
- [SeaweedFS: S3 conditional operations](https://github.com/seaweedfs/seaweedfs/wiki/S3-Conditional-Operations)
  — "atomic — cluster-wide", `If-None-Match: *`, `412`
- [Garage: known issues](https://garagehq.deuxfleurs.fr/documentation/reference-manual/known-issues/)
  — no conditional writes, structurally
- [MinIO #21526](https://github.com/minio/minio/issues/21526) / [PR #21567](https://github.com/minio/minio/pull/21567)
  — conditional-write fix; repo archived per `docs/research/store-substrate.md` §4

**Access**
- [Tailscale: manage permissions using ACLs](https://tailscale.com/kb/1011/acls) —
  deny-by-default, directional, locally enforced, default allow-all

**In-repo**
- `docs/adr/0002-box-lifecycle.md` — pause and durability contract
- `docs/adr/0004-storage-and-lease-substrate.md` — local-first state; the bucket is deferred
- `docs/adr/0006-access-is-ssh.md` — SSH plus tailnet, no accounts, no relay
- `docs/contract.md` — `[services]`, `[wake]`, reusable environment layers, scrub
- `internal/envcache/cache.go` — content-addressed key, immutable publish, trust classes
- `internal/runner/environment_cache.go` — in-memory build claim with a bounded lease
- `internal/state/state.go` — the box record, `CleanStop`, frozen `EnvironmentLayer`
- `internal/runner/disk.go` — the box's SSH keypair
- `docs/DEFERRED.md` — fleet layer, bucket state, chunked CAS, continuous replication
- `docs/research/durable-objects-leases.md` (branch `research/durable-objects-leases`) —
  celld/Cloudflare/E2B/Fly mechanics, store qualification, the §5 mapping and §6 open questions
- `docs/research/store-substrate.md` — lease constants, `celld dev` SQLite store, SeaweedFS
  single-node, MinIO archived, RustFS
- `docs/research/disk-state-pipeline.md` — raw-image capture, desync, why qcow2 is not an
  option under Firecracker
- `docs/research/devex-and-comparables.md` — `pluto layer ls|prune`, shared cache as the team
  charter
- Issues #116 (the moonshot), #117 (the map), #121 (this ticket)
