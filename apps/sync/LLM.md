# LLM.md — apps/sync

Guidance for apps/sync. Moved from the root LLM.md; cross-cutting rules stay in the root.

## GitHub → forge sync: every ref, one App, nothing per-repo

git.hanzo.ai is canonical and GitHub is its mirror, so an inbound push is an
*import*: it carries work the mirror received back to the canonical copy. The whole
configuration surface is below, because the shape people expect (a per-repo sync
setting) does not exist and should not be added.

**There is no per-repo and no per-ref configuration.** A repo is not enrolled, and a
ref is not filtered. `apps/integrations/github_webhook.go` accepts any ref under
`refs/`, and `Ref` stays a FULL ref (`refs/heads/x`, `refs/tags/v1.2.3`) the length of
the chain — webhook → `SyncEvent` → `sync.Event` → `GitInboundReq` → `inboundFastForward`
→ `GitPushEvent`. Tags matter here: a version is published by tag, so a filter on
`refs/heads/` left every release existing only on the mirror.

**Carrying tags needs no force, and that is what makes "everything" safe.** The
inbound advance uses a non-forcing refspec, so re-pointing a tag that already exists
is not a fast-forward and git refuses it — the canonical copy keeps the tag it
published. A branch delete is likewise never propagated (`ignored: branch delete not
propagated`): the mirror does not get to delete canonical history.

**A consumer that wants a branch cuts the prefix itself.** `strings.CutPrefix(ev.Ref,
"refs/heads/")` answers "is this a branch" and "what is its name" in one total step,
so a tag can never rebuild an app that tracks a branch (`apps/platform/push.go`),
and `refs/tags/main` is not `refs/heads/main` for the release check.

**Tenancy comes only from the installation id** in the HMAC-verified body — never a
header, never a client-controlled field. That is the whole tenant resolution:
`OrgForExternalID("github", installation.ID)`.

### Exactly one GitHub App — two is not redundancy

So pointing a second App at the same webhook with the same secret does not double
coverage — it makes the two Apps contend for one row. The HMAC passes for both
(shared secret), but only the most recent install's id is stored, and a delivery from
the other App resolves to no org and is acknowledged `200 {"ignored": "unknown
installation"}` so GitHub does not retry-storm. The failure is therefore SILENT at
both ends: GitHub shows a green delivery, and nothing arrives. If the stored id and
the configured key belong to different Apps, token minting fails for every event
instead. Run one App; install it on every org whose repos should sync.

**Reading the live configuration is already a route — do not add a second one.**
`GET /v1/integrations` (`providerViewFor`) reports, for the `github` provider,
`available` (the App's creds are present in this process) and, when the org has a
connection, `connection.externalId` — **the stored installation id**. That id is the
one value that decides whether a delivery resolves, so comparing it against the
`installation.id` GitHub shows on a delivery is the whole diagnosis: equal ⇒ the event
lands; different ⇒ it is the silent `unknown installation` ack, and a second App is
usually why.

### The knobs that do exist

Process values, all with working defaults — none of them selects *what* syncs:

- `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET`, `GITHUB_APP_SLUG`
  — the single App identity; absent ⇒ the plane reports itself unconfigured.
- `GITHUB_API_URL` — API base, for GitHub Enterprise.
- `GITHUB_IMPORT_CONCURRENCY` — parallel repo imports on first connect.
- `GIT_MIRROR_OUT_CONCURRENCY`, `GIT_MIRROR_OUT_TIMEOUT` — the outbound leg.
- `GIT_SYNC_ACTOR` — the actor recorded for a sync-initiated write.

**What genuinely is per-repo is a different plane**, and naming it keeps the two from
being confused: `/v1/git/repos/:name/*` (`apps/git/subscriptions.go`) holds a
repo→Slack-channel subscription and a repo→downstream mirror target. Those are
reactor config, org-scoped like every repo route. The code index is also per-repo and
indexes the default branch only — a feature-branch push is skipped so it cannot
clobber the canonical index. None of these decide whether a ref syncs.

### Which repositories sync: a repo sync each, or an account sync for all

A synced repository is a `/v1/sync` row; an App import alone is a one-time copy
(a push resolves to sync rows, so an import with no row never advances). An org
that wants a whole GitHub account declares ONE account sync —
`POST /v1/sync {"source":{"provider":"github","locator":"https://github.com/<account>"},
"direction":"pull","trigger":"poll","run":true}` (`apps/sync/account.go`, target
`*`). Reconciling it moves no ref: it declares a repo sync per repository the
installation grants on that account (`integrations_github_account`, asked of the
provider process over the plane), inheriting
direction/trigger/actor, and queues each import. A manual run and the poll sweep
(`CLOUD_SYNC_RECONCILE_INTERVAL`, 60m in prod) read the whole grant; a push or a
`repository` webhook (created/transferred/unarchived) declares the one repository
it names at once. Which repositories are covered is the installation's grant on
GitHub ("All repositories" = every one, created later included).

The forge namespace: estate orgs from `forge.Owner`; every other org from the
project store's made workspace (`forge.Workspace`, `forgeAt(…, create)`), made on
first import. A repo sync's copy is `<owner>/<account>_<name>` on git.hanzo.ai.
`GET` and `DELETE /v1/git/repos/:name` reach a made workspace's repository when
the git app's own store holds no such name (`madeGet`, `madeDelete`, as the
machine), so Projects' Remove deletes a customer's copy; support mode deletes
nothing there, as it makes nothing (`madeCreate`).

