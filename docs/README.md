# Pluto docs

## Start here

- [R3 plan](r3-plan.md) — the current working plan: observable automation,
  durable device onboarding, managed worktrees, and the GitHub issue-to-PR
  loop. This is the document to read for review 3.
- [Vision](VISION.md) — the claim and the non-claims.
- [Contract](contract.md) — every field of `.pluto.toml`.

## Product and operations

- [Remote access](remote-access.md) — SSH recipes and `pluto device`.
- [Windows client](windows-client.md) — the current Windows client reference.
- [Windows acceptance](windows-client-acceptance.md) — the manual acceptance
  procedure for #162. It remains unfilled until a real Windows 11 x64 run is
  recorded.
- [GitHub events](webhooks.md) — the signed webhook, trust/admission, and the
  issue-to-draft-PR and PR-test-comment loop.
- [Service connections](service-connections.md) — declared services and
  client access.
- [Task API](task-api.md) — durable tasks and runs over the daemon API.
- [Testing](testing.md) — unit, host-only, and Windows checks.
- [Releasing](releasing.md) — how a release is cut.
- [Dependency updates](dependency-updates.md) — self-hosted Renovate.
- [Deferred](DEFERRED.md) — what is deliberately parked.

## Decisions

- [ADRs](adr/) — the recorded architecture decisions.

## Research

- [Research notes](research/) — comparables, prior art, and measurements.
  Reference only; not a product promise.

## Archive

- [M5 UX brief](archive/m5-ux-overhaul.md) — historical.
- [M5 CLI brief](archive/m5-cli-overhaul.md) — historical.
- [Windows connection walkthrough](archive/windows-connect-to-linux.md) —
  superseded by the current Windows client reference and the R3 plan.

The R3 plan is the source of truth for behavior that has not landed yet.
Documents under Product and operations describe the current implementation
until the plan is implemented; where they disagree, the R3 plan wins.
