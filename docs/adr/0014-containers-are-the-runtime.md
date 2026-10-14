# Containers are the runtime; a box is a container

**Status:** Accepted. Supersedes [ADR 0001](0001-firecracker-primary-vmm.md), [ADR 0002](0002-box-lifecycle.md), [ADR 0006](0006-access-is-ssh.md), and [ADR 0007](0007-box-contract.md).

pluto's execution substrate is OCI containers, not microVMs. The daemon drives one container backend through the runner seam: a box is a container whose root is an overlay built from layers, with durable volumes outside the overlay. Containers win because the warm-layer CI story depends on reusing a package layer across many ephemeral boxes cheaply, because nested build tooling (bootc, buildah) needs a real engine inside the box, and because a shared kernel makes host-owned PTYs and stream-based exec trivial where the microVM needed vsock. The microVM path — Firecracker, the vsock agent transport, the bootable image builder — is removed, not kept as an alternate backend. The runner seam survives, so a second substrate could return if measurements demand it. Access is the daemon-owned terminal, not SSH into a machine.

## Considered options

- **MicroVM (Firecracker) primary.** The prior direction: a real kernel boundary and vsock. Removed because layer sharing, nested builds, and daemon-owned PTYs are all cheaper to build on a shared kernel, and the moonshot's CI economics need warm layers more than a hardware boundary.
- **Both substrates behind the seam.** Rejected for now: two backends doubles the lifecycle surface to spec before the container path has proven itself. The seam keeps the door open.
