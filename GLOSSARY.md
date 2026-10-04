# pluto

pluto is a personal, self-hosted fleet of durable machines: microVM boxes keyed by git worktree, paused disk-only, with state in a bucket the owner controls.

## Language

**box**:
A durable machine owned by one worktree. One box per worktree; it is the unit that is built, leased, paused, resumed, and forked.
_Avoid_: sandbox, devbox, VM, environment

**worktree**:
The (project, branch) pair a box is keyed to.
_Avoid_: workspace, checkout

**project**:
A git repository whose branches each have boxes.
_Avoid_: repo, repository

**runner**:
pluto's component that boots and manages a box on a host by driving a VMM: it prepares the disk, launches the process, wires vsock and networking, and performs lifecycle operations.
_Avoid_: orchestrator, manager, runtime

**VMM**:
The external hypervisor process that runs a box, such as Firecracker or Cloud Hypervisor. It sits behind the runner's interface and is not part of pluto.
_Avoid_: hypervisor, emulator
