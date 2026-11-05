# Pluto is one Go binary, and the CLI only talks to the host's API

Pluto is written in Go and ships as one binary with no cgo, acting as both client and host. Go cross-compiles to every planned platform, and the libraries this design leans on are Go: Tailcat for reaching hosts, and MyGo if the GUI uses it. The CLI talks to the host only through a versioned HTTP API over a Unix socket, checking capabilities before relying on a feature, and never touches its files. The exceptions are local by nature, for now: `pluto doctor` checks this machine's engine and disk, and `pluto enter` runs the engine command the host gives it. That means a remote route in M4 needs no new protocol. The host keeps state in SQLite and drives its container engine through the engine's CLI, in one reconcile loop, so a host restart finds containers that kept running.

## Considered options

- **Rust.** herdr shows it works well, but it gives up the Go libraries above and has slower iteration for one maintainer.
- **State in JSON files.** Simpler, but transactions and queries would have to be rebuilt by hand.
- **The engine's HTTP API.** Podman and Docker differ there, and the CLI is good enough for now.

## Consequences

- The pure-Go SQLite driver adds binary size, as Tailcat will.
- Engine CLI output is a dependency, so the engine stays behind one small interface.

**Revisit when:** a required library has no good Go path, binary size blocks a real install, or engine CLI differences cause repeated failures.
