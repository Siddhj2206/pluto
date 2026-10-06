# GitHub push webhooks

Pluto can accept authenticated GitHub `push` deliveries on a dedicated HTTP
listener. The listener is intended for a user managed HTTPS reverse proxy;
GitHub must be able to reach that proxy. Pluto does not terminate TLS or host a
relay.

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
`/github/production`, choose JSON, subscribe to **Pushes**, and use the same
secret. Set `[events.push] job = "name"` in the trusted default branch's
`.pluto.toml`; that job must be declared under `[jobs.name]`. Pluto accepts only
pushes to the registered box branch; deleted refs and other branches are
ignored. Delivery IDs are deduplicated durably. A 2xx response means queue
acceptance is durable; overload is reported with a non-2xx response and a
visible rejected queue record.

The daemon's existing Unix socket remains its local control surface. The
dedicated listener exposes only webhook intake. Use a distinct `--github-push-source`
and secret for each configured webhook source.

## Optional local post-commit hook

Run `pluto init` in a repo to create a starter `.pluto.toml` if one is absent.
This does not create or register a box. Add `--with-hooks` to opt into a local
post-commit hook, or `--remove-hooks` to remove Pluto's hook later. The installer
keeps an existing `core.hooksPath` and chains to the hook already there; an
existing contract is left untouched. If a user changes Pluto's managed hook,
removal stops and leaves its active hook path in place for manual inspection.

The hook best-effort submits the worktree, current commit, and branch to the
daemon's Unix socket. The daemon queues it only when that worktree already has
a box and its trusted default-branch contract declares `[events.push] job =
"name"`. It never creates or starts a box. A stopped or unreachable daemon
does not affect the commit. Local hooks do not work on a different host from
the daemon.
