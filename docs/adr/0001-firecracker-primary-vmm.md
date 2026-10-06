# Firecracker is the primary VMM, with Cloud Hypervisor as a triggered fallback

pluto's runner drives a hypervisor rather than owning one. Firecracker was chosen as the primary VMM because it runs rootless on the first host (`unshare -Urn` + TAP, verified), boots a stock guest in ~430 ms, has the smallest device model, and provides vsock without privileges. Cloud Hypervisor is not co-primary: it is adopted only when a concrete capability requires it (virtiofs, UEFI/UKI boot, migration, Windows guests). QEMU microvm is a debugging fixture. The runner exposes a thin VMM interface from day one, so the choice is a backend, not an architecture; the artifact uses the Firecracker CI kernel (6.18.51, up from M0's 6.1.186) while FSDK/BuildStream kernels are deferred (`docs/DEFERRED.md`). Two settings are part of the decision: block devices use `cache_type=Writeback` (Firecracker's default silently ignores guest flushes), and drives stay raw — no qcow2 — which rules out QEMU-bitmap continuous replication for good. The jailer is deliberately skipped until a hardening need exists, since there is no trust model.

## Addendum: operation posture after the Firecracker research (2026-10-06, #75)

The Firecracker operation research (`docs/research/firecracker-operation.md` §4) adopted four refinements and rejected four postures. This records the result so it is not relitigated.

Adopted:

- **Boot args (A1).** The runner emits Firecracker's default hardening/boot flags (`nomodule i8042.noaux i8042.nomux i8042.dumbkbd swiotlb=noforce`) alongside pluto's own `console=ttyS0 root=/dev/vda rw reboot=k panic=1`. `8250.nr_uarts=0` is deliberately *not* adopted: it would disable the serial console pluto's boot log depends on.
- **Metrics (A2).** Firecracker flushes JSON metrics to a per-box `metrics.json`; the daemon surfaces the latest snapshot at `GET /v1/boxes/{id}/metrics`, passing it through as Firecracker emitted it.
- **Bounded logs (A3).** `serial.log` and `fc.log` are capped by rotation in the runner. Because Firecracker opens `log_path` without `O_APPEND` and writes from offset zero, its log goes through a named pipe the runner drains into a bounded file rather than being written to the file directly.
- **Seccomp (A4) and device model (A5): no change.** Firecracker applies its most restrictive seccomp filters per-thread by default; pluto passes neither `--no-seccomp` nor a custom filter. The five-device model is fixed and pluto uses exactly it. The VMM seam stays the static pre-boot config file; no interface is extracted, because the research justifies no behavior change there.

Rejected, each with its revive trigger:

- **Jailer (R1).** Skipped while pluto is single-user and rootless; adopting it would break rootlessness. *Revive trigger: a real trust boundary appears* — pluto hosts boxes for users other than the host owner, or boxes run code from untrusted external sources.
- **Rootless cgroup enforcement (R2).** Committed by the M3 spec and implemented in #73, not here. *Fallback trigger:* if rootless cgroup v2 delegation proves impossible on a target host, use a privileged helper or a documented host-setup step; the commitment to enforce limits does not change.
- **Snapshots (R3).** Out of scope for M3; pause/resume stays a clean shutdown followed by a cold boot. *Revive trigger: box resume latency becomes a measured problem* (users wait too long for `up` after `pause`, or sub-second resume is required). Reference implementation: E2B's pre-booted templates restored via `userfaultfd`.
- **`--enable-pci` (R4).** MMIO is Firecracker's default and carries pluto's SSH/agent/build traffic. *Revive trigger: virtio device throughput becomes a measured bottleneck* in box workloads; then evaluate `--enable-pci` with a PCI-capable kernel.
