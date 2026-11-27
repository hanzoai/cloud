# LLM.md — apps/projects

Guidance for apps/projects. Moved from the root LLM.md; cross-cutting rules stay in the root.

### A site's namespace is its GitHub org

- The connection is the proof, because a repository URL is the caller's to
  write: without it any org could write under `hanzoai/` and an edge route that
  serves that prefix by path would serve it. A GitHub account is bound to an org
  by GitHub's consent screen (the connect flow) or a SuperAdmin's
  `POST /v1/provider/github/claim`. Nor may a namespace be the name of an org
  this store keeps sites for (`Store.Keeps`): whoever owns github.com/zoo is not
  org zoo, and `zoo/` holds zoo's sites.
- Decided at create and whenever an update sends `repo`, and stored
  (`projects.namespace`, backfilled to the org; unique with the slug). A
  repository in another account is a relink: its namespace is the site's or the
  update is refused. The same repository sent again (a console saving the whole
  body) moves the site only where `move` allows, else leaves it be (`relink`).
  A deployed integrations app that cannot answer refuses a create or a relink
  (503), never an edit that changed no link.
- A move is refused (409, `move`) for a site with releases, for a published
  site whose `index.html` is not already copied to the new prefix (same ETag),
  and into a GitHub namespace where anything is already kept for the slug — a
  hand-copied prefix an edge route serves cannot be taken by claiming its slug.
- Moving a live site: copy `<old>/<slug>/` to `<new>/<slug>/`, relink
  (`PATCH /v1/projects/<slug>` with the repo), switch the universe routes that
  name the old prefix, verify, delete the old prefix. The server copies nothing.
- Storage is billed to the IAM org: `footprint` sums `<org>/`, less sites other
  orgs keep under a namespace of that name, plus the org's own sites kept away.
  A release is never promoted from such a lodged site's bytes.

### Site releases already have a lifecycle — do not build a second one

`apps/projects` owns the full versioned-release model for static sites, and it is
the ONE way:

- `<namespace>/.releases/<slug>/rel_<128-bit manifest digest>/` — immutable, content-
  addressed, and a SIBLING of the mutable `<namespace>/<slug>/` prefix, so neither a
  full-artifact deploy nor a project delete (both of which purge that subtree) can
  shred a release the pointer still names.
- `Store.ActivateRelease` — the flip is one atomic `UPDATE … WHERE EXISTS (release
  row)`, so it cannot point a site at a release that was never created, and two
  concurrent activations cannot leave the pointer disagreeing with whichever won.
  `MarkLive` deliberately does NOT touch `current_release`.
- `servePrefix` (`apps/projects/sites.go`) — the ONE read rule, re-validating the
  id against `releaseIDRE` before it can widen a prefix. An unrecognized id falls back
  to the legacy prefix, so there is no flag day and no migration.
- Rollback is activating an older id. Routes are already mounted on both site
  surfaces via `siteReleases`.

A parallel `clients/cd` + `clients/site` lifecycle (kind-agnostic `Target`, a
`CURRENT` pointer object next to the bundles) was built and then DELETED unmerged: it
re-implemented all of the above with a weaker pointer — a `v<N>` counter instead of a
content digest, and a plain PUT that could name a release whose row does not exist.
Its one genuinely new finding was a gap in THIS lifecycle, closed below rather than
answered with a second system.

Two consequences, both load-bearing:

- **The live release is not a candidate at any depth.** It is excluded from the
  prune scan (so it never counts against the budget — a site parked on an ancient
  release keeps it), AND every prune `DELETE` re-checks `current_release` at delete
  time, so a rollback landing after the scan makes that delete match zero rows. Rows
  go first, bytes after — `promote`'s ordering run backwards — so a surviving row
  still means "the prefix is complete", and a crash between the two leaks objects
  nothing points at rather than serving a 404.
- **`activate` verifies BYTES, not just the row.** `ActivateRelease`'s
  `WHERE EXISTS (release row)` was sufficient only while nothing could prune bytes
  out from under a row. It now stats the release's `index.html` before the flip:
  missing row → 404 (no cross-tenant oracle), missing bytes → **410 Gone**. Without
  it, activating a reclaimed release returns 200 and takes a live site down.
  `ListReleases` and `PruneReleases` share one order (`created_at DESC, rowid DESC`)
  because `created_at` is second-granular and the menu a caller sees must be exactly
  the set retention keeps.