`GET /v1/sync` rows carry `scope` (repo|account) and, for a repo link, `native`:
forge url/clone/ssh, default branch, size, status (synced|conflict|pending|paused)
and the last advance — two reads per list (forge inventory, outcome roll-up), and
absent rather than guessed when either could not be read. `/v1/git/repos` is a
different store (the retired local bare repos) and never lists synced repositories.

The App key lives in the provider (integrations) process and sync is another
process, so sync never calls `provider.*` in-process: a poll's fetch token and an
outbound push's token are minted by `integrations_github_grant` (read-only unless
the link pushes), the account listing by `integrations_github_account`. Calling
`provider.InstallationToken` from sync answered "integrations: not mounted" in
production, and every private repository's poll fetched anonymously and failed
("could not read Username for 'https://github.com'").

### Every linked repository is a project

`GET /v1/projects` is the one list hanzo.ai and platform.hanzo.ai read, and a
linked repository is a row in it. Declaring a repo link (`POST /v1/sync`, or an
account sync declaring one per repository) and a GitHub-App import
(`POST /v1/provider/github/repos/import`) each ask the projects app over the plane,
`projects_link` (`apps/projects/link_rpc.go`): the project already building from
that repository (matched with or without `.git`, case-blind) or a new one named
`<account>/<repo>` under the first free slug of `<account>-<repo>`, `draft`,
public. It gets no community copy at link time (an account link makes hundreds);
its first update does (`share`), as for every project.

`apps/git/mirror_out.go` (native → GitHub) asks the same op for a token reaching
the one target repository, as the pushing org, and revokes it when the push ends.
No app calls `provider.InstallationToken` across a process any more.

**A URL a tenant names carries the tenant's credential or none.** Sync upstreams
and downstreams, `/v1/git/repos/:name/targets` and the plane's `/git/mirror` are
all tenant-named, so a call with no grant of its own is anonymous and a private
remote fails closed: `apps/sync` `credAuthHeader` presents only the `gitCred` its
caller passed (`forgeRemote` passes the forge machine credential; `upstream` and
`outboundCred` pass the org's grant or nothing), and `mirrorCredential` presents
the org's grant or nothing. The deployment's per-host token (`internal/gitcred`,
`GIT_MIRROR_TOKEN_<HOST>`) is read for the deployment's own work only: the
community replica on its own GitHub account (`community_github.go` `secret`).
A push presents it only to an EXACT match (`ownReplica`): the target row is on
the org-level repository the community registered, and its URL equals
`replica(org, repo)` for that repository's nameRE-valid name — so an encoded path
or a name under the org's prefix that no repository registered gets nothing.
Pinned by `TestOwnReplicaIsAnExactMatch`, `TestNoCredentialPresentsNone`,
`TestADeclaredRemoteCarriesOnlyItsOwnCredential`,
`TestADeclaredTargetNeverCarriesTheDeploymentCredential` and
`TestOnlyTheOrgsOwnReplicaCarriesTheDeploymentCredential`.
