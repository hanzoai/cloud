# `/v1/projects` — Shared Projects API (contract)

The ONE org-scoped store of buildable/deployable sites. The SAME records are
read/written by **hanzo.app** (the builder) and **console.hanzo.ai** (the
Projects module). Both call this surface through the gateway; there is no second
copy of project state.

Owner: `hanzoai/cloud` → `clients/projectsvc`. Mounted into the unified cloud
binary (HIP-0106), reachable at `https://api.hanzo.ai/v1/projects`.

## Auth & tenancy (HIP-0111)

- Authenticate via the `@hanzo/iam` SDK; send the IAM bearer token to
  `api.hanzo.ai`. The **gateway** validates the JWT and injects `X-Org-Id`,
  `X-User-Id`, `X-User-Email` (and strips any client-supplied copies).
- Every route is **org-scoped**: the tenant is the gateway-minted `X-Org-Id`
  (the JWT `owner` claim). No org → `403` (admins fall back to an `admin`
  bucket). Two orgs can hold the same `slug`; `(org, slug)` is unique.
- Never send `X-Org-Id` from the browser — it is ignored/stripped at the edge.

## Resources

### Project

```ts
type Project = {
  id: string;                    // "proj_<token>"
  org: string;                   // tenant slug (from JWT)
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
  prefix?: string;               // "<org>/<slug>"
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
| `POST` | `/v1/projects` | `CreateProject` | `201 Project` |
| `GET` | `/v1/projects` | — | `200 Project[]` (org, newest-updated first) |
| `GET` | `/v1/projects/:slug` | — | `200 Project` / `404` |
| `PATCH` | `/v1/projects/:slug` | `UpdateProject` | `200 Project` |
| `DELETE` | `/v1/projects/:slug` | — | `204` (also purges the live S3 site) |
| `POST` | `/v1/projects/:slug/deploy` | artifact **or** `GitDeploy` | `200 Deployment` (upload) / `202 Deployment` (git) |
| `GET` | `/v1/projects/:slug/deployments` | — | `200 Deployment[]` (newest version first) |
| `GET` | `/v1/projects/:slug/deployments/:id` | — | `200 Deployment` / `404` |
| `POST` | `/v1/projects/:slug/deployments/:id/complete` | `Complete` | `200 Deployment` (CI hook, git path) |

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

type GitDeploy = { source: "git"; commit?: string; branch?: string };  // Content-Type: application/json
type Complete  = { status: "live" | "error"; commit?: string; liveUrl?: string; message?: string; files?: number; bytes?: number };
```

Errors are JSON `{ "error": string, "code": number }` with the matching HTTP
status (`400` validation, `403` no org, `404` not found, `409` slug taken,
`502/503` deploy/storage failures).

## Deploy pipeline (two modes, one endpoint)

1. **Artifact (builder one-click).** `POST /v1/projects/:slug/deploy` with the
   request body set to a **tar** or **tar.gz** of the BUILT site (must contain
   `index.html` at the root; bounded by the gateway body limit). The site is
   unpacked to OUR S3 at `s3://<bucket>/<org>/<slug>/`, the bucket is marked
   public-read, and the deployment lands `live` with a `liveUrl`. Synchronous.

2. **Git (CI, never local).** `POST /v1/projects/:slug/deploy` with
   `Content-Type: application/json` and `{"source":"git"}`. Returns `202` with a
   `queued` deployment. CI (the reusable build workflow) checks out the linked
   repo, builds it, syncs `dist/` to the SAME prefix, then calls
   `/v1/projects/:slug/deployments/:id/complete` to flip it `live`. For sites
   too large to stream through the API.

`liveUrl` is `https://s3.hanzo.ai/<bucket>/<org>/<slug>/index.html` by default,
or `https://<sites-host>/<org>/<slug>/` when the `hanzoai/static` container
(the static-app image) is configured to serve the bucket behind the gateway.
GitHub export is an optional, separate step — going live never requires it.

## Console module notes

- List view → `GET /v1/projects`. Status badge from `status`; "Open" links to
  `https://hanzo.app/build/<slug>`; "Visit" links to `liveUrl`.
- Detail view → `GET /v1/projects/:slug` + `GET /v1/projects/:slug/deployments`
  for the deploy history/timeline.
- Create/rename/link-repo → `POST`/`PATCH`. Deploy/redeploy buttons can call the
  deploy endpoint directly (git mode) for repo-linked projects.
- Do not cache across orgs; the org is implicit in the token. Switching org in
  the console re-fetches with the new token (new `X-Org-Id`).
