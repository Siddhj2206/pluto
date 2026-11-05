# CI runs the git host's official runner in a fresh box

Pluto does not parse workflow files. For GitHub Actions, the git host keeps workflows, triggers, status and logs, and Pluto runs the official runner once in a fresh container for each job. `runs-on` is the only workflow edit, and no Pluto config is needed. This avoids a permanent compatibility burden: keeping up with Actions features becomes the runner's problem and not ours. The default image is GitHub's official runner image (1.57 GB, and enough for Quill's CI in testing), because full 1:1 compatibility isn't a goal. Missing tools must fail with a clear fix. A pre-job runner hook refuses fork PR jobs, and any event not on its allowlist, including `issue_comment` and `merge_group`, because those workflows can check out fork code that the event doesn't reveal. A refusal reports as a failed check. Testing confirmed that the hook stops a job before any step runs, but the check against a real fork PR is still untested and must pass before this is documented as a guarantee.

## Considered options

- **A workflow YAML front-end.** Works offline and across hosts, but it's expensive to maintain and can't reproduce GitHub-only features such as OIDC.
- **A large default image close to `ubuntu-latest`.** More workflows pass first time, but the download and disk cost conflict with the setup goal and with laptop hosts.

## Consequences

- Other git hosts each need their own runner adapter.
- Pluto can only offer what fits around the runner, such as caches, boxes, entry after failure and host management.
- Native macOS and Windows jobs stay on hosted runners.
- Workflows triggered by `issue_comment` or `merge_group` don't run on Pluto.
- This work is a base for tasks and schedules through its boxes, hosts, run records and caches. It doesn't provide a YAML engine, because GitHub reads the workflow file.

**Revisit when:** a slim image makes most workflows from real users fail on first run, or a target git host has no usable runner to wrap.
