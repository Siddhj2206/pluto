# Restart from the product, not the code

Pluto is rewritten from the ground up around [docs/VISION.md](../VISION.md). Earlier versions were shaped by what was interesting to build (microVMs, then the moonshot) and deferred the hardest product question: how a second device or a second person gets in. New decisions start from a real user and a moment of need, and work ships in milestones that are each usable alone, tracked in [#175](https://github.com/Siddhj2206/pluto/issues/175). The first user is the maintainer, on a laptop. The repository starts blank. Earlier code, ADRs and research remain in history on `master` and `moonshot/core`, and may be used as evidence, but nothing carries over by default.

## Consequences

- Features without a real moment of need get cut, even if already designed.

**Revisit when:** M1 and M2 are in daily use and M5's outside user cannot get a green job, or conversations with people running self-hosted runners show that they don't want this.
