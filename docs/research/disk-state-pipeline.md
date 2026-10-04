# Research: disk-state pipeline

Research output for pluto ticket [#5](https://github.com/Siddhj2206/pluto/issues/5) (wayfinder map [#1](https://github.com/Siddhj2206/pluto/issues/1)). Branch `research/disk-state-pipeline`.

Scope: how to capture, ship, and deduplicate a box's disk state, disk-only. What follows is the storage half of the architecture: VM disk layering, chunked content-addressed storage, incremental/continuous replication, object-store layout, and a recommended v1 and v2. The base-image half (BuildStream → microVM images, distribution) is ticket [#7](https://github.com/Siddhj2206/pluto/issues/7); VMM choice is ticket [#2](https://github.com/Siddhj2206/pluto/issues/2). This document is written to not depend on either outcome where possible.

## Decision summary

- **v1 box disk**: a raw sparse image file. Raw is the only format every candidate VMM consumes, it is the exact byte stream to chunk, and it needs no base-image coupling at pause time.
- **Capture engine**: `desync` ([folbricht/desync](https://github.com/folbricht/desync)) — actively maintained Go implementation of the casync formats, with native S3 stores, parallel content-defined chunking, seed-based extraction, and reflink-aware materialization.
- **v1 pipeline**: pause → quiesce/flush → stop VM → fsync image → `desync make` chunks the whole image into the bucket (only unseen chunks are uploaded) → commit a manifest object. Resume: `desync extract` with the local chunk cache and last materialized image as seeds, then boot. Full-image chunking each pause is cheap on this host (scan, not upload) and keeps v1 free of qcow2, dirty bitmaps, and backing chains.
- **v2 pipeline**: switch the working disk to a qcow2 (compat 1.1) active layer so QEMU can keep a **persistent dirty bitmap**; run `blockdev-backup sync=incremental` on an interval to local staging deltas and ship those as opaque objects, giving an RPO of the backup interval. Pause-time full-image `desync` checkpoints stay the canonical durable units; deltas are crash insurance between pauses. Migration is a one-time `qemu-img convert` at a pause plus one full re-sync; the chunk store and manifest model do not change.
- **Bucket**: [Garage](https://garagehq.deuxfleurs.fr) single-node first ([`--single-node`](https://garagehq.deuxfleurs.fr/documentation/quick-start/)), S3-compatible, AGPL. Do not build a new project on MinIO CE: the upstream repo is [archived](https://api.github.com/repos/minio/minio) and the community console was stripped in 2025 ([secondary](https://www.blocksandfiles.com/ai-ml/2025/06/19/minio-users-complain-after-admin-ui-removed-from-community-edition/1610856)).
- **RPO without memory snapshots** is a data-window problem only: at resume the box always boots fresh over a crash-consistent or quiesced filesystem; process state is never restored. Pause-only gives RPO = time since last pause; interval backups give RPO ≈ interval (plus bitmap-loss fallback, below).

---

## 1. VM disk layering

### 1.1 qcow2

qcow2 is a host-cluster-addressed copy-on-write image format. A fixed-size header points at an L1 table, whose entries point at L2 tables, whose entries map guest clusters to host clusters; refcounts are kept in a two-level refcount table. All allocations happen at cluster granularity and, since version 3 (`compat=1.1`), all-zero clusters are represented without allocating data clusters ([QEMU qcow2 spec](https://www.qemu.org/docs/master/interop/qcow2.html)). Cluster size is configurable between 512 B and 2 MiB ([`qemu-img(1)`](https://www.qemu.org/docs/master/tools/qemu-img.html)); QEMU's default is 64 KiB, which is also the default granularity of its dirty bitmaps ([QEMU bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). Newer QEMU can split standard clusters into 32 subclusters with the extended-L2-entries feature, which reduces the cost of small random writes at the price of a different L2 layout (qcow2 spec).

Backing chains are external overlays: an overlay records only the differences from its backing file, unallocated clusters read through to the backing file ([qcow2 spec](https://www.qemu.org/docs/master/interop/qcow2.html)). QEMU can create overlays live (`blockdev-snapshot-sync`), merge them down (`block-commit`), flatten them up (`block-stream`), mirror them (`blockdev-mirror`), and back them up at a point in time (`blockdev-backup`) ([QEMU live block operations](https://www.qemu.org/docs/master/interop/live-block-operations.html)). `qemu-img` can create, commit, and rebase chains offline; `qemu-img rebase` has an unsafe mode that just rewrites the backing-file pointer and a "diff" recipe that picks the delta between two images ([`qemu-img(1)`](https://www.qemu.org/docs/master/tools/qemu-img.html)).

Two properties matter for pluto:

- **Persistent dirty bitmaps only exist in qcow2** (and only in version-3/compat-1.1 images). Transient bitmaps can track any format but are discarded on exit ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). This is the gateway to incremental capture (section 3).
- **qcow2 metadata is not a good dedup substrate.** L1/L2/refcount tables are scattered through the file and rewritten on allocation changes, and compressed clusters are not cluster-aligned (qcow2 spec). Chunking a qcow2 file byte-for-byte finds less stable data than chunking the guest-visible raw bytes. qcow2 is a working format; store raw.

Known qcow2 operational hazards: lazy refcounts leave the image needing an automatic `qemu-img check -r all` after a host crash ([`qemu-img(1)`](https://www.qemu.org/docs/master/tools/qemu-img.html)); deep chains are fragile and slow, and the guidance is to keep the chain short and commit/stream excess overlays away (live block ops doc); on btrfs, QEMU recommends `nocow=on` because CoW filesystems fragment VM images badly ([`qemu-img(1)`](https://www.qemu.org/docs/master/tools/qemu-img.html)).

### 1.2 raw images

A raw image is the guest-visible bytes, nothing else. On a filesystem with holes, unwritten sectors take no space ([`qemu-img(1)`](https://www.qemu.org/docs/master/tools/qemu-img.html)). There is no metadata to churn, no chain, no repair tooling needed, and every candidate VMM accepts it. This is the natural storage form for pluto: it is both the thing the VMM consumes and the thing the chunker sees. Sparse holes read as zeros, which content-defined chunking plus desync's null seed handles for free ([desync concepts](https://github.com/folbricht/desync/blob/master/docs/concepts.md)).

### 1.3 btrfs and zfs reflinks/snapshots

btrfs reflinks share file extents between files with independent metadata; `cp --reflink=always` is the API. Constraints: same filesystem (cross-mount reflinks only since kernel 5.18), and NOCOW/checksum status must match between source and target ([btrfs Reflink doc](https://btrfs.readthedocs.io/en/latest/Reflink.html)). A btrfs snapshot is a subvolume with initial content that shares all blocks, is file-extent-based (not block-level like LVM), and is not recursive into nested subvolumes ([`btrfs-subvolume(8)`](https://btrfs.readthedocs.io/en/latest/btrfs-subvolume.html)). `btrfs send -p` emits a checksummed incremental stream against a reference subvolume ([btrfs send/receive](https://btrfs.readthedocs.io/en/latest/Send-receive.html)).

zfs snapshots are atomic point-in-time images that include all completed system calls; recursive snapshots share one point in time ([`zfs-snapshot(8)`](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-snapshot.8.html)). `zfs send -i` is incremental, `-c` sends compressed blocks, `-w` sends encrypted blocks raw, and a partially received stream can be resumed from a receive-resume token ([`zfs-send(8)`](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-send.8.html)).

For a *live* VM, both filesystems snapshot the disk file atomically, but neither knows anything about the guest: a filesystem snapshot of a running VM's image is at best crash-consistent, and the guest's journal must recover it (see §3.3). They are attractive as local mechanics — reflink a box image before a risky pause, age out local snapshots, stream `btrfs send`/`zfs send` to a replica — but they require the *host* filesystem to be btrfs/zfs, and they are not a portable capture format. pluto should treat them as optional local accelerators, not the sync layer.

### 1.4 Copy-on-write properties compared

| Mechanism | Allocation unit | Space amplification | Chunk-dedup friendliness | VMM/format support |
| --- | --- | --- | --- | --- |
| raw + sparse holes | 4 KiB host blocks / holes | none beyond written data | highest: exact guest bytes | universal |
| qcow2 overlay | 64 KiB clusters (default) | metadata + per-cluster COW | poor in qcow2 bytes; store raw instead | QEMU native; Cloud Hypervisor; crosvm; not Firecracker |
| btrfs reflink/snapshot | extent-based, file-granular | shared extents; fragmentation on COW writes | good for local copies; not a wire format | host-side only |
| zfs snapshot/clone | block-pointer CoW, `recordsize` | shared blocks; send streams are opaque | good for local/`send` streams | host-side only |

### 1.5 What VMMs support

- **QEMU/KVM**: raw and qcow2 natively; only qcow2 supports persistent bitmaps; full live block-operation suite ([bitmaps](https://www.qemu.org/docs/master/interop/bitmaps.html), [live block ops](https://www.qemu.org/docs/master/interop/live-block-operations.html)).
- **cloud-hypervisor**: disks are `--disk` `path=...,image_type=raw|qcow2`; image-type auto-detection is deprecated in favour of explicit types, and v52 added a `QcowDiskAsync` backend using io_uring ([v52 release notes](https://www.cloudhypervisor.org/blog/cloud-hypervisor-v52.0-released)). Snapshot/restore exists but is VM-state oriented; its own docs say the disk is not part of the snapshot and must be copied by the caller while the VM is paused ([snapshot/restore doc](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/snapshot_restore.md)). vhost-user-blk is supported for external backends ([device model doc](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/device_model.md)).
- **crosvm**: virtio-block supports raw, qcow2, zstd, and Android sparse images ([crosvm README](https://github.com/google/crosvm)); VM snapshotting is highly experimental ([crosvm book](https://crosvm.dev/book/architecture/snapshotting.html)).
- **Firecracker**: block devices are backed by files on the host that already contain the filesystem the guest will mount ([design doc](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md)); the getting-started image is an ext4 file ([docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)). It does not support qcow2; a secondary source states raw-only explicitly ([blog, secondary](http://blakesmith.me/2026/05/03/cloud-hypervisor-is-awesome.html)). Treat Firecracker as raw-only.

Of the candidates, **only QEMU exposes persistent dirty bitmaps** for block-level change tracking; cloud-hypervisor's snapshot API is VM-state oriented and leaves the disk to the caller ([snapshot/restore doc](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/snapshot_restore.md)), Firecracker's snapshot support is whole-VM/state oriented ([design doc](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md)), and crosvm's snapshotting is experimental ([crosvm book](https://crosvm.dev/book/architecture/snapshotting.html)). Continuous incremental capture therefore hinges on the VMM choice.

**Consequence:** raw is the storage form that survives every VMM decision. qcow2 is an optional working layer for QEMU-only incremental capture.

---

## 2. Chunked content-addressed storage

### 2.1 casync: the formats

casync's model is: serialize (a directory tree into a `.catar`, or take a raw blob), split the byte stream into variable chunks with a content-defined chunker, store chunks in a flat chunk store keyed by hash, and publish a small index describing the sequence ([casync README](https://github.com/systemd/casync)).

- **Chunker**: buzhash rolling hash with a 48-byte window. Default average chunk size is 64 KiB, with minimum = avg/4 (16 KiB) and maximum = avg×4 (256 KiB); the cut discriminator is adjusted because untrimmed content-defined cuts produce ~1.32× the configured average. Cuts happen when `hash mod discriminator == discriminator-1` after the minimum, with a hard cut at the maximum ([cachunker.h](https://github.com/systemd/casync/blob/main/src/cachunker.h), [cachunker.c](https://github.com/systemd/casync/blob/main/src/cachunker.c)). A fixed-size mode is available when min = avg = max.
- **Chunk identity**: SHA512/256 by default (SHA256 selectable), 32-byte IDs, computed over the *uncompressed* chunk ([README](https://github.com/systemd/casync), [cachunkid.h](https://github.com/systemd/casync/blob/main/src/cachunkid.h)).
- **Compression**: zstd by default, xz and gzip selectable; chunks are stored with a `.cacnk` suffix and may be mixed in one store ([casync 2 release notes](https://github.com/systemd/casync/releases/tag/v2)).
- **Index (`.caibx`)**: a `CA_FORMAT_INDEX` header carrying feature flags (including which digest is in use) and the min/avg/max chunk sizes, followed by a `CA_FORMAT_TABLE`: entries of `(offset, chunk_id[32])` plus a tail with the index offset and a marker. Chunk *lengths* are implicit in the next entry's offset ([caformat.h](https://github.com/systemd/casync/blob/main/src/caformat.h)). This is why a `.caibx` is tiny relative to the image.
- **catar**: a little-endian tagged stream: 64-bit `(size, type)` headers at every object, with typed records (`ENTRY`, `USER`, `GROUP`, `XATTR`, ACL records, `FCAPS`, `PAYLOAD` for file data, `SYMLINK`, `DEVICE`, `FILENAME`, and a `GOODBYE` directory lookup table at the end of each directory) ([caformat.h](https://github.com/systemd/casync/blob/main/src/caformat.h)). Feature flags cover permissions, ACLs, SELinux, xattrs, subvolume bits, etc.
- **Seeds**: `casync extract --seed=<dir> <index> <target>` reuses matching data found in a local tree instead of downloading it ([README](https://github.com/systemd/casync)). `casync gc` prunes a store against a set of indexes ([README](https://github.com/systemd/casync)).
- **Maintenance**: casync's last release is v2 from **2017-07-26** ([GitHub API](https://api.github.com/repos/systemd/casync/releases/latest)); the last commit in the repo is a 2025-09 mtree fix ([commit list](https://api.github.com/repos/systemd/casync/commits?per_page=1)). It works, but it is not moving.

### 2.2 desync

`desync` implements the casync formats and interoperates with them (same index files, catar archives, chunk stores) with parallel chunking, more store backends, and a Go library API ([README](https://github.com/folbricht/desync)). Relevant properties:

- **Store backends**: local, HTTP(S), **S3/GCS**, SFTP, SSH, OCI registries; indexes can also be stored remotely; stores can be chained and fronted by a cache ([stores doc](https://github.com/folbricht/desync/blob/master/docs/stores.md), [S3 store doc](https://github.com/folbricht/desync/blob/master/docs/stores-s3.md)). An S3 store URL looks like `s3+http://127.0.0.1:9000/store` ([S3 store doc](https://github.com/folbricht/desync/blob/master/docs/stores-s3.md)).
- **Chunker defaults match casync**: min 16 KiB, avg 64 KiB, max 256 KiB; min must be at least the 48-byte rolling-hash window ([concepts](https://github.com/folbricht/desync/blob/master/docs/concepts.md)).
- **Compression**: zstd only, compressed chunks by default; uncompressed stores are supported and configurable per store; `chunk-server -u` serves uncompressed ([stores doc](https://github.com/folbricht/desync/blob/master/docs/stores.md)).
- **Seeds**: null seed (zero chunks → sparse output), self seed (repeated sections cloned from the growing output), and file seeds (an older image plus its `.caibx`); on btrfs/XFS matching sections are reflinked rather than copied ([concepts](https://github.com/folbricht/desync/blob/master/docs/concepts.md)). CLI examples: `desync extract -s <store> -c <cache> --seed <old.caibx> <new.caibx> <out>` ([README](https://github.com/folbricht/desync)). This is the deliberate difference from casync: casync re-scans seed files at extraction time to find reusable data, while desync takes each seed's existing index and keeps an explicit local chunk cache, trading disk space for speed ([README, "Differences from casync"](https://github.com/folbricht/desync)).
- **FUSE and streaming**: `mount-index` exposes a published index as a file, fetching chunks on read ([README](https://github.com/folbricht/desync)).
- **GC and verification**: `prune` removes chunks not referenced by the listed indexes; `verify-index`, `verify` check integrity; `verify -r` evicts bad chunks from local stores ([README](https://github.com/folbricht/desync), [stores doc](https://github.com/folbricht/desync/blob/master/docs/stores.md)).
- **Concurrency**: adaptive per-store request limits from 10 up to 128 while throughput grows; `-n -1` uses one goroutine per CPU for chunking ([concepts](https://github.com/folbricht/desync/blob/master/docs/concepts.md)).
- **Maintenance**: v1.1.4 released **2026-09-22** ([GitHub API](https://api.github.com/repos/folbricht/desync/releases/latest)); this is the implementation to build on.

### 2.3 How much dedup actually buys

desync's README publishes a measurement over six builds of the Debian 12 `genericcloud` disk (3.2 GB raw, ~1 GB of data) in one store: all six builds occupy **832 MB** versus 1,753 MB for six separate copies; a client updating from the build two days earlier downloads **109.8 MB**, from six months earlier **165.4 MB**, and a cold client **292.2 MB** ([README](https://github.com/folbricht/desync)). Those are compressed chunk sizes. The lesson for pluto: version-to-version and branch-to-branch reuse is good when unchanged data stays byte-identical; it is poor when the payload is compressed or encrypted, because a one-byte input change rewrites everything downstream.

For raw disk images the unchanged data is substantial: base filesystem, toolchains, git objects, and any per-project cache that two branches share. Ext4 metadata (journals, allocation bitmaps, timestamps, inode churn) is the noisy part; content-defined chunking confines that damage to one or two chunks per modified region, which is exactly what CDC is for. Chunking a *mountable filesystem tree* (catar) would dedup directories better but requires mounting/unmounting the guest filesystem on every pause and is not how the VMM consumes the disk. Chunking a *qcow2* image is the weakest option (§1.1). **Chunk the raw bytes.**

### 2.4 borg and restic compared

- **restic** is a content-addressed backup repository: SHA-256 IDs, packs with encrypted blobs plus an encrypted header at the end, JSON index files, snapshot/tree objects, strict write ordering (packs → indexes → snapshots) so a crash never exposes a snapshot whose data is missing, and `prune` under an exclusive lock ([restic references](https://restic.readthedocs.io/en/stable/100_references.html)). Chunking is Rabin-fingerprint CDC with a 64-byte window; files below 512 KiB are not split; blobs are 512 KiB–8 MiB, aiming at 1 MiB ([restic references](https://restic.readthedocs.io/en/stable/100_references.html)).
- **borg** is a transactional key-value repository of append-only segments with PUT/DELETE/COMMIT tags, index/hints files, conditional compaction, AES-CTR + HMAC encryption, and a buzhash chunker whose defaults are min 512 KiB, target 2 MiB, max 8 MiB, window 4095 ([borg internals](https://borgbackup.readthedocs.io/en/stable/internals/data-structures.html)). It also offers a fixed-size chunker and a files cache keyed on inode/size/mtime to skip unchanged files ([borg internals](https://borgbackup.readthedocs.io/en/stable/internals/data-structures.html)).

Why they are the wrong primary tool for pluto:

1. **Chunk granularity**: ~1–2 MiB average blobs versus casync/desync's 64 KiB. Coarser chunks mean fewer matches for small changes and worse cross-branch sharing in a ~20 GB image.
2. **Object identity namespaces**: borg chunk IDs are keyed MACs derived from repo keys; restic IDs are hashes inside one encrypted repo. Dedup is per-repository. That is fine for one bucket, but these tools are not designed for "any host materializes a known disk image from a shared store with local seeding."
3. **Restore model**: backup tools reconstruct from a catalog; desync/casync reproduce a specific artifact from an index and will clone from any local seed (old image, reflink-friendly filesystem) and fetch only the missing chunks. `mount-index` even lets the image be read while it is still downloading ([desync README](https://github.com/folbricht/desync)).
4. **Bootstrapping cost**: a cold restore through restic/borg installs/decrypts and walks the repo; a desync extract is a sequence of chunk placements.

When borg/restic *are* right: backing up the host itself (home dir, configs, the bucket's metadata) with retention and encryption, and file-level restore. pluto's box disks want artifact materialization; use backup tools for the machines, not for box disk state.

---

## 3. Incremental and continuous replication while running

### 3.1 QEMU dirty bitmaps and incremental backup

A block-dirty-bitmap is a bit vector with configurable granularity; a set bit means that range *may* have changed. The default granularity matches the drive's cluster size, clamped to 4–64 KiB, currently 64 KiB for qcow2, and bitmap size is `ceil(ceil(image/granularity)/8)` bytes ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). Bitmaps are created with `block-dirty-bitmap-add`, and only **persistent** bitmaps (qcow2 only) survive a clean QEMU exit ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). Offline, `qemu-img bitmap` can add (`-g` granularity), remove, clear, enable, disable, and merge bitmaps, and `qemu-img info` reports them ([`qemu-img(1)`](https://www.qemu.org/docs/master/tools/qemu-img.html)).

`blockdev-backup device=... bitmap=... sync=incremental target=...` writes the ranges marked dirty into a target image. The backup's point in time is when the job *starts*; writes during the job are tracked in a temporary bitmap and merged back into the source bitmap at the end, so they land in the next backup (bitmaps doc, "First Incremental Backup"). If a job fails or is cancelled, the bitmap is not cleared, and the same command can simply be retried ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). Multi-disk consistency is possible in one `transaction`, with `completion-mode=grouped` to make all drives fail or succeed together ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)).

**The failure mode that matters**: if QEMU does not get a clean close, a persistent bitmap loads as `+inconsistent` and the only allowed operation is removal; no further incremental backups can use that chain and a new full backup is required ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). `virtnbdbackup` documents the same operational reality: inconsistencies between libvirt checkpoints and qcow2 bitmaps, forcefully killed QEMUs leaving unsynced bitmaps, and a fallback to full backup ([virtnbdbackup README](https://github.com/abbbi/virtnbdbackup)). Treat bitmap loss as expected on host crashes, not exceptional.

### 3.2 Snapshotting a running VM

Live block operations (QEMU 2.9-era API, unchanged in outline) give the toolbox ([live block ops](https://www.qemu.org/docs/master/interop/live-block-operations.html)):

- `blockdev-snapshot-sync` inserts a new overlay above the active layer; all subsequent writes go to the overlay, so the previous layer is a stable point in time.
- `blockdev-backup sync=incremental` pushes changed blocks to a separate target; `sync=full` copies the whole chain; `sync=top` copies only the active layer.
- `block-commit` merges overlays down (active commit is two-phase and needs `block-job-complete`); `block-stream` copies backing data up into overlays; `blockdev-mirror` synchronizes to another image.
- `drive-backup`/`drive-mirror` are deprecated in favour of the `blockdev-*` forms ([live block ops](https://www.qemu.org/docs/master/interop/live-block-operations.html)).

`virtnbdbackup` is the reference user-space implementation of the libvirt flow: `virDomainBackupBegin` starts a checkpoint and an NBD server over the disk at a point in time; the tool freezes guest filesystems through the guest agent for "application-consistent" backups, otherwise the stream is crash-consistent; raw disks without bitmap support can only be copied fully; qcow images must be version 3 (compat 1.1) for persistent bitmaps; scratch files (fleecing) absorb writes during the job ([virtnbdbackup README](https://github.com/abbbi/virtnbdbackup)).

### 3.3 Crash consistency and quiescing

libvirt's framing is the clearest: disk-only capture without guest cooperation is "comparable to rebooting a machine that had power cut before I/O could be flushed"; guests need proper journaling to recover, and cooperating with the guest to quiesce gives a fully consistent state "rather than merely crash consistent". Freezing is only possible with a trusted guest running a guest agent, and guests can legitimately panic if I/O stays frozen too long ([libvirt domain state capture](https://libvirt.org/kbase/domainstatecapture.html)). The QEMU guest agent is what exposes filesystem freeze/thaw ([qemu-ga(8)](https://www.qemu.org/docs/master/interop/qemu-ga.html)); while filesystems are frozen, only a safe subset of agent RPCs is allowed.

Practical pause flow for pluto:

1. If the guest agent is present: `guest-sync` then `guest-fsfreeze-freeze`.
2. QMP `stop` (vCPUs frozen; no more guest writes).
3. QMP `quit` — QEMU flushes caches on a clean exit ([live block ops example](https://www.qemu.org/docs/master/interop/live-block-operations.html)).
4. Host-side `fsync` the image before declaring the pause durable, then chunk.
5. On resume, the guest boots; its journal replays anything that was in flight. Without an agent step, step 1 is skipped and the snapshot is crash-consistent. Disk-only pause means nothing else about the running system survives anyway.

Filesystem-level snapshots (btrfs/zfs) of a live image fall under the same rule: atomic at the block-file level, crash-consistent to the guest.

### 3.4 Realistic RPO

| Capture mode | Data-loss window (RPO) | Notes |
| --- | --- | --- |
| Pause-only sync (v1) | Time since last pause; unbounded between pauses | What pluto's "pause" means today: durable at pause, fresh boot at resume. Simple, no dirty tracking. |
| Periodic incremental backup (v2) | ≈ backup interval | Point in time is job start; writes during the job go into the next round ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)). Interval bounded by staging space and scratch I/O. |
| Continuous-ish (still v2) | Interval, e.g. 30 s–5 min | Same mechanism, shorter interval; upload can lag the job. Host crash loses the bitmap → next capture must be full, so the *effective* window extends until the next full capture completes. |
| Anything with memory | — | Out of scope by decision; process state is never restored. |

Dirty rate is workload-dependent. For pluto's boxes the dominant writer is the project build (package installs, compilers, test output), so the changed fraction is bursty: near zero while idle, potentially many GB after a cold build. Deduplication across branches and the local chunk cache do more for cost than a shorter interval does. A reasonable v2 default is **every 15 minutes while running, plus a final incremental and full checkpoint at pause**.

---

## 4. Object-store layout

### 4.1 Self-hosted S3

- **Garage** fits the first host: a single static binary, `garage server --single-node --default-bucket` gets a working S3 endpoint with local credentials (Quick start), and the S3 API surface includes `PutObject`, `GetObject`, `HeadObject`, `CopyObject`, `ListObjects`/`V2`, presigned URLs, and every multipart endpoint. It does not implement versioning, object lock, ACLs/policies (it has its own key-bucket permission model), or server-side encryption; lifecycle is limited to expiration and aborting incomplete multipart uploads ([Garage S3 compatibility](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/)). None of the missing features are needed for an immutable content-addressed store with no trust model.
- **MinIO CE** is not a safe foundation for a new project: the upstream repository is archived on GitHub with its last push in April 2026 ([GitHub API](https://api.github.com/repos/minio/minio)); the community edition had its admin console removed in 2025 and is reported in maintenance mode ([secondary](https://www.blocksandfiles.com/ai-ml/2025/06/19/minio-users-complain-after-admin-ui-removed-from-community-edition/1610856)). A community fork exists ([secondary](https://blog.vonng.com/en/db/minio-resurrect)); not a dependency to bet on.
- SeaweedFS and Ceph RGW exist as alternatives; not evaluated in this pass. Ceph is disproportionate for one host.

The S3 features that matter: unbounded object PUT/GET (**chunks are immutable** — content addressing means a key is never rewritten), `ListObjects` for garbage collection, ranged GET for partial objects (index/manifest reads, and any future protocol work), and multipart only for large artifacts (indexes are MB-scale; chunks are ≤256 KiB). Conditional `If-None-Match: *` PUTs exist in the S3 API to prevent overwrites ([AWS docs](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)); Garage support for them was not verified, and with no trust model they are a hygiene feature, not a requirement.

### 4.2 Layout

Use one Garage bucket with three prefixes. Keep chunk-store internals owned by desync rather than inventing a parallel format — pluto controls both ends, and desync's S3 store is the read/write path:

```
<store>/
  chunks/…                      # desync/casync chunk store (zstd .cacnk objects, desync-managed names)
  indexes/<box>/<generation>.caibx
  manifests/<box>/<generation>.json
  manifests/<box>/current.json  # mutable alias, written last
```

A generation manifest is the durable statement of "box X had this disk at this point":

```json
{
  "schema": "pluto.disk/v1",
  "box": "pluto/research/disk-state",
  "generation": 42,
  "parent": 41,
  "reason": "pause",                  // pause | periodic | final
  "created_at": "2026-10-04T12:00:00Z",
  "host": "sid-desktop",
  "disk": { "kind": "raw", "size": 34359738368, "device": "vda" },
  "base": null,                        // content ID of the base image, when overlays are used
  "checkpoint": {
    "index": "indexes/pluto-research-disk-state/42.caibx",
    "digest": "sha512-256:…",
    "chunk_store": "chunks/",
    "chunks": 321457,
    "bytes_compressed": 1234567890
  },
  "layers": [                          // v2 only: ordered backup deltas after this checkpoint
    { "kind": "block-delta", "object": "deltas/pluto-research-disk-state/43-<ts>.qcow2.zst",
      "parent": 42, "bitmap": "pluto", "created_at": "…" }
  ],
  "tool": { "desync": "1.1.4", "qemu": "…" }
}
```

Commit order is **chunks → index → manifest → current.json** (or simply chunks → index → manifest, with `current.json` last). Every step is idempotent: chunks and indexes are immutable, a retried pause re-uploads nothing that exists and rewrites only the manifest. An interrupted pause leaves unreferenced garbage, never a manifest pointing at missing data. This mirrors restic's ordering invariant (data before indexes before snapshots, [restic references](https://restic.readthedocs.io/en/stable/100_references.html)) for a different format.

Prefix fanout inside `chunks/` is desync's business. If pluto ever implements its own store, use `chunks/<hex[0:2]>/<hex[2:4]>/<hex>` to avoid hot prefixes in S3-style backends; a 20 GiB raw image at 64 KiB average is roughly 300k chunks, which is fine.

### 4.3 Resume fetch

Same host: the previously materialized image is the seed. `desync extract -s <store> -c /var/cache/pluto/chunks --seed <old>.caibx <new>.caibx <disk>` reuses matching chunks (reflinking on btrfs/XFS, cloning repeated sections from the output itself, and synthesizing zero ranges sparsely) and fetches only what is missing ([README](https://github.com/folbricht/desync), [concepts](https://github.com/folbricht/desync/blob/master/docs/concepts.md)). Local chunk cache retains chunks even after old images are deleted, so pause→resume cycles on the same host are near-instant.

Cold host: fetch the caibx, then extract with only the local cache as seeds. desync adapts concurrency to the store (up to 128 in flight) and verifies chunks as they arrive; the README's cold-client download for a 3.2 GB Debian image is 292 MB of compressed chunks ([README](https://github.com/folbricht/desync)). A 20 GiB box image will be larger, but only the non-zero, non-reused data is fetched. Faster cold starts are possible with `desync mount-index`, which FUSE-mounts the index and fetches on read ([README](https://github.com/folbricht/desync)); for pluto, materialize fully and `fsync` before boot to keep the failure model trivial.

Garbage collection: `desync prune -s <store> <live indexes…>` deletes chunks not referenced by those indexes ([README](https://github.com/folbricht/desync)). The live set is every index reachable from any `manifests/<box>/current.json` (plus explicitly archived generations). Two cautions: (1) enumerate manifests first and never include an index that is still being uploaded — prune only indexes older than the newest committed manifest; (2) run prune when no pause is in flight on that box. For v1, the simplest policy is **no GC at all**: chunks are immutable and compressed, and deleting them can wait until retention policy is a real problem.

### 4.4 Compression

Compress **per chunk**, after chunking and hashing the plaintext. desync does this by default with zstd (zstd only), stores `.cacnk` objects, and can serve/read uncompressed stores for latency-sensitive caches ([stores doc](https://github.com/folbricht/desync/blob/master/docs/stores.md)). Hashing plaintext keeps dedup independent of compression: the same bytes are one object regardless of how they compressed. Never compress the whole image before chunking — a one-byte change then destroys downstream reuse (desync README's own caveat). Large zero ranges cost almost nothing: zstd compresses them to nothing at rest, and the null seed turns them into holes on extraction.

Do not encrypt. pluto has no trust model, and content-addressing plus encryption fight (desync's optional chunk encryption is designed for untrusted hosts, which is explicitly out of scope for now; [README](https://github.com/folbricht/desync)).

---

## 5. Recommended pipelines

### v1 — pause-time, full-image chunking (simplest that works)

1. **Disk**: one raw sparse file per box, e.g. `/var/lib/pluto/boxes/<box>/vda.raw`, provisioned from the project's base image when the box is created (base distribution is ticket #7's problem, not this pipeline's).
2. **Pause**: optional agent `fsfreeze` → QMP `stop` → QMP `quit` → `fsync` the image → `desync make -s s3+http://garage:3900/pluto/chunks <gen>.caibx vda.raw`. `make` reads the whole image but PUTs only unseen chunks, and zstd-compresses each new chunk.
3. **Commit**: PUT `<gen>.caibx`, PUT `<gen>.json`, then PUT `current.json`.
4. **Resume**: read `current.json`, `desync extract -s s3+http://garage:3900/pluto/chunks -c /var/cache/pluto/chunks --seed <last>.caibx <gen>.caibx vda.raw`, `fsync`, boot. If no local seed exists, extract cold from the bucket.
5. **GC**: none. Keep the last N generations per box; the bucket is the retention policy.

Costs: one full local read + chunk/hash pass per pause (CPU-bound; parallelizable on 12 cores), upload proportional to changed bytes, storage proportional to unique chunks. Benefits: no qcow2, no backing chains, no dirty bitmaps, no VMM coupling, and the chunk store, index format, and manifest model are already v2's.

*Variant for a QEMU-only fleet with a ready base-image pipeline*: make the box disk a qcow2 overlay whose backing file is the shared base image, and in v1 just upload the overlay object (content-addressed, zstd) per pause. Uploads are smaller by construction, but the disk format, base coupling, and metadata churn trade away the VMM independence. Defer until the VMM decision (ticket #2) lands.

### v2 — chunked, incremental, continuous

Prerequisites: the working disk becomes qcow2 (`compat=1.1`) so persistent bitmaps exist; QEMU (or libvirt/virtnbdbackup-equivalent orchestration) manages backup jobs. The chunk store, indexes, and manifests are unchanged.

1. **Working disk**: qcow2 file in the box's directory, created once via `qemu-img convert -f raw -O qcow2 -o compat=1.1 vda.raw vda.qcow2`, with a persistent bitmap `pluto` added (`block-dirty-bitmap-add … persistent=true`, granularity 64 KiB).
2. **Periodic while running** (e.g. every 15 min): `blockdev-backup device=vda bitmap=pluto sync=incremental target=<delta>` to a local staging qcow2. The delta contains only dirty clusters; zstd-compress and PUT it as an opaque object; append a `block-delta` layer to the box's manifest. Deltas are computed from the *last successful backup*, so retries reuse the same bitmap ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html)).
3. **Pause**: final incremental delta; then materialize the canonical raw image from the qcow2 file (`qemu-img convert -O raw`), run the full `desync make` checkpoint as in v1, and commit a new manifest. Optionally reset the bitmap chain (new full capture = new anchor).
4. **Resume**: extract the latest full checkpoint with desync, apply any later deltas in order by writing their allocated extents into the raw image (`qemu-img map --output=json` on each delta gives `start`/`length`/`offset` extents; [qemu-img doc](https://www.qemu.org/docs/master/tools/qemu-img.html)), `fsync`, boot. If no deltas exist, boot straight from the checkpoint. If the bitmap went inconsistent (unclean host death), ignore deltas, start from the last full checkpoint, and run a fresh full backup on next opportunity ([bitmaps doc](https://www.qemu.org/docs/master/interop/bitmaps.html), [virtnbdbackup README](https://github.com/abbbi/virtnbdbackup)).
5. **RPO**: the periodic interval. Crash consistency is the default; guest-agent freeze before a backup narrows the application-level window ([libvirt domain state capture](https://libvirt.org/kbase/domainstatecapture.html)).
6. **GC**: as v1, plus deltas pruned once a newer full checkpoint exists and a grace period has passed.

Costs: qcow2 working format, QEMU-only, delta-apply logic on resume, bitmap-loss fallbacks. Benefit: continuous-ish capture with bounded RPO, without giving up the canonical full checkpoints.

### Migration path v1 → v2

1. Keep v1 running unchanged: raw disk, pause-time desync checkpoints, manifests.
2. When continuous capture is wanted (and the VMM supports qcow2/bitmaps), pick a pause: convert the raw image to qcow2, keep the same disk bytes, add the persistent bitmap, and commit a full checkpoint (same chunk store — most chunks are reused; the qcow2 representation introduces a re-sync of the changed layer).
3. Turn on periodic deltas. Manifests from v1 remain valid; v2 manifests add a `layers` array. A v2 pause is a strict v1 pause plus deltas.
4. If the VMM decision rules out qcow2 (e.g. Firecracker), stay on v1; the only loss is between-pause RPO, which the pause-centric model already tolerates.

---

## 6. Uncertainties and open questions

- **desync store internals**: the on-disk/S3 object naming inside the desync store is not documented in the sources read; the layout above deliberately treats it as opaque. If pluto ever needs a store it manages itself (e.g. no desync on some host), the chunk naming has to be reverse-engineered or replaced.
- **Compression performance on this host**: the README's numbers are from the maintainer's machines; a 12-core/16 GB box should parallelize chunking well, but pause-time chunking duration for a 20 GiB image is an estimate, not a measurement. Measure with `desync make` on a real box before committing to a pause budget.
- **Firecracker format support** is asserted from a secondary source; no official Firecracker document found stating "raw only". Verify before depending on it.
- **Garage conditional writes / range GET behaviour** for the exact requests desync makes was not verified. Test `desync` against the chosen Garage version as the first integration test.
- **MinIO fork viability** ([Silo](https://blog.vonng.com/en/db/minio-resurrect), secondary) is moving; not a recommendation, just noted as a migration path if an existing deployment must stay on the MinIO format.
- **qcow2 default cluster size** is inferred from the bitmaps documentation (default granularity "matches the cluster size … currently 64 KiB"), not from the format spec.
- **btrfs/zfs snapshot consistency** for a live VM image is reasoned from filesystem semantics plus the guest-journal argument, not from a VM-specific primary source. Fine as a local accelerator; do not make it the durable path.
- **Deltas as opaque qcow2 objects** (v2) need a small apply step on resume; this is pluto-owned code. If that logic is unwanted, the alternative is to materialize and re-chunk a full checkpoint at every interval (CPU-heavy) or to keep local qcow2 chains with periodic commit (host-coupled).
- **Project cache disks**: if pluto later gives boxes a second, project-shared cache disk, it needs its own content ID and its own bitmaps; the manifest schema above anticipates multi-disk but does not design it.
- **Forking** a box (new branch from an existing box's state) is a seed problem, not a storage-format problem: extract the parent's checkpoint as the child's seed before the first capture. Worth a dedicated prototype when fork becomes a real verb.

## References

Primary sources are linked inline throughout. The load-bearing ones: [QEMU qcow2 spec](https://www.qemu.org/docs/master/interop/qcow2.html), [QEMU live block ops](https://www.qemu.org/docs/master/interop/live-block-operations.html), [QEMU dirty bitmaps](https://www.qemu.org/docs/master/interop/bitmaps.html), [libvirt domain state capture](https://libvirt.org/kbase/domainstatecapture.html) and [incremental backup internals](https://libvirt.org/kbase/internals/incremental-backup.html), [casync README](https://github.com/systemd/casync) and [`caformat.h`](https://github.com/systemd/casync/blob/main/src/caformat.h), [desync README](https://github.com/folbricht/desync) and docs, [restic references](https://restic.readthedocs.io/en/stable/100_references.html), [borg internals](https://borgbackup.readthedocs.io/en/stable/internals/data-structures.html), [Garage S3 compatibility](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/).
