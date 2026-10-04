# Box state lives in a probed S3 store; leases are celld-style

pluto's durable substrate is a self-hosted S3-compatible store certified by a probe, not a vendor list: the daemon proves conditional create/update, read-after-write, and list-after-write before trusting a store, refusing hibernate/claims/handoffs loudly and retryably if the probe fails while local verbs keep working. SeaweedFS is the default (≥ 4.46, single filer, auth on, bound to localhost or the tailnet); Ceph RGW is the already-Ceph alternative; Garage is excluded by design and MinIO is archived. Coordination is celld-style: two-level leases (a host-liveness lease with a 30 s TTL renewed every 10 s, plus box ownership) and a per-activation epoch that qualifies every mutable prefix, with the ack rule re-reading ownership before any acknowledgement so correctness never depends on clocks.

## Considered options

- **Multi-filer SeaweedFS replication for store HA**: eventually consistent, breaking the conditional-write assumptions — single filer instead, and backups as the mitigation.
- **A built-in filesystem or SQLite local backend** (celld-dev style): rejected in favour of running the real store locally, so M0 exercises the same code path as the fleet.
- **Vendor trust** (blessing a store by brand): rejected — the probe certifies, which is also what keeps young stores like RustFS on the watch list rather than in the default.

## Consequences

- The store is a deliberate single point of failure; backup and the probe are the mitigations, not clustering.
- No GC in v1: chunks and manifests accumulate; a future `pluto prune` is gated on list-after-write, and tombstones are forever.
- Commit ordering is chunks → index → manifest → pointer CAS → ack readback; interrupted commits leave only unreferenced objects.
