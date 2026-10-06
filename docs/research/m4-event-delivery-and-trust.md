# M4 Research: Webhook Delivery & Event Credentials/Trust Boundaries

**Issues:** #84 (webhook delivery), #85 (credentials & trust boundaries)
**Parent spec:** #83 (M4: event-driven and on-demand work orchestration)
**Date:** 2026-10-06
**Researcher:** Agent (for Siddhj2206)

---

## Part 1 — Webhook Delivery (Issue #84)

### 1.1 GitHub webhook intake

**Signature verification.** GitHub signs each delivery with `X-Hub-Signature-256`, an HMAC-SHA256 hex digest of the raw request body keyed by the webhook's secret. The legacy `X-Hub-Signature` (SHA-1) is still sent for compatibility but GitHub recommends the SHA-256 header. Verification must use a constant-time comparison (e.g., `crypto.timingSafeEqual` in Go) to avoid timing attacks. If no secret is configured, neither signature header is present.

- Source: [Validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)

**Delivery headers.** Every GitHub webhook POST carries:

| Header | Purpose |
|---|---|
| `X-GitHub-Delivery` | Globally unique GUID per delivery — the stable dedup key |
| `X-GitHub-Event` | Event type name (e.g., `push`, `pull_request`, `issues`) |
| `X-GitHub-Hook-ID` | Unique webhook configuration ID |
| `X-Hub-Signature-256` | HMAC-SHA256 signature |
| `User-Agent` | Always prefixed `GitHub-Hookshot/` |
| `X-GitHub-Hook-Installation-Target-Type` | `repository`, `organization`, etc. |
| `X-GitHub-Hook-Installation-Target-ID` | Resource ID where webhook was created |

- Source: [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads)

**Payload cap.** 25 MB maximum. Larger events are not delivered at all (e.g., a `create` event with many branches/tags pushed at once).

- Source: [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads)

**Content type.** Payloads are delivered as `application/json` or `x-www-form-urlencoded` (configurable at webhook creation). Pluto should accept JSON.

- Source: [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads)

### 1.2 Retries and redelivery

**GitHub does NOT automatically retry failed deliveries.** If the endpoint returns a non-2XX or times out, the delivery is marked failed. The server must manually redeliver from the "Recent deliveries" UI or via the REST API. Redeliveries are available for the past **3 days** only. A redelivered payload carries the same `X-GitHub-Delivery` GUID as the original.

- Source: [Redelivering webhooks](https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/redelivering-webhooks)

**Response time.** GitHub expects a 2XX response within **10 seconds**. If the server takes longer, the connection is terminated and the delivery is considered failed. This means Pluto must acknowledge only after the queue record is durable (write to the state store), and the queue write must complete within 10 seconds.

- Source: [Best practices for using webhooks](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks)

**Implication for Pluto:** The daemon's event receiver must write the queue item to the durable state store *before* returning 2XX. If the write fails, return 5XX so the delivery is recorded as failed and can be redelivered. The 10-second budget is generous for a local file write but leaves no room for network calls or box operations before ack.

### 1.3 Endpoint reachability and HTTPS

GitHub delivers webhooks over **HTTPS only** and verifies SSL certificates by default. The endpoint must be reachable from GitHub's IP ranges. The user owns the network path (reverse proxy, port forward, tunnel); Pluto provides no hosted relay.

- Source: [Best practices for using webhooks](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks)

**IP allow lists.** GitHub publishes its webhook IP ranges via the `GET /meta` API endpoint. The list is not exhaustive (some services like LFS and Packages may not be listed). GitHub discourages IP allow-listing as the primary defense but recommends it as a supplement to signature verification.

