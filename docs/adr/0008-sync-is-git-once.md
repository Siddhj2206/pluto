# Sync is a git bundle, once at creation

A box receives its repository as a `git bundle` of every ref, streamed over
vsock on first boot and cloned into `/home/dev/work/<project>`. Only
committed state travels, so gitignored and untracked files (`node_modules`,
`dist`, build output) never cross the wire; provision installs what the box
needs. The box's copy is the live one afterwards — the host does not re-sync
on wake, so work done inside the box cannot be clobbered, and git (commit
and push from inside the box) is the floor for anything that must leave it.
After the bundle clone the box mirrors every remote in the host worktree:
each remote's name, fetch URL, any distinct push URL(s), and the default
fetch refspec (`+refs/heads/*:refs/remotes/<name>/*`). No remote-tracking
refs are pre-fetched; the box fetches on demand. The checked-out branch is
made to track `origin`, or the sole remote when there is no `origin`; when
several remotes exist and none is `origin`, the branch is left untracked and
pluto warns. `push.default` is set to `current`, so a bare `git push` works
with a single remote, for published and unpublished branches alike. A
worktree with no remotes yields a box with no remote: local-only is a
first-class outcome, not an error and not a synthetic remote.

Authentication is the user's — for now a token supplied through `[env]` and
wired to git by provision, kept out of any committed contract — and pluto
stores no secrets. An SSH (`ssh://` or `git@`) remote is mirrored and the
branch may track it, but pushing over SSH is out of scope until M3; the
sanctioned private path is an HTTPS remote with an `[env]` token. Remotes
are read on first boot only: later remote changes are `git remote` inside
the box, or a destroy and recreate.

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
- The box mirrors the host worktree's remotes, so `git pull` and `git push`
  work inside the box; syncing is still a git verb, not a pluto one.
- A branch tracks `origin`, or the sole remote when there is no `origin`;
  an ambiguous multi-remote worktree is left untracked and warned about. A
  remote is a git fact, not a contract one: no remote URL rides `.pluto.toml`.
- A worktree with no remotes produces a local-only box: `pluto status` says
  `none (local-only)` and `pluto up` warns once that pushing is unavailable.
- Outbound authentication is the user's concern, expressed in the contract;
  pluto never holds credentials. SSH remotes are mirrored but warned about
  until M3.
- Repository size bounds first-boot sync time; the sample repo's bundle is a
  few megabytes.
