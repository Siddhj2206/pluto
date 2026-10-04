# State is local to the host in v1; the bucket is deferred portability

The daemon keeps all state in a versioned local directory (box records, jobs, schedules) guarded by a lock, one host per pluto installation. There is no bucket, no lease, no epoch, and no content-addressed chunking in v1: those exist to let a box move between hosts, and there is one host. `export`/`import` — a copy of a box's disk and record — is the first portability step, and the celld-style probed-S3 design from research is the target only when a second host is actually in use (revive triggers in `docs/DEFERRED.md`).

## Considered options

- **Running SeaweedFS locally from the first box** (the original plan): exercises the fleet code path, but adds a store to install, operate, and debug before there is anything to sync.
- **Local backend with a pre-built bucket schema**: keeps the same complexity and adds an untested translation layer.
- **A database for local state**: versioned files with atomic replace are enough at this scale; a database can come when queries demand it.

## Consequences

- No cross-host handoff, no GC problem, no store probe in v1.
- State records stay versioned so export/import and later leases can be added without a rewrite.
- Backups are the user's responsibility (git plus, later, export).