**Three transports, one trigger.** A push reaches `cloud.OnGitPush` — the
single-registrant client, never a second CI — from the embedded git server
(`apps/git/smart_http.go`), the GitHub App (`/v1/connector/github/webhook`), and
the canonical forge (`POST /v1/platform/hook`, `apps/platform/hook.go`). The third
exists because git.hanzo.ai is a SEPARATE process: its pushes never touch our
receive-pack, so without that endpoint the host we call canonical builds nothing and
only the mirror releases. It lives in **platform** because the builder does —
served from `apps/git` it verified every delivery, answered 204, and dispatched to
a registrant that is nil in that process forever (`apps/git/webhook.go` is the 410
naming the new address). Both webhook transports HMAC-verify fail-closed and drop
bot-authored pushes through the one `cloud.IsBotActor`, so a release cannot
retrigger itself. `apps/platform` is the only place that decides what a push
MEANS: an app tracking the repo rebuilds, and cloud's own upstream cuts a release.
Cloud is the machine, so it calls the release in-process and the build token is
never handed to a caller. The trigger runs BEFORE the token mint and the mirror,
because a build reads from GitHub and must not be lost to a sync outage.

**One repository, two spellings.** `normRepo` drops the HOST on the mirrored pair
— the upstream and this deployment's forge — so `github.com/hanzoai/cloud` and
`git.hanzo.ai/hanzoai/cloud` are the same repository while the migration runs.
Without it every forge delivery matched no application, built nothing, and looked
green. The path is compared WHOLE and never by its last two segments: the embedded
git server's clone URL is `/v1/git/<org>/<project>/<repo>`, whose tail a tenant
names, and a project `hanzoai` holding a repo `cloud` would otherwise spell the
release repository's own coordinate.

The org an inbound webhook belongs to comes from the App INSTALLATION id via the
`connections` row written by the install callback. Without that row every delivery is
acked `200 {"ignored":"unknown installation"}` and silently does nothing — a 200 on
that path is not evidence it worked; check for sync/build activity.

## Names under hanzo.app are one namespace

An app answers at `<name>.hanzo.app`, one label under the apex, because the apex's
certificate covers exactly one; there is no `<app>.<org>.hanzo.app`. App names (store
apps, declared apps, previews, runs) and project-site slugs are ONE namespace, unique
across every org, first come first served: the projects store's `site_hosts` row for the
bare name is the claim. A site holds its slug from create (`insert` → `Store.Claim`); the
platform claims an app's over the plane (`client.SitesClaim`, holder `app:<id>`, released
by `SitesRelease` on delete). A name another org, app or site holds, or one
`sites/reserved.go` reserves, is a 409 at create — except a deploy through
`POST /v1/platform/apps` that names no `host` (the builder's "Add to project", #769, the
owner's call of 2026-11-16): it takes the first of `<name>`, `<name>-<org>`,
`<name>-<org>-2` … `-5` that is free or already its own (`ownNames`, apps/platform
names.go, each a label of at most 40), and `notice` says which. The order is fixed and a
claim of a name the org's app already holds succeeds, so the same deploy asked again (the
dialog's "Ship to main" re-sends the name) lands on the same app. A named `host`, and a
site's slug, are still 409. Under the apex
an org owns exactly its own apps' names: `validateOrgDomains`, `declareHost` and
`addDomain` refuse every other `*.hanzo.app` host, and `appHosts` renders an app's own
name first and drops any other apex host.

## An estate site keeps what it served, so it can be rolled back (#432)

A site published by a deployment serves in place (`<namespace>/<slug>/`), so a bad
publish could only be undone by publishing again. Now, when a deployment of an estate
org's site (`forge.Owner`: hanzo, lux, zoo, adnexus) goes live (`land`, `publishSite`),
`record.go` `keep` copies the live prefix into a release in the background: the same
content-addressed value `promote` makes, at `<namespace>/.releases/<slug>/<id>/`, written
LAST, with `deployments.release_id` linking the deployment to it. The copy keeps each
object's own metadata (content type, encoding, cache policy as the publisher wrote them;
`copyRelease` replaces them), is conditional on the listed ETag, and is bounded by
`keptFiles`/`keptBytes` (hanzo.ai serves about 10k files). Retention keeps
`keptReleases` (3). A customer's site is not kept: it is billed for what it stores.

Rolling back is `activate` of an earlier release, the same flip `POST
/v1/project/{slug}/releases/{id}/activate` makes, and the next deployment takes the
pointer back. The deploy console asks over the plane: `projects_site_apps` (the
caller's sites; one site with its history) and `projects_site_rollback`. Rolling
back to the release it serves is 409. A keep that fails is logged ("site not kept as a
release") and the site is untouched; rollback reaches only releases kept since this
shipped.

