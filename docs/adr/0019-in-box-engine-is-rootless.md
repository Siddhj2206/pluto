# A box's in-box engine is rootless, and privilege is an opt-in escalation

When the contract sets `docker`, a box runs **its own** container engine so its build tooling — `docker build`, `buildah`, `bootc` — works. The engine is isolated from the host's: it keeps its store in the box's own volumes, it listens on its own socket inside the box, and pluto never bind-mounts the host's `/var/run/docker.sock` (or the podman socket) into any box, in any mode. Mounting the host's socket would hand the workload the host daemon's root-equivalent API, which is strictly worse than running a nested engine.

The default is **rootless**. The box's engine runs as the box's unprivileged user; the only flag the host adds at create time is `/dev/fuse`, the device a userspace overlay (`fuse-overlayfs`) needs where the kernel will not give an unprivileged user a native overlay. pluto never runs `--privileged` Docker-in-Docker as the default and never sets `seccomp=unconfined` or `--cap-add SYS_ADMIN` for a rootless box. Where a build genuinely cannot run rootless — a native overlay, a loopback filesystem, the finpilot/Bluefin bootc path — the project opts in explicitly with `docker: {privileged: true}`, which maps to `--privileged`.

The escalation is a documented, deliberate loosening, not a hidden one. `--privileged` lifts the seccomp profile, grants every capability, and exposes the host's devices; a workload inside such a box can reach any host-kernel surface reachable from a privileged container. It is off unless the contract asks for it, it applies per box, and it is the project's stated acceptance of the wider boundary. Normal boxes — and rootless engine boxes — keep the hardening baseline in [docs/research/container-and-microvm-hardening.md](../research/container-and-microvm-hardening.md) §6.1.

## Considered options

- **The host socket, bind-mounted.** Rejected outright: it gives the box the host daemon, so the box is the host. Hardening §2.8(b).
- **Privileged DinD by default.** Rejected: it removes the boundary for every box to serve the few projects that need one. Hardening §2.8(a).
- **No engine at all; rootless BuildKit or Kaniko only.** Rejected for now: the spec calls for `bootc` and `buildah`, which want a real engine, and the rootless engine is adequate for the trusted-offload scope.

## Consequences

- `docker` is a box-level capability with three states: unset (no engine), `true` (rootless, isolated), `{privileged: true}` (the escalation). With `docker` unset the box has no engine.
- The rootless engine's images, containers, and socket live in the box and die with it; a box cannot see the host's, and the host cannot see the box's.
- The escalation's risk is the project's to accept. pluto records the choice in the compiled contract and never enables it implicitly.
