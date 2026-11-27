# `/v1/project` — Shared Projects API (contract)

The ONE org-scoped store of buildable/deployable sites. The SAME records are
read/written by **hanzo.app** (the builder) and **console.hanzo.ai** (the
Projects module). Both call this surface through the gateway; there is no second
copy of project state.

Owner: `hanzoai/cloud` → `apps/projects`. Mounted into the unified cloud
binary (HIP-0106), reachable at `https://api.hanzo.ai/v1/project`.

## Auth & tenancy (HIP-0111)

- Authenticate via the `@hanzo/iam` SDK; send the IAM bearer token to
  `api.hanzo.ai`. The **gateway** validates the JWT and injects `X-Org-Id`,
  `X-User-Id`, `X-User-Email` (and strips any client-supplied copies).
- Every route is **org-scoped**: the tenant is the gateway-minted `X-Org-Id`,
  resolved from the JWT's signed `orgs` **membership** claim — *never* from
  `owner`, which IAM stamps with the **application's** org, so the same human
  authenticating through two apps carries two different `owner` values. No org →
  `403`. Two orgs can hold the same `slug`; `(org, slug)` is unique.
- Never send `X-Org-Id` from the browser — it is ignored/stripped at the edge.

## Resources

### Project

```ts
type Project = {
  id: string;                    // "proj_<token>"
  org: string;                   // tenant slug (from JWT): who owns and pays for it
  namespace: string;             // first prefix segment: the GitHub org of `repo` when `org` holds that GitHub account, else `org` (namespace.go)
  slug: string;                  // org-unique handle; ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$
  name: string;                  // display name
  description?: string;
  repo: { url?: string; branch?: string; provider?: string }; // provider: github|gitlab|bitbucket|git
  framework: string;             // static|vite|next|react|astro|svelte|vue|remix|nuxt
  status: "draft" | "building" | "live" | "error";
  liveUrl?: string;              // set once deployed
  bucket?: string;               // S3 bucket holding the site
  currentDeploymentId?: string;  // "dep_<token>"
  createdAt: number;             // unix seconds
  updatedAt: number;
};
```

### Deployment

```ts
type Deployment = {
  id: string;                    // "dep_<token>"
  projectId: string;
  version: number;               // monotonic per project, 1-based
  status: "queued" | "building" | "uploading" | "live" | "error";
  source: "upload" | "git";
  commit?: string;
  liveUrl?: string;
  bucket?: string;
  prefix?: string;               // "<namespace>/<slug>"
  files: number;
  bytes: number;
  message?: string;              // error or note
  createdAt: number;
  updatedAt: number;
};
```

## Endpoints

| Method | Path | Body | Returns |
|--------|------|------|---------|
| `POST` | `/v1/project` | `CreateProject` | `201 Project` |
| `GET` | `/v1/project` | — | `200 Project[]` (org, newest-updated first) |
| `GET` | `/v1/project/:slug` | — | `200 Project` / `404` |
| `PATCH` | `/v1/project/:slug` | `UpdateProject` | `200 Project` |
| `DELETE` | `/v1/project/:slug` | — | `204` (also purges the live S3 site) |
| `POST` | `/v1/project/:slug/deploy` | artifact | `200 Deployment` |
| `POST` | `/v1/project/:slug/purge` | — | `200 Project` (flush the edge cache-tag; no redeploy) |
| `POST` | `/v1/project/:slug/deployments` | `{ commit?: string }` | `202 Deployment` (queued, with `upload` grant) |
| `GET` | `/v1/project/:slug/deployments` | — | `200 Deployment[]` (newest version first) |
| `GET` | `/v1/project/:slug/deployments/:id` | — | `200 Deployment` / `404` |
| `GET` | `/v1/project/:slug/deployments/:id/objects` | — | `200 Objects` / `404` / `503` |
| `POST` | `/v1/project/:slug/deployments/:id/complete` | `Complete` | `200 Deployment` / `400` / `404` / `409` / `502` / `503` |

