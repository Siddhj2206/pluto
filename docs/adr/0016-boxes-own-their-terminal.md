# Boxes own their terminal; the daemon multiplexes it

**Status:** Accepted. Supersedes [ADR 0010](0010-sessions-are-tmux.md).

A box's interactive terminal is a PTY whose master the host daemon owns — not tmux, not SSH. The daemon hands the slave into the container as a task's stdio, keeps a bounded scrollback ring, and fans output out to any number of subscribers; `watch` is read-only and `attach` may type. Several clients may attach at once. With a shared kernel this needs no guest transport, which is exactly why it is cheap here and was not in the microVM. This reverses ADR 0010's deliberate choice of tmux as the boring owner: multiplayer attach and one multiplexer that also serves live log streaming are worth the new subsystem.

## Considered options

- **tmux in the box.** Boring and durable, but a detaching/re-attaching model, not a pub/sub stream, and it cannot serve multiple read-only watchers or feed the GitHub runner surface.
- **One SSH connection per client.** No shared scrollback, no fan-out, and access becomes the network's problem again.
