# CI runs the git host's official runner in a fresh box

Pluto does not parse workflow files. For GitHub Actions, the git host keeps workflows, triggers, status and logs, and Pluto runs the official runner once in a fresh container for each job. `runs-on` is the only workflow edit, and no `pluto.yml` is needed. This avoids a permanent compatibility burden: keeping up with Actions features becomes the runner's problem and not ours. The default image starts slim because full 1:1 compatibility isn't a goal, so missing tools must fail with a clear fix. Fork PR jobs are refused by a pre-job runner hook, which reports as a failed check, and that behaviour must be tested before it's documented as a guarantee.

## Considered options

- **A workflow YAML front-end.** Works offline and across hosts, but it's expensive to maintain and can't reproduce GitHub-only features such as OIDC.
- **A large default image close to `ubuntu-latest`.** More workflows pass first time, but the download and disk cost conflict with the setup goal and with laptop hosts.

## Consequences

- Other git hosts each need their own runner adapter.
- Pluto can only offer what fits around the runner, such as caches, boxes, entry after failure and host management.
- Native macOS and Windows jobs stay on hosted runners.

**Revisit when:** a slim image makes most workflows from real users fail on first run, or a target git host has no usable runner to wrap.
