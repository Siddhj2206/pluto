# Durable objects and lease coordination

- **Ticket:** [Siddhj2206/pluto#4](https://github.com/Siddhj2206/pluto/issues/4) (Research: durable objects and lease coordination)
- **Date:** 2026-10-04
- **Method:** primary sources only (official docs, source code, release notes, vendor engineering blogs). Every claim below was read from the linked source; uncertainty is flagged inline. Fetch dates are in [Sources checked](#sources-checked).

**Why this matters for pluto:** the map says pluto leases a box (worktree) to one host at a time, keeps disk state in a self-hosted S3-compatible bucket, and has "no membership, no failure detector, no consensus". This document establishes that this is not a novel unsound idea: it is exactly what [celld](https://github.com/denoland/celld) does, with a published protocol, a TLA+ model, and a live-fleet test suite. The rest of the document studies the agent/sandbox landscape, then the bucket feature that the whole design rests on (conditional writes), and finally maps the pattern onto pluto.

---

## Resolution in brief

1. **celld is the reference implementation of pluto's coordination model.** Nodes share one bucket. A **conditional write to the bucket assigns cell ownership**, the record carries a monotonic **fencing epoch**, and the node's own liveness is a separate **node lease with a TTL**. There is no membership protocol, no failure detector, and no consensus service; a dead node "releases" its cells by letting its lease expire, and a peer takes over with a compare-and-swap (CAS). ([celld README](https://github.com/denoland/celld/blob/main/README.md#how-it-works), [What celld guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md))
2. **The model has a hard substrate requirement: the bucket must implement conditional writes** (create-if-absent, overwrite-if-unchanged), read-after-write consistency, and ranged reads. celld ships an active storage test (`celld diagnose`) because stores can accept the headers and silently ignore the condition. ([guarantees, "What the bucket must provide"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#what-the-bucket-must-provide))
3. **Of the self-hosted stores in scope, Garage is disqualified by design** ("structurally impossible … due to the lack of a consensus algorithm"), **MinIO CE and SeaweedFS implement conditionals** (MinIO's regression where `If-Match` on an absent object was ignored was fixed in the 2025-09-07 release; SeaweedFS has hardened conditional writes from 4.09 through 4.18/4.46), and **Ceph RGW supports them with ongoing fixes**. celld qualifies only MinIO CE for its storage test, not for production. ([Garage known issues](https://garagehq.deuxfleurs.fr/documentation/reference-manual/known-issues/), [celld guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md), [MinIO #21526](https://github.com/minio/minio/issues/21526), [SeaweedFS 4.09/4.18/4.46 releases](https://github.com/seaweedfs/seaweedfs/releases), [SeaweedFS conditional-writes blog](https://seaweedfs.com/blog/conditional-writes/))
4. **Cloudflare Durable Objects are the inspiration but not the architecture celld copies.** Cloudflare runs a proprietary storage layer (Storage Relay Service) with 5 followers and 3-of-5 acknowledgements and fences an unreachable object by contacting a follower quorum; celld replaces that with 1–2 followers, an object-storage data path, and one CAS lease in the bucket. ([Cloudflare: Zero-latency SQLite](https://blog.cloudflare.com/sqlite-in-durable-objects/), [Easy, Fast, Correct](https://blog.cloudflare.com/durable-objects-easy-fast-correct-choose-three/))
5. **The agent/sandbox landscape confirms the split.** E2B coordinates with Postgres + Redis + distributed locks and memory snapshots; Anthropic documents process/dev-container sandboxing but **no multi-host lease/ownership model**; Fly uses a global control plane for placement and Corrosion (SWIM gossip + CRDTs) for service discovery, explicitly not consensus. None of them exposes the bucket-CAS pattern pluto wants; celld is the closest fit (its durable object is a SQLite database plus a single-threaded runtime, with no memory snapshot).
6. **For pluto, the model transfers with one major simplification.** celld must recover an acknowledged write tail from the previous owner's followers before takeover (its "recovery gate"); a disk-only, checkpointed box does not need that gate if pause/checkpoint only acknowledges after the state is in the bucket. Content-addressed chunks make stale-writer damage moot for block data; epoch fencing is still needed for the box's mutable manifest/checkpoint pointers.
7. **M0 (one host) does not need CAS at all**, but should keep the record shape and a monotonic epoch so the multi-host path is additive. celld itself ships `celld dev` with a local object store and the same protocol, which is the precedent to copy.

---

## 1. celld: the reference implementation

**What it is.** celld is Deno's open-source (Apache-2.0) daemon that runs Cloudflare Workers applications — Workers, Durable Objects, KV, Queues, D1, Workflows, Cron, static assets — on your own machines. Each Durable Object is a **cell**: a named server with its own SQLite database. Nodes that share one bucket are a **fleet**. Stack as described by the site: **V8 + S3 + SQLite + LTX + Tokio**. The current release at the time of writing is **v0.6.1 beta**. ([celld README](https://github.com/denoland/celld/blob/main/README.md), [celld.dev](https://celld.dev/), [celld docs](https://celld.dev/docs))

**Design claims, verbatim:**

> "A conditional bucket write gives a node the ownership of a cell, so exactly one node owns a cell at a time. The fleet needs no membership protocol, failure detector, or consensus service. Signed peer HTTP provides routing and replicated-log transport." — [celld README](https://github.com/denoland/celld/blob/main/README.md#how-it-works)

> "Exactly one node serves a cell at a time. The nodes do not elect a leader or keep a membership list: a node claims a cell by writing a small record to the bucket. The store accepts the write only if no other node changed the record first. Object storage therefore decides who wins, and two nodes cannot both claim the same cell. The claim expires unless the node renews it, so a failed machine releases its cells without a separate failure detector." — [celld docs, "Ownership and durability"](https://celld.dev/docs)

> "celld makes two promises about your data. Exactly one node serves a cell at a time, so two machines never write the same database. And celld does not answer a write until that write survives a failure, so nothing you were told succeeded is lost." — [What celld guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md)

### 1.1 The bucket contract

celld requires four properties from the object store, plus a fifth for epoch garbage collection:

| # | Property | Needed for |
|---|---|---|
| 1 | **Conditional create** (fail if the object exists) | claiming an unowned cell |
| 2 | **Conditional overwrite** (fail if the object changed since the read) | renewing/taking over an owned cell |
| 3 | **Read-after-write consistency** | observing your own claim before acting on it |
| 4 | **Ranged reads** (exact byte range) | restoring paged LTX data |
| 5 | **List-after-write consistency** | epoch GC (deleting superseded epoch prefixes) |

([guarantees, "What the bucket must provide"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#what-the-bucket-must-provide))

The store must be *tested*, not trusted. `celld diagnose` performs four conditional writes — create, reject-create, update, reject-stale — and **two must fail**. "A store can also accept the conditional headers and ignore the condition, and that store fails late and silently, so run the storage test below." Every node reruns the test before serving; a node *stops* when a required conditional write or ranged read is unsupported, and cannot disable the test. ([guarantees, "The storage test"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-storage-test))

Request dialect differs per provider: S3-compatible stores get `If-None-Match: *` and `If-Match: <etag>`; GCS uses the XML API with `x-goog-if-generation-match` (because "Cloud Storage does not apply `If-Match` to a PUT"); Azure uses the same `If-` headers via Put Blob. `AlreadyExists` and HTTP 412 are treated as *clean* rejections; every other error is **ambiguous**, because an ambiguous write can have changed the object. ([guarantees, "What the bucket must provide"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#what-the-bucket-must-provide))

### 1.2 Ownership records, node leases, and the acquire/release CAS

The source makes the shapes exact:

- **Cell ownership record:** key `cells/<cell>/own.json`, body `{"node": "<node-id>", "epoch": <u64>}`. Read returns an ETag. Acquire is `put_cas(key, body, guard)` where `guard = Absent` (conditional create) or `guard = Match(etag)` (compare-and-swap). Release writes `{"node": "", "epoch": <same epoch>}` under a CAS on the record this node wrote. ([`ownership_store.rs`](https://github.com/denoland/celld/blob/main/crates/celld/ownership_store.rs); [`OwnerWire` and `CasGuard`](https://github.com/denoland/celld/blob/main/crates/logic/types.rs))
- **A released record keeps its epoch:** `OwnerRecord.node: Option<NodeId>` — "`None` is a deliberately released, fenced record. Epochs never reset." ([`types.rs`](https://github.com/denoland/celld/blob/main/crates/logic/types.rs))
- **Epoch advances on every activation:** "Every activation advances the epoch, a takeover and a local wake alike. Each owner therefore replicates under a fresh epoch, and an epoch never has two writers." ([guarantees, "The ownership record"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-ownership-record))
- **Node (host) lease:** key `nodes/<node>.json`, carries `expires_ms`, address, probe key, protocol version, a folded log state, and load counters (owned cells, resident cells, RSS, pressure, restoring backlog). It is renewed at **one third of its lifetime** (`CELLD_TTL_MS`, default **10000 ms**). ([`NodeLeaseWire`](https://github.com/denoland/celld/blob/main/crates/celld/ownership_store.rs), [guarantees, "Self-fencing"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#self-fencing))
- **Cell ownership does not itself expire** — the *node lease* does. A peer that wants a dead node's cell reads the owner record, observes the expired node lease, and CASes the owner record to itself with `epoch + 1`.
- **Self-fencing:** a node fences itself when its published expiry passes, when its lease record is gone, or when the record no longer matches what it published. "The fence writes nothing to the bucket. Each peer already reads the lease as dead or replaced." The fenced node exits with code 3; a supervisor restarts it (and "must wait at least one lease lifetime between attempts"). ([guarantees, "Self-fencing"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#self-fencing))

Two robustness details worth copying: the CAS adapter classifies a failed write as `NotCommitted` (provably nothing written — retry with the same ETag) or `Ambiguous` (readback required), with "Ambiguous is the safe default"; and the node lease implementation does not trust the clock for acknowledgements — after a bucket proof it re-reads the ownership record and acknowledges only if it still names this node at this epoch. "The check reads the record instead of comparing a clock, so a paused process or a skewed clock cannot pass it." ([`LeaseCasError`](https://github.com/denoland/celld/blob/main/crates/celld/ownership_store.rs), [guarantees, "The acknowledgement rule"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-acknowledgement-rule-rpo0))

### 1.3 Epoch fencing: one writer, enforced by key layout

The fence is not just the record — it is the **storage prefix**:

> "The replicator copies each cell's SQLite data to the bucket under `cells/<cell>/ltx/e<epoch>/`, with plain unconditional PUTs. The epoch in the key is the fence: a node that lost ownership can keep writing, but its writes land in a superseded prefix, and a restore selects the current lineage." ([guarantees, "The epoch prefix"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-epoch-prefix))

A losing writer therefore cannot corrupt the winner's data even if it never notices it lost. This is the standard fencing-token pattern relocated into object keys. Restores walk the epoch prefixes newest-first, linking each epoch to its predecessor's cut ("epoch-chain restore"); **epoch GC** deletes prefixes below a base after a conditional `retired.json` write and requires list-after-write consistency. ([guarantees, "Epoch-chain restore" and "Epoch GC"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#epoch-chain-restore))

### 1.4 Durability: output gates and the bucket/fleet proof

celld ports Cloudflare's **output gate** idea to a self-hosted durability protocol: no response (and no outgoing network message that can reveal a write) is released until the write is proven durable, then ownership is re-verified. ([guarantees, "The acknowledgement rule"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-acknowledgement-rule-rpo0); the concept is Cloudflare's, see [Easy, Fast, Correct](https://blog.cloudflare.com/durable-objects-easy-fast-correct-choose-three/))

Two postures:

- **Bucket proof** (single node): upload to the bucket, then re-read the ownership record. RPO=0, latency = one storage round trip.
- **Fleet proof** (default `CELLD_DURABILITY=fleet`, needs ≥2 nodes): the owner sends each write to one or two **followers**; every follower fsyncs; the write is acknowledged once the ensemble holds it, and the bucket upload happens afterwards. A fleet of three or more holds three copies; losing one follower does not fall back to the bucket. ([guarantees, "The ensemble needs two nodes"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-ensemble-needs-two-nodes), [celld README](https://github.com/denoland/celld/blob/main/README.md))

Measured numbers from Deno's lab: one bucket proof ≈ **600 ms** against a non-region-local store, ≈ **90 ms** region-local; a fleet proof ≈ **25 ms**; failover after node loss ≈ **20 s** on the front page, with a 10-node lab test (4 vCPU / 8 GB nodes, 10,000 cells, SIGKILL two nodes) reporting every cell available again on another node in **~11 s at the tail**. ([celld.dev](https://celld.dev/), [What celld guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md), [Testing](https://github.com/denoland/celld/blob/main/docs/testing.md))

**Takeover recovery gate.** With fleet durability, an acknowledged write can exist only on the dead node's followers. Each process session therefore writes a conditional node-log record with states `open` / `recovering` / `sealed`; a cold activation of a non-sealed session must fence the record, collect the reachable followers' retained segments into the bucket, seal, and only then restore. A failed recovery backs off and retries for minutes. ([guarantees, "The takeover recovery gate"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#the-takeover-recovery-gate))

### 1.5 LTX / SQLite replication format

celld does not use Litestream as a process; it vendors and evolves a Rust replication library, `celld-ltx`, compiled into the node ("Replication runs inside the celld process, so a node needs no external replicator" — [celld docs](https://celld.dev/docs)).

- **LTX** is Superfly's **Lite Transaction File** format: a file carrying SQLite page frames (LZ4-compressed), a page index, min/max TXIDs, and checksums. Format v3 is fully documented in both upstream and celld's fork. ([superfly/ltx README](https://github.com/superfly/ltx/blob/main/README.md), [celld `ltx-format.md`](https://github.com/denoland/celld/blob/main/crates/ltx/reference/ltx-format.md))
- `celld-ltx` captures committed WAL data as L0 LTX segments, compacts through levels, creates snapshots, and can restore by **paging**: a fault-in SQLite VFS reads each page from segments on first use via a page map built from the segments' page indexes. ([`crates/ltx/README.md`](https://github.com/denoland/celld/blob/main/crates/ltx/README.md))
- Provenance is explicit: seeded from `rustyriver` (a from-scratch Rust reimplementation of Litestream v0.5), following Litestream tag v0.5.16 for the LTX v0.5.2 block format, with attribution to Litestream and Superfly. ([`crates/ltx/README.md`](https://github.com/denoland/celld/blob/main/crates/ltx/README.md))
- **A deliberate deletion:** Litestream's own object-storage **leaser** (`leaser.go`, `heartbeat.go`, `s3/leaser.go`) was ported into celld and then removed unused: "celld fences cell ownership with a conditional-write record carrying an epoch, and fences the data path by stamping that epoch into the LTX prefix; a lease file under the replica prefix would be a second, competing layer. Upstream's own leaser is unwired for the same reason." ([`crates/ltx/README.md`](https://github.com/denoland/celld/blob/main/crates/ltx/README.md)) This is a strong design signal: **two lease layers is a bug, not a belt**.

For reference, upstream Litestream's replication model is: take over SQLite checkpointing, package new WAL pages as LTX files named by TXID range, compact L0→L1→L2→L3 plus daily snapshots, and restore by replaying a contiguous TXID chain from the newest snapshot. ([Litestream: How it works](https://litestream.io/how-it-works/))

### 1.6 Hibernation and wake

Lifecycle in celld's terms: a **resident** cell is in memory (active or idle); an idle cell is evicted after `CELLD_IDLE_EVICT_S`, and if it keeps its hibernatable WebSocket clients it is **hibernated** (still owned by the same node, sockets parked); a cell no node holds is **inactive** — "only an object in the bucket, so it costs almost zero". A wake costs ~4 ms warm (celld.dev). ([celld docs, "Cell lifecycle"](https://celld.dev/docs), [celld.dev](https://celld.dev/))

Alarms are made durable with a **wake entry** object per committed installation (`wake/entries/`), an advisory fleet **waker lease** (`wake/waker.json`), and conditional-write retirement records; a node only wakes cells it owns except the elected waker, which may take over a dead owner's due cells. Alarm responses wait for the publication PUT. ([guarantees, "Alarm discovery and the wake format"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#alarm-discovery-and-the-wake-format))

Platform comparison: Cloudflare's hibernation keeps WebSocket clients connected while the object is evicted and bills no duration; per-connection state must be re-attached with `serializeAttachment`/`deserializeAttachment`. ([Cloudflare docs, Use WebSockets](https://developers.cloudflare.com/durable-objects/best-practices/websockets/)) celld reproduces the client-visible behavior: moving a hibernated cell closes parked WebSockets with code 1012 so clients reconnect to the new owner. ([celld README, balancing](https://github.com/denoland/celld/blob/main/README.md#operate-a-fleet))

### 1.7 Node-loss failover

The full path, assembled from the guarantees page:

1. Node stops renewing; its `nodes/<node>.json` lease's `expires_ms` passes.
2. Peers treat its cells as takeable; no explicit notification or detector exists.
3. A new owner CASes `cells/<cell>/own.json` to itself with `epoch + 1` (after, in fleet mode, the recovery gate of §1.4).
4. Restore composes the epoch chain from the bucket; the stale node, if alive, self-fences when it notices (and its late writes land in a superseded prefix).
5. The `celld diagnose` command can enumerate every node lease and probe live peers; `wake/retired/` and dead-node reconciliation clean up debris. ([guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md), [dead_node_reconciliation.rs](https://github.com/denoland/celld/blob/main/crates/logic/dead_node_reconciliation.rs))

The test suite attacks exactly these seams: SIGKILL mid-write with the local database deleted (recovery only from bucket), freeze an owner and write through others then unfreeze, cut a node off from the bucket (self-fence), throttle the bucket, and stop a host at the provider level. The protocol is also specified in TLA+; model checking found four bugs including "a split-brain that lost an acknowledged write", all fixed. ([Testing](https://github.com/denoland/celld/blob/main/docs/testing.md))

### 1.8 "No membership, no failure detector, no consensus" — what is actually true

- **No consensus service:** ownership is decided by the object store's linearizable compare-and-swap on a single key. That is the entire agreement mechanism. The bucket is a dependency, not a peer.
- **No membership protocol:** nodes discover each other by listing `nodes/` leases; there is no join command and no fixed membership list. ([celld docs, "Add nodes"](https://celld.dev/docs))
- **No failure detector:** failure is inferred from an expired wall-clock timestamp in the bucket lease; a node can be partitioned and still "alive", and the fleet will take its cells after the TTL. celld compensates with: epoch prefixes (stale writes are harmless), an ownership readback before any ack (a partitioned owner cannot successfully acknowledge), and self-fencing (the owner refuses to keep serving once it can't prove authority).
- **Caveats, stated rather than hidden:** celld's own testing page notes a fleet at its resident limit has no room for a lost node's cells and degrades; celld is v0.6.1 **beta**; restore timing for the current `celld-ltx` implementation is explicitly unpublished ("this page gives no number until a fleet run measures `celld-ltx`"). MinIO CE is not production-qualified by celld. The model-checked specification assumes a linearizable store and "perfect shared clocks" for finding bugs, which means the real design depends on wall-clock TTLs staying within an unspecified skew margin. ([Testing](https://github.com/denoland/celld/blob/main/docs/testing.md), [guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md))

**Security boundary** (relevant because pluto says "no trust model"): celld is explicitly not safe for hostile multi-tenancy. The bucket credentials control the fleet; the internal listener must stay on a trusted private network (WireGuard/Tailscale); peer traffic is plaintext with HMAC-authenticated control requests. celld's docs say to put advertised addresses on a private network or encrypted overlay. ([Security](https://github.com/denoland/celld/blob/main/docs/security.md), [Limitations](https://github.com/denoland/celld/blob/main/docs/limitations.md))

---

## 2. Cloudflare Durable Objects: inspiration and where celld differs

### 2.1 The model

- A Durable Object is a globally named, **single-threaded** Worker with private, strongly consistent storage that lives with the object; all messages for a name reach the same instance. Cloudflare automatically places, starts, and hibernates objects; a single object lives in **exactly one location at a time**. ([What are Durable Objects?](https://developers.cloudflare.com/durable-objects/concepts/what-are-durable-objects/), [the 2020 beta announcement](https://blog.cloudflare.com/introducing-workers-durable-objects/))
- **Input gates, output gates, automatic caching** (2021): while a storage op is outstanding, no other events are delivered; while a write is in progress, outgoing messages are held until the write completes; writes answer from cache and coalesce. These make naive `get`/`put` code correct and fast. ([Easy, Fast, Correct](https://blog.cloudflare.com/durable-objects-easy-fast-correct-choose-three/))
- **SQLite backend** (2024): synchronous SQL in the same thread; storage capped at 1 GB in beta (10 GB planned GA); point-in-time recovery for 30 days. ([Zero-latency SQLite](https://blog.cloudflare.com/sqlite-in-durable-objects/))
- **Hibernation:** an idle object is evicted from memory while WebSocket clients stay connected; events wake it, and per-connection state survives via attachments. ([docs](https://developers.cloudflare.com/durable-objects/best-practices/websockets/))

### 2.2 Storage Relay Service (SRS): the proprietary layer celld replaces

The same blog describes Cloudflare's internal durability layer, which is the most direct comparison point in the whole document:

- SQLite runs in WAL mode; SRS hooks the VFS, batches change logs (up to 10 s or 16 MB), uploads them to object storage (R2), and periodically uploads full snapshots (when logs exceed the DB size, capping storage at ~2× the database). "Credit where credit is due: this idea … was inspired by Litestream." ([Zero-latency SQLite](https://blog.cloudflare.com/sqlite-in-durable-objects/))
- **Every commit is also forwarded to five follower machines across the network; the write is confirmed once at least three of five acknowledge.** Followers hold the change on local disk and delete it when the bucket upload is confirmed; a follower that never hears the confirmation uploads the change itself. ([Zero-latency SQLite](https://blog.cloudflare.com/sqlite-in-durable-objects/))
- **Takeover when a host is unreachable:** contact at least three of five followers and tell them to stop confirming writes for that object, then start a replacement. "We cannot start up a new instance of the DO until we know for sure that the previous instance is dead – or, at least, that it can no longer confirm writes." ([Zero-latency SQLite](https://blog.cloudflare.com/sqlite-in-durable-objects/))

### 2.3 celld vs Cloudflare

| Dimension | Cloudflare DO | celld |
|---|---|---|
| Ownership decision | Cloudflare control plane (SRS owns the storage internals) | **CAS write to the customer's bucket** |
| Failure detection | SRS probes; takeover via follower quorum contact | Lease expiry in bucket; no detector |
| Write path / replication | 5 followers, 3-of-5 acks, then R2 batches | 1–2 followers (or bucket proof), then bucket upload |
| Placement | Cloudflare decides globally, migrates objects | Nodes claim cells from the bucket; advisory balancing by load |
| Storage engine | Proprietary SRS (WAL batches + snapshots to R2) | SQLite + **LTX** segments to the customer's bucket |
| Hibernation | Evict object, keep client WebSockets, wake on event | Evict isolate, keep hibernatable WebSockets, ~4 ms warm wake |
| Isolation / trust | Multi-tenant managed platform | One fleet, one app, **trusted**; not hostile-multi-tenant |
| Bucket requirement | N/A (managed) | Conditional writes + consistency; operator-tested |

celld is, in effect: keep the DO programming model and the output-gate/epoch-fencing discipline, replace the control plane and the 5-follower quorum with **one conditional-write record in a bucket the operator owns**, and accept that the operator's store is now part of the correctness boundary.

---

## 3. How agent / sandbox servers coordinate

### 3.1 E2B: Postgres + Redis + locks, with memory snapshots

E2B's runtime (repo now `e2b-dev/runtime`, formerly `e2b-dev/infra`; Apache-2.0) is the open-source backend behind E2B Cloud: control-plane API, per-node orchestrator driving Firecracker, in-VM `envd` agent, edge router, template builder. It can be self-hosted ("E2B Embed" = whole stack on one Linux host with KVM, via Docker Compose/Terraform/K8s), but the project labels Embed "an evaluation package, not a production deployment pattern". ([runtime README](https://github.com/e2b-dev/runtime/blob/main/README.md), [ARCHITECTURE.md](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md))

**Coordination, from the architecture doc:**

- **Control plane vs data plane are deliberately separate.** "The API decides *where* a sandbox runs and tracks *that* it runs (Postgres/Redis); the orchestrator on each node owns *how* it runs (Firecracker, networking, storage). Sandbox traffic never passes through the API." ([ARCHITECTURE.md](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md))
- **PostgreSQL** holds durable control-plane state: teams, users, quota/limits, templates (`envs`), builds, aliases, **snapshots (paused sandboxes)**, API keys, volumes, clusters. **Redis** holds *ephemeral runtime state*: the running-sandbox store ("source of truth" for running sandboxes), the sandbox→node routing catalog, caches, rate limiting, and the P2P chunk peer registry. **Object storage** holds template/snapshot artifacts (`{buildID}/memfile`, `rootfs.ext4`, `snapfile`, metadata). ([ARCHITECTURE.md, "Data stores"](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md))
- **Placement** is best-of-K over live nodes discovered via Nomad/Kubernetes/static list, scored by CPU and hugepage-pool load, with retries/backoff when a node refuses a create. ([ARCHITECTURE.md, "API"](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md))
- **Locks and reservations are Redis primitives, not object-store CAS.** The sandbox storage layer uses `bsm/redislock` distributed locks, with a pub/sub notification plus an exponential-backoff fallback because "PubSub is best-effort; the fallback ticker is required for correctness." The reservation layer uses a Lua script that atomically removes stale entries, enforces team limits, and records pending sandbox creations in a sorted set. ([`sandbox/storage/redis/lock.go`](https://github.com/e2b-dev/runtime/blob/main/packages/api/internal/sandbox/storage/redis/lock.go), [`sandbox/reservations/redis/README.md`](https://github.com/e2b-dev/runtime/blob/main/packages/api/internal/sandbox/reservations/redis/README.md))
- The routing record is written by the **orchestrator** on `MarkRunning` and deleted on `MarkStopping`, guarded by an `execution_id` in a Lua script "so a stale lifecycle never removes the record of a newer execution"; its TTL is the sandbox's max lifetime. ([ARCHITECTURE.md, "Sandbox routing records"](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md))

**Persistence and pause/resume:** a sandbox is a **resumed snapshot** — templates are pre-booted VMs (memory + disk + VM state) in object storage; memory pages load lazily on fault (`userfaultfd`) and rootfs is a copy-on-write overlay. Pause saves **both filesystem and memory** by default; `keepMemory: false` produces a **filesystem-only snapshot** that cold-boots on resume. Pause costs ≈4 s per GiB of RAM; resume ≈1 s. Paused sandboxes are kept indefinitely. Auto-pause/auto-resume make them serverless. ([persistence docs](https://e2b.dev/docs/sandbox/persistence), [runtime README](https://github.com/e2b-dev/runtime/blob/main/README.md), [ARCHITECTURE.md, "Pause and resume"](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md))

**Where a durable-object model would have fit:** E2B's running-sandbox truth lives in Redis with Redis locks; that is an extra always-on stateful service, and it is carefully partitioned from the durable Postgres/snapshot state. A bucket-CAS lease per sandbox could replace the Redis "running" map and its locks, letting any orchestrator claim a paused or orphaned sandbox from the bucket directly. E2B did not choose that route (it chose managed Redis + Postgres and memory snapshots), so it validates the need but not the pattern.

### 3.2 Anthropic: OS-level sandboxing, no documented cross-host lease

Three separate, documented patterns — none of them a fleet coordination protocol:

1. **Sandbox runtime (`srt`)** — an open-source (research preview) wrapper that enforces filesystem and network restrictions on arbitrary processes *without a container*: bubblewrap on Linux (with network namespace removed and traffic routed through host proxies over Unix sockets), Seatbelt on macOS, a dedicated user + Windows Filtering Platform fence on Windows. It sandboxes the process tree, supports dynamic network-policy updates via `--control-fd`, and is what Claude Code uses for the Bash tool. ([sandbox-runtime README](https://github.com/anthropic-experimental/sandbox-runtime), [Claude Code sandboxing docs](https://docs.claude.com/en/docs/claude-code/sandboxing), [engineering blog](https://www.anthropic.com/engineering/claude-code-sandboxing))
2. **Dev containers** — "an identical, isolated environment" (Docker container, potentially in Codespaces) where Claude Code and the toolchain run, with the repository bind-mounted. The docs recommend named volumes for `~/.claude`, egress firewalling inside the container, and non-root `--dangerously-skip-permissions`. State that must persist lives in the mounted workspace/volume; there is no coordination layer. ([devcontainer docs](https://docs.claude.com/en/docs/claude-code/devcontainer))
3. **Claude Code on the web** — an isolated cloud sandbox per session, with credential isolation: git operations go through a proxy that validates a scoped credential and the interaction (e.g. branch), attaching real upstream credentials outside the sandbox. The blog and docs do **not** document a lease/ownership model, multi-host placement, or how a sandbox on a failed host is recovered. ([engineering blog](https://www.anthropic.com/engineering/claude-code-sandboxing))

**Where a durable-object model would have fit:** the durable-object name/single-writer model is exactly what a "session" needs if sessions outlive a host; Anthropic's public material does not describe that problem, so this is an absence of evidence, not a contrary design. For pluto, the reusable part is the *boundary* discipline: isolate filesystem and network at the OS level, and keep credentials out of the box; the lease model is pluto's own layer.

### 3.3 Fly.io: control plane + gossip/CRDT, memory snapshots

- **Machine states** (persistent): `created`, `started`, `stopped` (exited), **`suspended`** ("suspended to disk; will attempt to resume on next start"), `failed`; transient `suspending`/`starting` etc. ([Machine states](https://fly.io/docs/machines/machine-states/))
- **Suspend/resume uses Firecracker snapshots** capturing CPU registers, memory, and open file handles; resume is "a few hundred ms" vs ~2 s cold boot. The snapshot is **not guaranteed to persist** — deployments, host migration, corruption, or maintenance discard it and force a cold start; an attached volume survives regardless; network connections may need reconnecting after resume. Fly recommends suspend only for ≤2 GB machines and warns that clock can lag until NTP syncs. ([Machine suspend and resume](https://fly.io/docs/reference/suspend-resume/))
- **They deliberately do not use consensus for discovery.** Corrosion "replaces Consul's central state database with eventually consistent state distributed across our hosts": each node keeps a SQLite database, gossips local changes, resolves conflicts with CR-SQLite CRDTs, and manages cluster membership with Foca's SWIM protocol. "Raft fell short for some use cases at Fly.io where round-trips to a centralized location are too expensive." ([Corrosion README](https://github.com/superfly/corrosion))

**Where a durable-object model would have fit:** Fly offers the closest *verbs* to pluto (suspend/start machine, auto start/stop, storage-only cost while suspended) but the opposite *architecture*: a global control plane owns placement and machine identity, and discovery uses SWIM membership + CRDTs. Fly is evidence that a fleet can avoid Raft for membership — but it is **not** evidence for "no failure detector": SWIM *is* a failure detector. pluto's "no membership, no detector" is only sound because the bucket, not a peer network, holds the authority.

### 3.4 Cross-cutting lessons

- Every system here separates **placement** (where it runs) from **ownership** (who may write). pluto can collapse both into one CAS record.
- The two ways to move a stateful unit are memory snapshot (E2B default, Fly suspend) and disk-only (E2B's `keepMemory:false` option, pluto's decision). Disk-only makes the ownership protocol simpler because there is no memory tail to recover, but it makes *when you may acknowledge a pause* the critical question.
- An extra coordination store (Redis, Consul, etcd) is a second failure domain that must itself be leased/replicated. celld's contribution is showing the object store you already need for data can be the coordination substrate.

---

## 4. Self-hosted S3-compatible stores and the conditional operations a lease needs

### 4.1 What the lease actually needs

From §1.1, reduced to the minimum for pluto:

1. `PUT` with `If-None-Match: *` — **create-once** (claim an unowned box).
2. `PUT` with `If-Match: <etag>` — **compare-and-swap** (renew, take over, release, commit a checkpoint pointer).
3. Read-after-write consistency — **you must see your own claim**.
4. `GET` (whole object is enough for pluto if disk state is chunked; LTX-style paging would add ranged reads).
5. `LIST` with list-after-write consistency — only if pluto garbage-collects superseded records/prefixes.
6. Honest failure behavior: reject a failed condition with 412/409; **never silently ignore the condition**. This is the property no vendor advertises, hence celld's probe.

AWS is the reference semantics: conditional writes were added for `PutObject`/`CompleteMultipartUpload` with `If-None-Match: *` and later `If-Match` (compare-and-swap), with `412`/`409` responses. ([AWS docs: conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html), [AWS What's New, conditional writes](https://www.amazonaws.cn/en/new/2024/amazon-s3-adds-new-functionality-for-conditional-writes/))

### 4.2 Garage — disqualified for leases, by design

Garage's own known-issues page, in the architectural-limitations section:

> "**No conditional writes / locking / WORM support (`if-none-match`, ...)** — This is structurally impossible to implement in Garage due to the lack of a consensus algorithm, which is one of Garage's core design choices which we cannot reconsider. … This means that many practical use-cases for `if-none-match` cannot be supported (e.g. using it to implement mutual exclusion between concurrent writers)." — [Garage known issues](https://garagehq.deuxfleurs.fr/documentation/reference-manual/known-issues/)

Garage also has no mutual exclusion for `CreateBucket` "due to the lack of a consensus algorithm". This is a clear, first-party answer: **Garage cannot host a CAS lease**. It can still store data (content-addressed chunks), but then the fleet needs a second coordination substrate — which celld's design explicitly refuses to have ("a second, competing layer", [`crates/ltx/README.md`](https://github.com/denoland/celld/blob/main/crates/ltx/README.md)). Garage is the recommended store in many self-hosting circles; for pluto it would force exactly the architecture pluto is trying to avoid.

### 4.3 MinIO — conditionals implemented; use a recent release; celld does not production-qualify it

- MinIO is AGPL-3.0; "high-performance, S3 compatible object store". ([repo metadata](https://github.com/minio/minio))
- In Aug–Sep 2025, MinIO community edition **ignored `If-Match` on a missing object** and created it; the bug was reported with a minimal repro, fixed by PR #21567, and confirmed fixed by the reporter on **`RELEASE.2025-09-07T16-13-09Z`**. The preceding release (`RELEASE.2025-09-06T17-38-46Z`) returned `NoSuchKey` for the conditional create of an absent object — this matches celld's note exactly. ([minio#21526](https://github.com/minio/minio/issues/21526), [PR #21567](https://github.com/minio/minio/pull/21567))
- celld's verdict on current MinIO CE: "implements the conditional writes and passes the storage test, but celld has not qualified it for production. One release is broken … Use RELEASE.2025-09-07T16-13-09Z or later." ([guarantees, "What the bucket must provide"](https://github.com/denoland/celld/blob/main/docs/guarantees.md#what-the-bucket-must-provide))
- MinIO's current documentation (branded AIStor) advertises conditional operations with `If-None-Match` on `GetObject`, `PutObject`, and `UploadPart`; that is the commercial edition's docs page, so treat CE support as evidenced by the source fix above plus celld's test, not by that page. ([AIStor S3 API compatibility](https://docs.min.io/aistor/developers/s3-api-compatibility/))

**Assessment:** the most likely drop-in for pluto's lease today, with two caveats: celld-grade production qualification must be done by pluto itself (run the four-write probe), and MinIO's community edition has had licensing/feature shifts (AGPL, feature gating in the commercial product) that matter for a self-hoster long-term.

### 4.4 SeaweedFS — conditionals implemented above the store, actively hardened

- SeaweedFS (Apache-2.0) claims S3 conditional writes (`If-None-Match: *`, `If-Match`, `If-Unmodified-Since`, conditional copy/delete, Object Lock). Its implementation does **not** require CAS from the metadata backend: gateways compute the object's **owner filer** on a consistent-hash ring, route the conditional write there, and serialize it with an in-process per-path lock. "The store did two ordinary things: a read and a write. It was never asked to compare-and-swap. The atomicity lives entirely in routing every writer to one owner plus that owner's local lock." ([SeaweedFS blog, "Conditional Writes Without a Compare-and-Swap Store"](https://seaweedfs.com/blog/conditional-writes/))
- Release history (open-source repo) shows the feature maturing:
  - **4.09** (2026-02-04): "Fix S3 conditional writes with versioning (Issue #8073)" (PR #8080).
  - **4.18** (2026-04-02): "s3api: make conditional mutations atomic and AWS-compatible" (PR #8802).
  - **4.46** (2026-09-08): "filer: route exclusive and conditional creates to the entry's ring owner" (PR #11109).
  ([releases](https://github.com/seaweedfs/seaweedfs/releases))
- The bug that 4.09 fixed: conditional write was broken when **versioning + locking** were enabled (the check read the non-versioned path); closed within a day in Jan 2026. ([issue #8073](https://github.com/seaweedfs/seaweedfs/issues/8073))
- Open-source vs Enterprise: the feature comparison table does not list conditional writes as enterprise-only; Enterprise adds data recovery/PITR/EC/zstd/etc. The blog is published by the commercial entity, but the release notes above are from the open-source repo. ([Open Source vs Enterprise](https://seaweedfs.com/docs/comparison/), [releases](https://github.com/seaweedfs/seaweedfs/releases))

**Assessment:** plausible self-hosted CAS substrate, especially because it can run on plain local disks without Ceph-scale machinery; the version floor should be `≥4.18`, ideally latest, and **not with bucket versioning + object locking enabled** until validated. Its owner-routing design is a useful fallback pattern in its own right (ownership by consistent hash + local lock) but that pattern requires a membership ring, which pluto doesn't want — only its S3 semantics matter here.

### 4.5 Ceph RGW — supported, documented thinly, under active repair

- Ceph's S3 object-ops reference documents `if-match`/`if-none-match` for GET/HEAD and `x-amz-copy-if-*` for COPY, but **does not document conditional PutObject headers** in the table fetched. ([Ceph objectops reference](https://docs.ceph.com/en/latest/radosgw/s3/objectops/))
- Source and PRs show conditional writes exist on the RADOS backend: the RGW SAL wrapper has `rgw_put_object_conditional`/`rgw_copy_object_conditional` with a comment "supports If-Match, If-None-Match"; a unit test notes "known fail: dbstore, posix — do not enforce conditional write preconditions", i.e. the RADOS path enforces them and other test backends don't. ([rgw_sal_wrapper.h](https://github.com/ceph/ceph/blob/main/src/rgw/rgw_sal_wrapper.h), [test_rgw_sal_wrapper.cc](https://github.com/ceph/ceph/blob/main/src/test/rgw/test_rgw_sal_wrapper.cc))
- Recent merged fixes: "squid: RGW | fix conditional Delete, MultiDelete and Put" (#65932, Sep 2026), "rgw: fix conditional writes on versioning-suspended buckets" (#71584, Sep 2026), "tentacle: RGW | fix conditional MultiWrite" (#67425, Mar 2026). The ceph/s3-tests tracking issue "Add conditional writes" is **still open** as of its last update (Nov 2025), indicating incomplete parity coverage. ([ceph PRs](https://github.com/ceph/ceph/pulls), [s3-tests#583](https://github.com/ceph/s3-tests/issues/583))

**Assessment:** a correct substrate if you already run Ceph, but a heavy one for a 12-core/16 GB first host (it also introduces a cluster of its own). If used, pin a recent release (Squid/Tentacle-era) and run the probe; treat the docs gap as a smell.

### 4.6 Stores celld reports as unqualified

celld states Backblaze B2, Hetzner Object Storage, and DigitalOcean Spaces "do not implement the required conditional writes. celld is not correct on such a store: two nodes can then own one cell." Tigris Global/Dual-region only provides read-after-write in the write's region (so no cross-region epoch GC). ([guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md#what-the-bucket-must-provide)) These are managed services and out of scope for pluto's self-host preference, but they explain why the feature cannot be assumed.

### 4.7 Fallback substrates if the object store cannot CAS

| Fallback | What it provides | Cost against pluto's stated preferences |
|---|---|---|
| **etcd** (lease + lock API) | canonical leases/locks/watch; primary docs: [create lease](https://etcd.io/docs/v3.5/tutorials/how-to-create-lease/), [create locks](https://etcd.io/docs/v3.5/tutorials/how-to-create-locks/) | Introduces an always-on consensus service — the thing the map says no; needs 1–3 hosts, own storage, own failure modes |
| **PostgreSQL advisory locks / row locks** | session- or transaction-scoped locks, no schema needed; [docs](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS) | Same objection as etcd, plus pg is not part of the fleet as designed; E2B shows this works at scale but it is a managed dependency there |
| **Consul sessions** | session + key locks | Same objection; also the system Fly explicitly moved away from |
| **Single coordinator host** (lock service on host #1) | no consensus, simple | Recreates a single point of failure and gives the coordinator host special status; if it dies, coordination stops until it returns — worse than bucket CAS because the bucket is already required for data |
| **Single-host only** (M0) | nothing needed; one writer by topology | No failover; loses nothing if the box state flushes to the bucket and any host can restore |
| **Store that CASes** (MinIO/SeaweedFS/Ceph) | the celld model exactly | The recommended path; the only real cost is operational, not architectural |

---

## 5. Mapping to pluto

Assumptions carried from the map: box = (project, branch) worktree; disk state is content-addressed in a self-hosted S3-compatible bucket; disk-only pause (no memory); git is the only data sync layer; no trust model; first host is a single x86_64 KVM machine on Tailscale; rare but not alien.

### 5.1 Two-level lease, celld-shaped

- `boxes/<box>/own.json` — `{"host": "<host-id>", "epoch": <u64>, "expires_ms": <wall clock>}`. Created with `If-None-Match: *`; renewed/taken/released with `If-Match: <etag>`. `epoch` increments on **every** activation (including same-host wake), never resets, and a released record is `{"host": "", "epoch": <unchanged>}`.
- `hosts/<host>.json` — host liveness + capability (address, protocol version, capacity, resident boxes). TTL on the order of 10–30 s, renewed at TTL/3; a host self-fences when its lease expires, is missing, or was replaced (celld's exact three triggers).
- A box's ownership becomes takeable when **its owning host's lease is expired**; the new host CASes `own.json` with `epoch + 1`. No cell record expiry field is needed (celld does not have one either).
- Before acknowledging anything (pause, checkpoint, handoff), re-read `own.json` and require it to still name this host at this epoch — the record check, not a clock check. This is the single most transferable line of celld's protocol.

### 5.2 Epoch fencing for content-addressed disk state

- Immutable, content-addressed chunks are naturally stale-writer-safe: a superseded host can only add blobs nobody references.
- The mutable pointers must be fenced. Put per-epoch manifests under `boxes/<box>/e<epoch>/…` (or stamp `epoch` into the manifest object) and make restore read the newest committed manifest lineage. A late writer with an old epoch writes into a prefix the restore never selects — celld's `cells/<cell>/ltx/e<epoch>/` pattern.
- The commit is a CAS on the manifest pointer (or a CAS on `own.json` before publishing the pointer). "Commit" means: chunks uploaded, manifest written, pointer advanced under CAS, then ack. Anything else risks acknowledging a pause that the next owner cannot reproduce.

### 5.3 Pause / handoff semantics — the main divergence from celld

celld must recover an *acknowledged SQLite write tail* from a dead owner's followers; that is why it has the recovery gate (§1.4). Pluto has no replicated tail to recover if it defines pause as a checkpoint:

- **Recommended semantics:** `pause` returns only after the box's dirty state is in the bucket and the pointer/ownership CAS has succeeded. Then any host can restore with no contact with the old one, and takeover needs **no recovery gate** — a large simplification and a genuine advantage of disk-only.
- **Alternative semantics:** checkpoint asynchronously and accept "RPO = last checkpoint" with git as the reconciliation layer. Simpler, but then pause is not a durability boundary and the box's state at pause time is not guaranteed. This should be a recorded decision, not an accident.
- Because processes die at pause anyway (disk-only), there is no memory tail and no "follower ensemble" requirement at all. A single-host fleet has the same correctness as a multi-host fleet for box state; the fleet only adds *availability*.

### 5.4 Hibernation and wake

- No hibernatable WebSockets / parked connections (pluto has no UI and boxes are microVMs); wake = restore chunks, start VM, run the project's start command. celld's ~4 ms wake is an object-in-memory number, not a VM-boot number; pluto should measure cold restore and keep a local chunk/VM cache for same-host wake.
- Mirror celld's distinction between **idle eviction that keeps ownership** (fast same-host wake) and **release** (hand the box back so any host may take it). Pluto's "stop" should default to keeping the lease while the host is alive; only pressure/handoff/expiry releases it.

### 5.5 Node-loss failover

Same shape as celld: host dies → its `hosts/` lease expires → another host reading `boxes/<box>/own.json` sees a dead owner → CAS to itself with a new epoch → restore the last committed manifest → boot. No failure detector, no membership protocol, no quorum. The stale host, if it comes back, self-fences on lease mismatch (and its uploads are fenced by epoch). Tailscale + NTP keeps clock skew in the seconds range; the TTL margin must exceed realistic skew (celld does not document its margin — treat as an open question, and run the numbers at design time).

### 5.6 Fleet discovery and placement

celld's nodes find each other by listing `hosts/` leases and read a shared capacity sample for balancing; placement is advisory and every placement races for the box anyway. Pluto can start even simpler: any host can restore any box it can reach in the bucket; route requests by reading `own.json` and forwarding over Tailscale (celld uses signed peer HTTP + HMAC because its internal listener sees untrusted hostnames; Tailscale ACLs can replace much of that, but the bucket is still "fleet administrator access" — keep bucket credentials tight).

### 5.7 Single-host M0

- With one host there is no contention, so **CAS is not strictly needed**. Options: (a) local JSON/SQLite state + lock file, mirroring `celld dev`'s local object store ([celld docs](https://celld.dev/docs)); (b) still write bucket records with the same schema, which exercises the real path from day one (the CAS always succeeds against itself).
- Either way, keep the `own.json` shape, the monotonic epoch, and the `hosts/` record. Multi-host then becomes: flip the backend to bucket CAS, run the storage probe, add the TTL renewal loop. No data-model migration.
- Recommended M0 store choice: if a bucket is already needed for disk state, choose the CAS-capable one now (MinIO CE ≥ 2025-09-07 or SeaweedFS ≥ 4.18) rather than adopting Garage and discovering the wall later. The storage probe should run at host startup exactly as celld does, and **stop the host** if the four writes don't behave.

### 5.8 What to port directly

1. `put_cas(key, body, Absent | Match(etag))` as the only coordination primitive; classify failures `NotCommitted` vs `Ambiguous`.
2. Epoch monotonicity: every activation (takeover or local wake) increments; released records keep their epoch.
3. Epoch-qualified prefixes for mutable box state; unconditional PUTs inside a prefix.
4. Ownership readback before every acknowledgement; self-fence on lease expiry/missing/mismatch; supervisor restarts with a full-lease-lifetime backoff.
5. A startup storage probe with four conditional writes and a ranged read (pluto can skip ranged reads unless disk state pulls it in); never trust a store's compatibility table.
6. The decision to have exactly **one** lease layer (no Litestream-style leaser file under the data prefix).

---

## 6. Open questions and uncertainty

1. **Pause durability contract.** Does pluto guarantee "all disk state at pause time survives", or "last checkpoint survives"? The former requires the upload-then-CAS-then-ack gate of §5.3; the latter is cheaper but must be explicit. This is the biggest unresolved design lever.
2. **Disk-state format.** This ticket assumed content-addressed chunks. Whether the box is an ext4 image plus overlay diffs or a file-level CAS tree decides whether ranged reads, list consistency, and manifest GC are needed. (Belongs with the disk-state/storage ticket.)
3. **Store selection and qualification.** MinIO CE is unqualified by celld for production; SeaweedFS is younger but actively hardened; Ceph is heavy. No self-hosted store has a *celld-grade* public qualification yet. Pluto should own a small probe suite.
4. **Clock skew.** Lease TTLs compare wall clocks across hosts. celld does not document a skew budget. Decide TTL margins (and measure Tailscale/NTP behavior) before trusting 10 s leases.
5. **GC of superseded epochs/manifests.** Needs list-after-write consistency; Garage-style stores can't. Decide GC policy early (manual vs automatic) because it affects the store requirements.
6. **Box identity and forks.** box id = f(project, branch) means a fork/branch rename changes identity; epochs must never reset across id reuse. Decide the id scheme (content hash + immutable uuid).
7. **Host-level capacity/pressure.** celld sheds cells under memory pressure and balances hibernated cells. Pluto boxes are microVMs with much bigger footprints; placement/shedding policy is a separate research ticket.
8. **Security surface.** celld's bucket creds = fleet admin. With no trust model, Tailscale ACLs + a fleet-scoped bucket credential may be enough; decide whether peer HTTP needs celld's HMAC-at-tunnel-establishment or plain Tailscale.
9. **celld's restore-time number is unpublished** (`docs/testing.md` explicitly withholds it), so failure-recovery UX for pluto must be measured on its own VMM, not extrapolated from celld's wake/failover numbers.
10. **Anthropic's cloud sandbox internals are undocumented**; the statements in §3.2 are limited to published behavior. Do not treat the absence of a lease model there as evidence about its internals.

---

## Sources checked

Fetched 2026-10-04 unless noted.

**celld / Deno**
- [celld README](https://github.com/denoland/celld/blob/main/README.md) — architecture, fleet model, durability modes, balancing, commands
- [docs/guarantees.md](https://github.com/denoland/celld/blob/main/docs/guarantees.md) — bucket contract, storage test, ownership records, epochs, ack rule, recovery gate, epoch GC, self-fencing, wake format
- [docs/limitations.md](https://github.com/denoland/celld/blob/main/docs/limitations.md), [docs/security.md](https://github.com/denoland/celld/blob/main/docs/security.md) — beta boundary, trust model, listeners, bucket authority
- [docs/testing.md](https://github.com/denoland/celld/blob/main/docs/testing.md) — TLA+ spec, simulation, live-fleet fault injection, measured numbers
- [celld.dev](https://celld.dev/) — v0.6.1, headline reliability/cost numbers
- [celld.dev/docs](https://celld.dev/docs) — cell lifecycle, ownership, alarms, operators, object-storage configuration
- [crates/celld/ownership_store.rs](https://github.com/denoland/celld/blob/main/crates/celld/ownership_store.rs) — lease record schema, `put_cas`, `LeaseCasError`, node leases
- [crates/logic/types.rs](https://github.com/denoland/celld/blob/main/crates/logic/types.rs) — `OwnerRecord`, `NodeLeaseRecord`, `CasGuard`, effects, halt reasons
- [crates/logic/dead_node_reconciliation.rs](https://github.com/denoland/celld/blob/main/crates/logic/dead_node_reconciliation.rs) — dead-node debris rules
- [crates/ltx/README.md](https://github.com/denoland/celld/blob/main/crates/ltx/README.md) — celld-ltx provenance, removed Litestream leaser, compatibility
- [crates/ltx/reference/ltx-format.md](https://github.com/denoland/celld/blob/main/crates/ltx/reference/ltx-format.md) — LTX v3 byte layout

**Cloudflare**
- [What are Durable Objects?](https://developers.cloudflare.com/durable-objects/concepts/what-are-durable-objects/)
- [Workers Durable Objects Beta (2020)](https://blog.cloudflare.com/introducing-workers-durable-objects/)
- [Durable Objects: Easy, Fast, Correct — Choose three (2021)](https://blog.cloudflare.com/durable-objects-easy-fast-correct-choose-three/)
- [Zero-latency SQLite storage in every Durable Object (2024)](https://blog.cloudflare.com/sqlite-in-durable-objects/) — SRS, 5 followers/3-of-5 acks, takeover via follower quorum
- [Use WebSockets (hibernation)](https://developers.cloudflare.com/durable-objects/best-practices/websockets/)

**Agent / sandbox servers**
- [E2B runtime README](https://github.com/e2b-dev/runtime/blob/main/README.md) — services, pause/resume, Embed self-hosting caveat
- [E2B ARCHITECTURE.md](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md) — state stores, placement, routing records, pause/resume, fork
- [E2B Redis storage lock.go](https://github.com/e2b-dev/runtime/blob/main/packages/api/internal/sandbox/storage/redis/lock.go), [reservations README](https://github.com/e2b-dev/runtime/blob/main/packages/api/internal/sandbox/reservations/redis/README.md) — Redis locks and Lua reservations
- [E2B sandbox persistence](https://e2b.dev/docs/sandbox/persistence) — memory vs filesystem-only pause, timings
- [Anthropic: sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime), [Claude Code sandboxing docs](https://docs.claude.com/en/docs/claude-code/sandboxing), [engineering blog (2025-10-20)](https://www.anthropic.com/engineering/claude-code-sandboxing), [devcontainer docs](https://docs.claude.com/en/docs/claude-code/devcontainer)
- [Fly: Machine states](https://fly.io/docs/machines/machine-states/), [Machine suspend and resume](https://fly.io/docs/reference/suspend-resume/), [Corrosion README](https://github.com/superfly/corrosion)

**Object stores and conditional writes**
- [AWS S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html) — reference semantics
- [Garage known issues](https://garagehq.deuxfleurs.fr/documentation/reference-manual/known-issues/) — no conditional writes, structurally
- [minio#21526](https://github.com/minio/minio/issues/21526) and comments, [PR #21567](https://github.com/minio/minio/pull/21567), [AIStor S3 compatibility](https://docs.min.io/aistor/developers/s3-api-compatibility/), [repo license](https://github.com/minio/minio)
- [SeaweedFS conditional writes blog (2026-07-16)](https://seaweedfs.com/blog/conditional-writes/), [release 4.09/4.18/4.46](https://github.com/seaweedfs/seaweedfs/releases), [issue #8073](https://github.com/seaweedfs/seaweedfs/issues/8073), [OSS vs Enterprise](https://seaweedfs.com/docs/comparison/)
- [Ceph objectops reference](https://docs.ceph.com/en/latest/radosgw/s3/objectops/), [rgw_sal_wrapper.h](https://github.com/ceph/ceph/blob/main/src/rgw/rgw_sal_wrapper.h), [test_rgw_sal_wrapper.cc](https://github.com/ceph/ceph/blob/main/src/test/rgw/test_rgw_sal_wrapper.cc), [s3-tests#583](https://github.com/ceph/s3-tests/issues/583), merged PRs #65932/#71584/#67425
- [etcd: create lease](https://etcd.io/docs/v3.5/tutorials/how-to-create-lease/), [create locks](https://etcd.io/docs/v3.5/tutorials/how-to-create-locks/), [PostgreSQL advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)

**Format lineage**
- [superfly/ltx README](https://github.com/superfly/ltx/blob/main/README.md), [Litestream: How it works](https://litestream.io/how-it-works/)
