# GitHub event webhooks

Pluto accepts authenticated GitHub `push`, `pull_request`, and `issues`
deliveries on a dedicated HTTP listener. Run `pluto setup github` in the
repository to choose supported events and map each event to a declared job.
The listener is intended for a user-managed HTTPS reverse proxy; GitHub must
be able to reach that proxy. Pluto does not terminate TLS or host a relay.

Configure one registered remote box when starting the daemon:

```sh
export PLUTO_GITHUB_WEBHOOK_SECRET='a-long-random-secret'
pluto daemon --webhook-listen 127.0.0.1:8787 \
  --github-push-source production --github-push-box BOX_ID
```

The secret is read from the named daemon environment variable, used only to
verify `X-Hub-Signature-256`, and never passed into the box or job environment.
Keep it in the service manager's protected environment configuration. In the
GitHub repository settings, create a webhook pointing through the TLS proxy to
`/github/production`, choose JSON, subscribe to **Pushes**, **Pull requests**,
and **Issues** as configured, and use the same secret. Event mappings must be
on the trusted default branch and each selected job must be declared under
`[jobs.<name>]`; `pluto setup github` writes those mappings to `.pluto.toml`.

Pluto accepts pushes only for the registered box branch; deleted refs and
other branches are ignored. A repository branch has one durable `github`
task; each unique delivery appends a serialized run. A pull request or issue
likewise has one task for its repository number. Delivery retries return the
same run, and task/run records preserve source, job, event context, and the
trusted contract revision. Pluto requires approval of the exact current
trusted default-branch contract revision at admission and checks it again
before execution. Unlabeled pull requests remain untrusted: they receive no
host-held credentials, and the existing trust-class restrictions continue to
apply. A 2xx response means queue acceptance is durable; overload is reported
with a non-2xx response and a visible rejected run.

The daemon's existing Unix socket remains its private control surface. The
dedicated listener exposes only signed webhook intake, not task or control
routes. Use a distinct `--github-push-source` and secret for each configured
webhook source.

## Optional local post-commit hook

Run `pluto init` in a repo to create a starter `.pluto.toml` if one is absent.
This does not create or register a box. Add `--with-hooks` to opt into a local
post-commit hook, or `--remove-hooks` to remove Pluto's hook later. The installer
keeps an existing `core.hooksPath` and chains to the hook already there; an
existing contract is left untouched. If a user changes Pluto's managed hook,
removal stops and leaves its active hook path in place for manual inspection.

The hook best-effort submits the worktree, current commit, and branch to the
daemon's Unix socket. The daemon accepts it only when that worktree already
has a box, the trusted default-branch contract declares `[events.push] job =
"name"`, and that exact contract revision is approved. It records one durable
post-commit task per repository branch and appends one run per unique commit.
It never creates or starts a box. A stopped or unreachable daemon does not
affect the commit. Local hooks do not work on a different host from the daemon.