```ts
type CreateProject = {
  name: string;                  // required
  slug?: string;                 // defaults to slugify(name)
  description?: string;
  framework?: string;            // defaults to "static"
  repo?: { url?: string; branch?: string };
};

type UpdateProject = {           // all optional; only provided fields change
  name?: string;
  description?: string;
  framework?: string;
  repo?: { url?: string; branch?: string };
};

type Complete  = {
  status: "live" | "error"; commit?: string; liveUrl?: string; message?: string; files?: number; bytes?: number;
  keys?: string[];               // the whole release, relative to the prefix
  copyFrom?: string;             // another site of this org whose prefix holds `copy`
  copy?: string[];               // keys to copy from it; each also in `keys`
};
type Objects   = { objects: { key: string; size: number; etag: string }[] };  // key relative to the prefix; etag unquoted (MD5 hex for a one-part object)
```

Errors are JSON `{ "error": string, "code": number }` with the matching HTTP
status (`400` validation, `403` no org, `404` not found, `409` slug taken,
`502/503` deploy/storage failures).

## Billing

Publishing is free: no deploy, release or build completion is charged. A site
is billed for the storage its org holds in the sites bucket — everything under
`<org>/`, retained releases included — past the free tier (5 GB, the object-storage
row of `@hanzo/plans`), at $0.02 per GB-month, metered hourly as `hosting` usage
(`apps/projects/storage.go`). An org past the tier whose wallet cannot pay a month
of that storage is refused `402` before a publish writes anything (`503` when
commerce cannot be asked); inside the tier a publish never asks.

## Deploy pipeline (two modes)

1. **Artifact (builder one-click).** `POST /v1/project/:slug/deploy` with the
   request body set to a **tar** or **tar.gz** of the BUILT site (must contain
   `index.html` at the root; bounded by the gateway body limit). The site is
   unpacked to OUR S3 at `s3://<bucket>/<namespace>/<slug>/`, the bucket is marked
   public-read, and the deployment lands `live` with a `liveUrl`. Synchronous.

2. **CI (never local).** `POST /v1/project/:slug/deployments` returns `202` with
   a `queued` deployment and an `upload` grant: a presigned POST policy confined to
   the deployment's prefix, 30 minutes long (`grant.go`). For sites too large to
   stream through the API. The publisher (hanzoai/ci `bin/site`):
   1. reads `GET /v1/project/:slug/deployments/:id/objects`, the store's own record
      of the prefix (key, size, ETag; a one-part object's ETag is the MD5 of its
      bytes; a store that cannot be listed is `503`, never a partial list);
   2. uploads with the grant only the files that differ from it or are absent;
   3. calls `POST /v1/project/:slug/deployments/:id/complete` with `status: "live"`
      and `keys`, the whole release. `copyFrom` + `copy` ask for keys to be copied
      from another site of the org inside the store instead (`404` for a site the
      org does not hold, `400` for a copy key not in `keys` or not a clean relative
      path, `502` naming the key when a copy fails).

   A live completion with `keys` lists the prefix once and answers `409` while any
   key is absent, naming how many and the first 20; a store it cannot list is
   `503`. Every refusal leaves the deployment `queued` — a release that cannot be
   proven whole is never marked live — so the publisher uploads what is missing and
   completes again. Without `keys` nothing is checked or deleted.

   Once live, the prefix is reconciled against `keys` (best-effort; a failure
   leaves stale files, never a 500). Immutable keys — those the edge caches for a
   year (`sites.Immutable`: fingerprinted assets, everything under `_next/static/`
   and `assets/`) — get a grace period so a tab opened on the previous release can
   still lazy-load its chunks: every immutable key the release names whose
   `LastModified` is older than 12 h is re-stamped (copied onto itself with its
   metadata replaced by its own, so only `LastModified` moves), and a dropped
   immutable key is kept while its `LastModified` is within 36 h (12 h of re-stamp
   slack + 24 h of grace). Every other dropped key is deleted.

`liveUrl` is `https://<slug>.hanzo.app` — the one host shape a wildcard Ingress
and a wildcard cert can route and secure. That bare `<slug>` namespace is
**global and first-come across every org**, so `liveUrl` is set only when the
deploy actually WON the binding; a project whose slug is already claimed (or is
a reserved label) deploys normally, its bytes are live at its own S3
prefix, and it comes back with an **empty `liveUrl`** plus a `message` saying to
rename. It is never given the URL that serves the other tenant.
GitHub export is an optional, separate step — going live never requires it.

## Edge cache purge (no redeploy)