- Source: [About GitHub's IP addresses](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-githubs-ip-addresses)

### 1.4 Event types relevant to M4

The M4 spec (#83) requires support for **push**, **pull request**, **issues**, and **generic webhook** events.

**Push event.** Triggered on branch/tag pushes and deletions. Key payload fields: `ref` (e.g., `refs/heads/main`), `before`/`after` SHAs, `pusher`, `repository`, `sender`, `forced`, `deleted`. No `action` field — push events are unconditional.

- Source: [Webhook events and payloads — push](https://docs.github.com/en/webhooks/webhook-events-and-payloads#push)

**Pull request event.** Triggered on PR activity. Key payload fields: `action` (`opened`, `closed`, `reopened`, `synchronize`, `edited`, `labeled`, `unlabeled`, `assigned`, `unassigned`, `review_requested`, etc.), `number`, `pull_request` (with `head.ref`, `head.sha`, `base.ref`, `fork`, `draft`, `merged`), `repository`, `sender`.

- Source: [Webhook events and payloads — pull_request](https://docs.github.com/en/webhooks/webhook-events-and-payloads#pull_request)

**Issues event.** Triggered on issue activity. Key payload fields: `action` (`opened`, `closed`, `reopened`, `edited`, `labeled`, `unlabeled`, `assigned`, `unassigned`, `milestoned`, `demilestoned`, `deleted`, etc.), `issue` (with `number`, `title`, `body`, `labels`, `state`), `repository`, `sender`.

- Source: [Webhook events and payloads — issues](https://docs.github.com/en/webhooks/webhook-events-and-payloads#issues)

**Common payload fields.** Most events include `action`, `repository`, `sender`, and `organization` (when applicable). The `sender` can be the `ghost` user for internal processes — do not assume it always identifies a real person.

- Source: [Webhook events and payloads — common payload parameters](https://docs.github.com/en/webhooks/webhook-events-and-payloads#common-payload-parameters)

### 1.5 Generic webhooks

The M4 spec requires generic webhooks to use their own per-source secret. Unlike GitHub, there is no standard signature scheme for generic webhooks — the secret mechanism is entirely Pluto's design. Options:

1. **Shared secret in a header** (e.g., `X-Webhook-Secret`): Simple but vulnerable to replay if not combined with a timestamp/nonce.
2. **HMAC signature** (e.g., `X-Signature: sha256=<hmac>`): Industry standard (Stripe, Slack, etc.). Pluto can define its own scheme.
3. **Token in URL path** (e.g., `/hooks/<token>/`): Simple but leaks in logs and Referer headers.

**Recommendation:** Use an HMAC-SHA256 signature scheme with a timestamp header (e.g., `X-Webhook-Timestamp` + `X-Webhook-Signature`). Reject deliveries older than 5 minutes to prevent replay. This mirrors GitHub's approach and is self-documenting.

### 1.6 `gh` polling as fallback

The M4 spec identifies server-side `gh` polling as a documented fallback, not the primary implementation. Key facts:

- `gh auth login` stores a token in the system credential store, with plaintext fallback when no store is available.
- `gh` can be used headlessly via `GH_TOKEN` environment variable or `--with-token`.
- Polling the GitHub API for events (e.g., `gh api repos/{owner}/{repo}/events`) is possible but has rate limits (5,000 requests/hour for authenticated users).
- Polling introduces latency (depends on interval) and requires storing a GitHub credential on the host.

- Source: [gh auth login](https://cli.github.com/manual/gh_auth_login)

**Recommendation:** Document `gh` polling as a fallback for hosts that cannot receive inbound HTTPS. The polling path would use `gh api` to fetch recent events, deduplicate by event ID, and feed the same queue. This is strictly worse than webhooks (latency, rate limits, credential storage) but provides a safety net.

### 1.7 What happens when the host is offline

- GitHub delivers to the configured URL. If the host is offline, the connection fails and the delivery is marked failed.
- Failed deliveries can be manually redelivered for up to 3 days after the original attempt.
- If the host is offline for more than 3 days, those events are lost unless the user manually triggers a re-sync (e.g., via `gh` polling or a "catch-up" command).
- Pluto's durable queue ensures that once an event is accepted, it survives daemon restarts. But events that never reached the daemon (host offline) are lost.

**Recommendation:** The M4 spec already addresses this: "Acknowledge an inbound event only after its queue record is durable." For the offline case, document that events during downtime are lost unless manually redelivered within 3 days. A future `gh` polling catch-up could fill gaps.

### 1.8 Manual repository setup

The user configures GitHub webhooks manually via the repository's Settings > Webhooks interface. Pluto provides the endpoint URL and secret; the user pastes them into GitHub. No GitHub App or OAuth login is required for repository webhooks.

- Source: [Creating webhooks](https://docs.github.com/en/webhooks/using-webhooks/creating-webhooks)

**Recommendation:** `pluto init` should print the webhook configuration instructions (endpoint URL, secret, events to subscribe to) so the user can copy-paste them into GitHub's UI. The CLI should also provide a `pluto events status` command to show the endpoint URL and whether the daemon is reachable.

---

## Part 2 — Event Credentials & Trust Boundaries (Issue #85)

### 2.1 Separating webhook verification secrets from job credentials

**Webhook verification secrets** authenticate the *delivery* — they prove the payload came from GitHub (or the configured generic source) and was not tampered with. These secrets are stored on the host and used only by the event receiver to verify signatures before parsing/queueing.

**Job credentials** authenticate *API and Git operations* — they allow the box to clone/fetch private repos, push branches, create PRs, call the GitHub API, etc. These are supplied through the contract's `[env]` and wired into git by the `[provision]` command.

**These are fundamentally different trust domains:**

| Aspect | Webhook secret | Job credential |
|---|---|---|
| Purpose | Verify delivery authenticity | Access private repos/APIs |
| Stored | Host daemon state | Contract `[env]` → box provision |
| Scope | One per webhook/source | One per box/project |
| Trust | Proves sender identity | Grants resource access |
| Leak impact | Spoofed event injection | Unauthorized repo access |

**Recommendation:** Never use the webhook secret as a job credential or vice versa. The webhook secret should be stored in the host's state directory (not in the contract). Job credentials should flow through `[env]` → `[provision]` → git credential helper, never through the event receiver.

### 2.2 Private repo cloning/fetching

The current repo approach (from `docs/contract.md`): "pluto stores no credentials and adds no secret handling. For a private HTTPS remote, supply a token through the top-level `[env]` and have `provision` wire it into git with a credential helper or `url.insteadOf`."

Example from the contract docs:
```toml
[env]
GITHUB_TOKEN = "ghp_..."

[provision]
command = '''
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"
'''
```

**Implication for M4:** Event-triggered boxes (PR boxes, issue boxes) need to clone the repo. If the repo is private, the box needs credentials. The credential must come from the trusted default-branch contract, not from the triggering ref (which may be an untrusted fork).

### 2.3 GitHub API credentials

For jobs that call the GitHub API (e.g., to comment on a PR, update issue status), the box needs a token. Options:

1. **Personal Access Token (classic):** Broad access, tied to a user. Stored in `[env]`, wired by provision. Simple but over-privileged.
2. **Fine-grained PAT:** Limited to specific repos and permissions. More secure but has feature gaps (cannot contribute to public repos where user is not a member, cannot access Packages, etc.).
3. **GitHub App installation token:** Short-lived, well-scoped, but requires a GitHub App (out of scope for M4 per spec).
4. **`GITHUB_TOKEN` (Actions-only):** Not applicable — Pluto is not GitHub Actions.

- Source: [Managing your personal access tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)

**Recommendation for M4:** Use a fine-grained PAT stored in the trusted contract's `[env]`, wired into the box by provision. The token should be scoped to the minimum permissions needed (e.g., `contents:read` for cloning, `pull_requests:write` for PR comments, `issues:write` for issue updates). Document that the token is a user credential and should be rotated regularly.

### 2.4 Job environment secrets

The contract's `[env]` flows to provision, wake, services, jobs, and sessions. The top-level `[env]` is the default; each entity can override per key. Values are literal (no interpolation). The `PLUTO_` prefix is reserved.

**Current behavior:** `[env]` reaches all phases equally. There is no per-job secret isolation — every job sees the top-level env plus its own overrides.

- Source: [Contract reference — env](https://docs/contract.md), [ADR 0007](https://docs/adr/0007-box-contract.md)

**Implication for M4:** If the top-level `[env]` contains a GitHub token, every job in every box for that project sees it. This is fine for trusted branch jobs but dangerous for untrusted PR jobs (see 2.5).

### 2.5 PRs from forks and untrusted contributors

This is the core trust boundary problem. GitHub Actions addresses it with two event types:

- **`pull_request`:** Triggered by PRs from forks. Runs in a restricted context with read-only `GITHUB_TOKEN` and no access to secrets. The workflow runs from the merge commit, not the PR head.
- **`pull_request_target`:** Triggered by PRs but runs in the context of the base repository with write `GITHUB_TOKEN` and access to secrets. Dangerous with untrusted code — GitHub explicitly warns against checking out untrusted PR content in this context.

- Source: [Security hardening for GitHub Actions](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions), [Preventing pwn requests](https://securitylab.github.com/research/github-actions-preventing-pwn-requests/)

**Self-hosted runner risk:** "Self-hosted runners should almost never be used for public repositories, because any user can open pull requests against the repository and compromise the environment." This directly applies to Pluto: a public repo's PR from a fork could trigger a job on the user's host with access to credentials.

- Source: [Security hardening for GitHub Actions — self-hosted runners](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions)

**The M4 spec's approach (from #83):**
- "A PR or issue has one durable event box, keyed by repository and work-item identity."
- "PR boxes start from the PR head ref."
- "Job execution details are read from the triggering ref where one exists."
- "Trusted event policy is read from the registered repository's default branch."
- "The triggering ref supplies the job definition, so a PR can exercise its proposed contract while the trusted policy remains in control of whether and what it may trigger."

This is a **trust delegation model**: the PR's ref supplies *what to run* (the job definition), but the trusted default-branch policy controls *whether it may run* and *which job names are allowed*. This is analogous to GitHub Actions' `pull_request` vs `pull_request_target` split but with a different mechanism.

### 2.6 Default credential access by event type

Based on the M4 spec and GitHub's trust model, the recommended default credential matrix:

| Event type | Trust level | Credential access | Rationale |
|---|---|---|---|
| Push to registered branch | Trusted | Full `[env]` from default-branch contract | Maintainer-controlled code |
| Issue opened/edited | Trusted | Full `[env]` from default-branch contract | Maintainer-controlled repo |
| PR from same repo (trusted) | Semi-trusted | Configured per-job credentials | Maintainer can mark as trusted |
| PR from fork (untrusted) | Untrusted | **No host-held secrets or write credentials** | Changed code cannot use privileged credentials |
| Generic webhook | Configurable | Per-source secret only | Depends on source trust |

**Recommendation:** The trusted default-branch contract should declare which jobs are allowed for each event type and what credentials each job receives. For untrusted PR jobs, the job definition comes from the PR's ref but the credential set is empty (or read-only). The maintainer can explicitly mark a PR as trusted (e.g., via a label or comment) to grant additional credentials.

### 2.7 Where credentials are stored, how they reach the box, and how to keep them out of committed contracts and logs

**Storage:** Credentials live in the contract's `[env]` on the host's worktree. The contract is read by the daemon and sent to the box's agent. The box never parses TOML — it receives the parsed env.

**Path to the box:** `[env]` → daemon → agent → job execution environment. The env is passed as environment variables to the job process.

**Keeping credentials out of committed contracts:** The M4 spec notes: "`.pluto.toml` need not be committed, so the token can sit in a local, uncommitted contract." This is the primary mechanism — the contract with credentials lives only on the host, not in the repo.

**Keeping credentials out of logs:** The current repo has no explicit secret redaction in job logs. GitHub Actions masks secrets in logs automatically. Pluto should implement similar masking: any env value marked as sensitive (or matching a pattern like `*_TOKEN`, `*_SECRET`, `*_KEY`) should be redacted in job output.

**Recommendation:**
1. Mark sensitive env keys in the contract (e.g., a `sensitive = true` flag or a naming convention).
2. Redact sensitive values in job logs (both host-side and in-box).
3. Never write credentials to the queue item's durable record (the queue stores event metadata, not credentials).
4. The webhook secret is stored in the host state directory, never in the contract.

### 2.8 Concrete M4 trust policy

Based on the above research, the recommended M4 trust policy:

1. **Event intake authentication:** Verify HMAC-SHA256 signature (GitHub) or HMAC-SHA256 + timestamp (generic) before parsing/queueing. Reject invalid signatures with 401/403.

2. **Trusted policy resolution:** Read the event policy from the registered repository's default branch contract. The policy maps `(event_type, action, ref_pattern, label_pattern)` → `job_name`. Only jobs declared in the trusted policy can be triggered.

3. **Job definition resolution:** For PR events, read the job definition from the PR's head ref. This allows a PR to propose changes to `.pluto.toml` and test them. But the job name must still be in the trusted policy's allowed set.

4. **Credential injection:**
   - Push/issue jobs: inject the full `[env]` from the default-branch contract.
   - Trusted PR jobs: inject only the credentials explicitly configured for the trusted job name.
   - Untrusted PR jobs: inject no host-held secrets. The job runs with a clean environment (only `PLUTO_WORKTREE` and non-sensitive env).

5. **Maintainer trust marking:** A maintainer can mark a PR as trusted via a label (e.g., `pluto:trusted`) or a comment (e.g., `/pluto trust`). This is recorded in the queue item and elevates the job's credential access.

6. **Box isolation:** Each PR/issue gets its own durable box. The box is created from the PR head ref (for PRs) or default branch + issue branch (for issues). The box's provision wires credentials from the trusted contract, not from the triggering ref.

### 2.9 Unresolved risks

1. **Secret leakage through job output:** If a job prints its environment (e.g., `env` or `printenv`), credentials leak to logs. Pluto needs log redaction or explicit warnings.

2. **Credential persistence in the box:** Once provision wires a credential into git (e.g., `url.insteadOf`), it persists in the box's `.git/config`. If the box is later used for an untrusted job, the credential is still there. Mitigation: use a credential helper with a short TTL, or re-provision on trust level change.

3. **Fork repo access:** A PR from a fork needs to clone the fork's repo. The fork's URL is in the PR payload. But the credential from the trusted contract may not have access to the fork (if the fork is private and the token doesn't cover it). This needs testing.

4. **Webhook secret rotation:** If the webhook secret is rotated, the old secret must be rejected. Pluto should support a grace period or dual-secret verification during rotation.

5. **Generic webhook replay:** Without a timestamp/nonce, a captured generic webhook payload can be replayed. The HMAC + timestamp scheme mitigates this but requires clock synchronization.

6. **`gh` credential storage:** The `gh` polling fallback stores a user credential on the host. If the host is compromised, the credential is exposed. This is inherent to the polling approach and should be documented as a trade-off.

---

## Part 3 — Recommendations Summary

### For Issue #84 (webhook delivery)

1. **Implement GitHub webhook intake** with `X-Hub-Signature-256` verification using `crypto.timingSafeEqual` (Go's `hmac.Equal`).
2. **Use `X-GitHub-Delivery` as the dedup key.** Store it in the queue item and reject duplicates.
3. **Acknowledge only after durable queue write.** Write to the state store, then return 2XX. If the write fails, return 5XX.
4. **Implement generic webhook intake** with HMAC-SHA256 + timestamp verification. Reject deliveries older than 5 minutes.
5. **Require HTTPS.** Document that the user must provide a reverse proxy or tunnel. Pluto does not terminate TLS.
6. **Subscribe to minimum events.** Only `push`, `pull_request`, `issues`, and `ping` (for health checks).
7. **Document `gh` polling as a fallback** for hosts without inbound HTTPS. Use `gh api` to fetch events, deduplicate by event ID, feed the same queue.
8. **Handle the offline case:** Events during host downtime are lost unless manually redelivered within 3 days. Document this limitation.
9. **Provide `pluto init` webhook setup instructions** — print the endpoint URL, secret, and GitHub configuration steps.

### For Issue #85 (credentials & trust boundaries)

1. **Separate webhook secrets from job credentials.** Webhook secrets live in host state; job credentials live in contract `[env]`.
2. **Use the trusted default-branch contract as the policy authority.** Map event type/action/ref/label → job name. Only declared jobs can be triggered.
3. **For untrusted PR jobs, inject no host-held secrets.** The job runs with a clean environment.
4. **For trusted branch/issue jobs, inject the full `[env]`** from the default-branch contract.
5. **For trusted PR jobs, inject only configured per-job credentials.**
6. **Implement log redaction** for sensitive env values (pattern-based: `*_TOKEN`, `*_SECRET`, `*_KEY`, etc.).
7. **Never store credentials in the queue record.** The queue stores event metadata only.
8. **Document the credential path:** `[env]` → daemon → agent → job env. Credentials are never in the queue, never in the event payload.
9. **Support maintainer trust marking** via label or comment to elevate a PR's credential access.
10. **Document the `gh` polling credential trade-off** — it stores a user credential on the host.

---

## Part 4 — Edge Cases

1. **Duplicate delivery (same `X-GitHub-Delivery`):** GitHub may redeliver the same event. The queue must deduplicate by source + delivery GUID. If the GUID is already in the queue, return 2XX without creating a new item.

2. **Redelivery after 3 days:** GitHub's redelivery window is 3 days. If a delivery fails and is not redelivered within 3 days, it is lost. Pluto should log a warning if it sees a delivery older than 3 days (indicating a missed event).

3. **Payload size > 25 MB:** GitHub will not deliver such events. Pluto should document this as a known limitation.

4. **Webhook secret rotation:** During rotation, both old and new secrets should be accepted for a short window. Pluto should support a `previous_secret` field.

5. **Generic webhook clock skew:** If the sender's clock is more than 5 minutes off, legitimate deliveries will be rejected. Document this and allow the window to be configured.

6. **PR from a private fork:** The PR payload contains the fork's repo URL. The trusted credential may not have access to the fork. The job will fail at clone time. This should be surfaced as a visible failure, not a silent skip.

7. **Issue with no associated PR:** An issue event creates a new box from the default branch. The issue's body/labels are available in the event context but the job must interpret them (Pluto does not hard-code issue semantics).

8. **Concurrent events for the same PR:** Two events for the same PR (e.g., `synchronize` then `labeled`) should queue two jobs on the same box. The box's job runner serializes them (one at a time).

9. **Event for a deleted branch:** A push event with `deleted: true` should not start a job. The trusted policy should filter these out.

10. **`ghost` user as sender:** Some events have `sender.login == "ghost"`. The trusted policy should not rely on sender identity for access control.

11. **Queue full:** When the queue is at capacity, reject new automatic work with a visible reason. The webhook receiver should return 5XX so the delivery is recorded as failed and can be redelivered.

12. **Daemon restart during webhook processing:** If the daemon crashes after writing the queue item but before returning 2XX, GitHub will redeliver. The dedup key prevents a duplicate job.

---

## Part 5 — File/Code Pointer List

### Repo files relevant to M4 implementation

| File | Relevance |
|---|---|
| `internal/daemon/daemon.go` | HTTP server, scheduler loop, job execution. Event receiver and queue logic extends this. |
| `internal/daemon/schedule.go` | Scheduler loop, `fireDueSchedules`, `launchFire`. Event queue priority/aging extends this. |
| `internal/state/state.go` | `Store`, `Box`, `CreateBox`, `mutate`, `writeFileAtomic`. Queue item storage extends this. |
| `internal/state/job.go` | `Job`, `BeginJob`, `SetJob`, `JobRunning`. Event job records extend this. |
| `internal/state/schedule.go` | `Schedule`, `SetSchedules`, `AdvanceSchedule`. Event queue items are analogous. |
| `internal/contract/contract.go` | `Contract`, `Load`, `Parse`, `ExecJob`, `EnvFor`, `MergeEnv`. Event policy and job definition extend this. |
| `internal/runner/job.go` | `RunJob`, `JobLog`, `finishJob`. Event job execution extends this. |
| `internal/runner/boxrun.go` | `BoxRun`, `BoxHolder`. Box lifecycle for event-created boxes. |
| `internal/api/api.go` | Wire types (`RunRequest`, `RunEvent`, `Error`). Event queue API types extend this. |
| `docs/contract.md` | Contract reference. Event policy and credential sections extend this. |
| `docs/adr/0007-box-contract.md` | Box contract ADR. Event policy decisions extend this. |
| `docs/adr/0003-triggers-are-durable-alarms.md` | Trigger durability ADR. Event queue semantics extend this. |
| `docs/remote-access.md` | Remote access docs. Event endpoint reachability extends this. |
| `docs/research/trigger-comparables.md` | Trigger comparables research. Event delivery semantics informed by this. |

### GitHub documentation sources

| Source | URL |
|---|---|
| Validating webhook deliveries | https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries |
| Best practices for using webhooks | https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks |
| Webhook events and payloads | https://docs.github.com/en/webhooks/webhook-events-and-payloads |
| Handling webhook deliveries | https://docs.github.com/en/webhooks/using-webhooks/handling-webhook-deliveries |
| Redelivering webhooks | https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/redelivering-webhooks |
| About GitHub's IP addresses | https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-githubs-ip-addresses |
| Security hardening for GitHub Actions | https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions |
| Contexts reference (Actions) | https://docs.github.com/en/actions/reference/workflows-and-actions/contexts |
| Managing your personal access tokens | https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens |
| gh auth login | https://cli.github.com/manual/gh_auth_login |
| Preventing pwn requests (GitHub Security Lab) | https://securitylab.github.com/research/github-actions-preventing-pwn-requests/ |

---

*End of research notes.*
