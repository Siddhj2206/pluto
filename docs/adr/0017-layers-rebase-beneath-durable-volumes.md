# Layers rebase beneath durable volumes

A box's root is overlayfs over a base layer and a package layer. A workspace's durable state lives in volumes mounted outside the overlay at `/workspace` and `/home/pluto`, plus a per-box writable layer that is best-effort. On wake the daemon recomputes the two lower layers and swaps them in, touching neither the volumes nor a durable box's writable layer, so a provision or lockfile change reaches new state without destroying existing work. Ephemeral boxes get a throwaway writable layer and no durable volumes.

## Consequences

- Anything a developer writes outside the two volumes is best-effort: it survives idle and stop, but a rebase may reset it. The volumes are the durability contract; the writable layer is convenience.
- Layers are content-addressed and shared, so the package layer is built once and reused by many ephemeral boxes.
