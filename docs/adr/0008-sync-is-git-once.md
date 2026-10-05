# Sync is a git bundle, once at creation

A box receives its repository as a `git bundle` of every ref, streamed over
vsock on first boot and cloned into `/home/dev/work/<project>`. Only
committed state travels, so gitignored and untracked files (`node_modules`,
`dist`, build output) never cross the wire; provision installs what the box
needs. The box's copy is the live one afterwards — the host does not re-sync
on wake, so work done inside the box cannot be clobbered, and git (commit
and push from inside the box) is the floor for anything that must leave it.
After the bundle clone the box's `origin` is set to the host worktree's
remote, so a session can push a branch out; authentication is the user's —
for now a token supplied through `[env]` and wired to git by provision, kept
out of any committed contract — and pluto stores no secrets.

`.pluto.toml` is read by the daemon on the host, not in the box, so the
contract itself does not need to be committed. Commands that reference repo
files must point at committed files, because those are what the box gets.

## Considered options

- **Tar the working tree**: carries uncommitted edits and gitignored
  directories; a full `node_modules` over vsock is slow and fights the box's
  own installs.
- **Re-sync on every wake**: silently overwrites changes made inside the
  box; the wrong default for a machine an agent works in.
- **virtiofs or a shared mount**: Firecracker has no shared filesystem, and a
  network filesystem is a fleet-scale answer to a single-host problem.
- **desync-style incremental sync**: parked in `docs/DEFERRED.md`; it pays
  only once export/import and many boxes exist.

## Consequences

- A box is created from a commit, not a dirty worktree; uncommitted host
  edits are invisible to it until committed and pushed from somewhere.
- The box's `origin` is the host worktree's remote, so `git pull` and `git
  push` work inside the box; syncing is still a git verb, not a pluto one.
- Outbound authentication is the user's concern, expressed in the contract;
  pluto never holds credentials.
- Repository size bounds first-boot sync time; the sample repo's bundle is a
  few megabytes.
