# Pause stays local; hibernate is the durability boundary

A box has two dormant states with different jobs. **pause** stops the machine cleanly and leaves the raw disk on its host — cheap, offline-safe, host-pinned, lease held. **hibernate** seals the disk into the content-addressed bucket, commits the manifest under CAS, and releases the lease — the only transition that makes a box claimable from another host and the only durability boundary. Pausing is therefore cheap enough for auto-pause and offline laptops, celld's "ack only after the bucket proves it" discipline applies exactly where handoff happens, and git remains the floor for everything not hibernated.

## Considered options

- **Checkpoint at every pause** (the disk-state research's literal v1): every pause reads and hashes the whole image and needs the store reachable, so an offline host cannot pause cleanly and auto-pause becomes heavy.
- **Best-effort async checkpoints on pause**: two write paths and an "upload not landed" state everyone must reason about.

## Consequences

- Paused boxes are pinned to their host; host loss loses them — git and hibernation are the answers. Fleet takeover only ever applies to hibernated boxes.
- Auto-pause can be aggressive (default: one hour idle, overridable globally / per project / per box) because pausing costs no bucket I/O.
- Crash recovery marks boxes paused-unclean; resume replays the journal; nothing uploads automatically.
- Box identity is an immutable uuid per incarnation, so epochs never reset across destroy/recreate; forks mint new uuids, take the `#n` key suffix, and share chunks copy-on-write.
