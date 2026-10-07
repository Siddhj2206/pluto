# Triggers are durable alarms on the box record

A trigger is a durable activation request attached to a box: *ensure running → optionally run a command → record the outcome*. Schedules are declared in the box's `.pluto.toml` (ADR 0007) and stored as box records with the arm time that made them current and the latest due occurrence materialized as a task/run, so they survive daemon restarts and host reboots. A due occurrence is durably accepted before the schedule clock advances. Its timestamp is the idempotency key, so retries of one occurrence return the same task/run while later occurrences each create a new task. The scheduler materializes missed occurrences one at a time after restart. The host queue orders explicit user work before repository events and scheduled work, preserves FIFO within a class, and promotes aged work. It enforces the host running-box limit and a box still runs at most one job at a time. Firing lands in M1; fleet claims are deferred until a second host exists (ADR 0004).

## Considered options

- **Systemd timers as the source of truth**: free catch-up via `Persistent=`, but binds schedule state to one host's systemd and cannot travel with a box when export/import lands.
- **Daemon-memory timers**: lose schedules on restart.
- **Skip due work when a box is busy**: simple, but drops requested work. M4 stores due work in the durable host queue instead.

## Consequences

- Queue execution is at-least-once: a crash window can repeat a run, while the occurrence task identity and delivery idempotency prevent duplicate accepted work.
- A busy box leaves each occurrence pending in the durable host queue; it does not start a concurrent job. Every missed firing is materialized as a separate task/run on restart, and failed runs are recorded like any job.
- Warm-ups use the same queue and host running-box limit and create a task/run with no job. Every occurrence has its own queue item; scheduled work uses the lowest default priority and receives aging promotions. Job outcomes continue to appear in per-box job history.
- A paused box's schedule fires from its host; no other host exists in v1.
- Wake-on-connection is deferred (`attach` implies `up`, ADR 0006).
- "Trigger" is the umbrella for manual runs, schedules, and connection wakes.
