# Boxes are fresh by default, and caches live outside them

**Replaced by ADR 0008.** Kept as a record of the earlier decision.

A task's box is fresh by default, and `box: keep` is an explicit opt-in for work with real state, such as a dev server. Warmth comes from caches kept outside the box, so a fresh box still starts fast. Pluto does not cache results: every run executes every step. This avoids one long-lived box per checkout, with lock policies between its tasks, which the previous design used. That forced tasks to share state they mostly didn't need and made results depend on earlier runs. A task is still defined once and keeps a run history.

## Considered options

- **One durable box per workspace.** Simple to explain, but it's less reproducible and needs locking.
- **Result caching (skip when inputs are unchanged).** Tools like Turbo already do this per project, and a wrong hit reports a pass for work that never ran.

## Consequences

- Caches can hold bad state, so clearing them must be easy and named in relevant errors.
- Sharing a kept box between tasks is still undecided.

**Revisit when:** M3 measurements show that outside caches don't speed up heavy builds such as bootc images, or real users repeatedly need several tasks sharing one box.
