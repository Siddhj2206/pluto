# Runs share a box by lock policy

A workspace has one durable box, and a task declares how its runs share it. `service` tasks stay running for the box's life; `shared` tasks run concurrently with each other and with attach sessions; `exclusive` tasks run alone and wait for shared work to drain before starting. This replaces the earlier "all runs on a durable box serialize" rule so that a long batch run does not block interactive development, while a step that needs the whole box still gets it exclusively.

## Consequences

- The lock policy is a first-class field in the box contract, not a scheduler heuristic.
- Two shared tasks can race on the same files; that is the developer's choice, not the engine's error.