`POST /v1/project/:slug/purge` flushes the project's edge cache-tag
(`site-<org>-<slug>` — the SAME tag deploy purges) so already-published content is
re-fetched from the S3 origin at the edge. It **never** writes or deletes the S3
origin: the live build keeps serving; only stale edge copies drop. It stamps
`lastPurgeAt` and returns the updated `Project`. An unconfigured/failing edge (CF)
is non-fatal — the purge is a no-op and still returns `200`.

## Console module notes

- List view → `GET /v1/project`. Status badge from `status`; "Open" links to
  `https://hanzo.app/build/<slug>`; "Visit" links to `liveUrl`. Every repository
  the org links (`POST /v1/sync`, an account link, a GitHub-App import) is a row
  here: named `<account>/<repo>`, `draft` until deployed (`link_rpc.go`).
- Detail view → `GET /v1/project/:slug` + `GET /v1/project/:slug/deployments`
  for the deploy history/timeline.
- Create/rename/link-repo → `POST`/`PATCH`. Deploy/redeploy buttons open a
  deployment (`POST /v1/project/:slug/deployments`) for repo-linked projects.
- Do not cache across orgs; the org is implicit in the token. Switching org in
  the console re-fetches with the new token (new `X-Org-Id`).

## Convergence — one site plane (retires the static crs/ plane)

Two mechanisms serve S3-backed static sites today; they collapse into ONE:

1. **Projects PaaS (canonical).** A site is a `Project` in THIS store; its bundle
   lives at `<bucket>/<namespace>/<slug>/`; `apps/sites` is the host-router that turns
   `<slug>.hanzo.app` (and bound custom domains) into that site. DB-backed,
   tenant-isolated, versioned, metered. The `/v1/deploy` dashboard already projects
   these (see `apps/deploy/sites.go`).
2. **Static crs/ plane (LEGACY — retires).** A hand-declared `staticFiles`
   Middleware + IngressRoute per site (`universe infra/k8s/operator/crs/
   static-sites.yaml`) serving `s3://cdn/<slug>` on `<slug>.hanzo.ai`, straight from
   the ingress with no store, no build, no versioning. It was *easy* (one route to
   hand-write) but not *simple* — a second serving path + a second S3 layout + a
   second state home for exactly the job Projects already does.

**Decision:** every first-party site (`cd`, `flow`, `gallery`, `yadota`, …) becomes
a `Project`, served by `apps/sites`. No external-prefix special case in the
resolver: the bundle MOVES to the one canonical layout `<bucket>/<namespace>/<slug>/`
(the resolver keeps computing `sitePrefix(namespace, slug)` — one way, no branch). The
`<slug>.hanzo.ai` host is a bound host routed through cloud, exactly like
`<slug>.hanzo.app`.

**Per-site migration (idempotent, gated, lowest-risk first; `cd.hanzo.ai` LAST):**
1. Copy the bundle to the canonical layout:
   `s3 cp --recursive s3://cdn/<slug>/  s3://<blob.bucket>/hanzo/<slug>/`.
2. `POST /v1/project` (org `hanzo`) → `{slug, framework:"static", status:"live"}`,
   then record its live Deployment (prefix `hanzo/<slug>`) so the resolver serves it.
3. `BindHost` the first-party host `<slug>.hanzo.ai` to the project (the reserved-
   label guard in `apps/sites/reserved.go` still applies).
4. Route `*.hanzo.ai` first-party site hosts through cloud, mirroring the
   `*.hanzo.app` wildcard edge (`universe .../crs/hanzo-app-sites.yaml`: wildcard
   Ingress → `cloud:8000`, cloud's `apps/sites` runs FIRST, before identity).
5. Delete that site's `staticFiles` Middleware + IngressRoute from `static-sites.yaml`.
   Verify the host still serves (now via cloud) BEFORE removing the crs/ route.

Steps 1–2 are additive (no live-routing change — the crs/ route keeps serving until
step 4/5). The cutover (4/5) is a reviewed per-host flip; `cd.hanzo.ai` is the CD
dashboard's own host, so it migrates last, after every other site is proven on the
new path. End state: `static-sites.yaml` holds ZERO first-party sites, one host-
router, one S3 layout, one store — sites are Projects, sourced from `hanzo-apps`.
