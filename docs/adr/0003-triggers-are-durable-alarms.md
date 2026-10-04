# Triggers are durable alarms on the box record

A trigger is a durable activation request attached to the box record: *ensure running → optionally run a command → record the outcome*. The record is the source of truth — it survives host restarts, travels with a hibernated box, and lets any daemon claim a due firing through the same CAS lease that arbitrates every activation. The daemon materializes due times into timers it can fire; missed firings coalesce to one late run rather than replaying a backlog.

## Considered options

- **Systemd timers as the source of truth**: free catch-up via `Persistent=`, but binds schedule state to one host's systemd, which breaks hibernation and fleet portability.
- **Daemon-memory timers**: lose schedules on restart, and a hibernated box has no record to wake from.

## Consequences

- Delivery is at-least-once: a crash window can duplicate a run — the right bias for build and agent work.
- A paused box's schedule fires only from its pinned host; a hibernated box's can be claimed by any daemon.
- "Trigger" is the umbrella term for manual runs, schedules, connection wakes, and the planned git events.
