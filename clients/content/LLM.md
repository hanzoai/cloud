# Hanzo Content — the agentic-marketing loop

`clients/content` is the native Go marketing content loop — the ONE replacement for
the bespoke karma Python scripts (studio→S3→library.json→karma-queue→sync-*→karma.style).
It is a framework app-lane (like `cms`/`knowledge`): a framework MODULE (DocType
fixtures + a lifecycle hook) PLUS a thin control-plane subsystem. It opens NO store of
its own — content IS framework documents; content is a stateless orchestrator.

Multi-tenant for ANY brand with zero brand-specific code: org = tenant (validated IAM
owner), `project` field = brand/site sub-scope.

## The pieces

| File | Concern |
|------|---------|
| `lifecycle.go` | the ONE state machine (states + legal edges), a pure value |
| `doctypes.go`  | module "marketing": `Campaign`, `SocialPost`, `Asset` fixtures |
| `hooks.go`     | before_save gate enforcing status-edge legality on EVERY write |
| `content.go`   | Mount + `/v1/content/*` handlers + exported `Transition` |
| `generate.go`  | `Generator` seam (zen5 copy + studio assets) + `Generate` write path |
| `publish.go`   | `Distributor` seam (hanzoai/social) + `Publish` |
| `channels.go`  | the REAL `Distributor` over hanzoai/social's Public API |
| `storefront.go`| `Storefront` seam (Hanzo Commerce) + `StorefrontPublish`: a published catalog `Asset` → the storefront product image |

## Lifecycle (one state machine, one place)

```
draft → in_review → approved → queued → published        (+ archived off any; reopen → draft)
```

- `lifecycle.go` owns the rule (`transitions`, `CanTransition`). Two consumers read it:
  the before_save hook (enforces edges at the storage boundary — even a raw
  `PUT /v1/framework/:doctype` cannot make an illegal jump) and the `/v1/content`
  transition endpoint (same check + the distribution side effect). Side effects live
  ONLY in the endpoint, never the hook.
- `queued`/`published` trigger a channel fan-out (`entersDistribution`). The site is
  PULL: `published` == site-visible, so the site reads live docs directly (no push).

## Surface (`/v1/content/*`, org-scoped via `principal.Org`)

```
GET  /v1/content/lifecycle                     states + legal edges (console board columns)
GET  /v1/content/board?status=&project=&doctype=&limit=   cross-DocType queue board
POST /v1/content/generate    {doctype,brief,...}          draft via zen5+studio → 201
POST /v1/content/publish     {doctype,name,scheduleAt}    distribute to channels
GET  /v1/content/channels                                 a brand's connected channels
POST /v1/content/:doctype/:name/transition {to,scheduleAt}  lifecycle move (+ distribution)
```

CRUD/tenancy/permissions/install are the framework's generic surface — content adds NO
parallel CRUD. Raw content reads/writes go to `/v1/framework/<DocType>`; install the
lane per-org with `POST /v1/framework/modules/marketing/install`.

## In-process seam (the exported ops)

`Generate`/`Publish`/`Transition` are the ONE implementation. The HTTP handlers AND the
automations connector (`clients/automations/connector_content.go`) both call them, so a
human console, an `/v1/automations` flow, an MCP `tools/call`, and a headless hanzo-bot
drive the same code. They read/write the CMS through `framework.Ingest/Get/Search/
UpdateData/Installed` (validation + lifecycle hooks run through those).

## Autonomous loop

The `content` automations connector exposes `content_generate`/`content_transition`/
`content_publish` as flow steps AND MCP tools (`POST /v1/automations/mcp`). It calls the
ops IN-PROCESS (org-scoped by `rc.Org`) — NOT `core.http_request`, whose SSRF guard
blocks internal `/v1/*`. Canonical flow (cron polling trigger):

```
content_generate → core.wait_for_approval → content_transition → content_publish
```

## Edges (wired in `Mount`, follow-ups)

- `Generator`: zen5 copy via `deps.AI.ChatCompletion` (metered `b.Bill.Gate/MeterUsage`)
  + studio assets via the ComfyUI `POST /prompt` Qwen-Image-Edit-2511 graph.
- `Distributor`: hanzoai/social Public API — `GET /public/v1/integrations` (channels),
  `POST /public/v1/posts` (`type:now|schedule`), key custodied in KMS.

Until wired, both are the fail-closed default: generate/publish/channels return 503,
and a transition into distribution records `distribution:{"status":"not_configured"}`
WITHOUT failing the status change. Never a 5xx from a foreseeable condition.

### HARD GATE — must land BEFORE any brand connects a social key

Red review left exactly ONE open item, on the `Distributor` key-connect path. It is a
BLOCKING prerequisite for connecting the first real per-brand social key. Exposure
TODAY is ZERO: no key ⇒ `Distributor` is `errNotConfigured` ⇒ no fan-out ⇒ the window
below cannot open. It arms the instant a brand connects a KMS social key, so the fix
MUST ship in that same change.

- **The window:** `Publish` records `external_ids` (the per-channel skip-set) only AFTER
  the whole fan-out completes (`publish.go`), and the per-item lease is a crash net, not
  a fence — an expired-but-unreleased holder IS preempted (framework `acquireLock` steals
  on `expires_at<=now`; pinned by `TestRedLease_ExpiredLiveHolderIsPreempted`). So if one
  item's fan-out runs LONGER than `publishLeaseTTL` (5m), a contender steals the lease,
  re-reads a still-empty skip-set, and re-posts the whole set → double-post. It takes a
  brand with ~15+ slow channels each stalling to `distributeTimeout` (20s):
  `(N+1)×20s > 5m ⇒ N ≳ 14`.
- **Fix (one of, in priority order):** (1) lease heartbeat/renew — keep the holder's lease
  alive while the fan-out is live so a live holder is never preempted (PREFERRED; turns the
  lease into a real fence); (2) record `external_ids` INCREMENTALLY per channel so a stolen
  lease sees the already-posted set and skips it; (3) bound the fan-out to strictly less
  than `publishLeaseTTL`. Land one of these in the same change that connects the first
  real social key — do not connect a key without it.

## karma migration

karma.style swaps its data sources onto this CMS (markup unchanged): `journal.json` →
`GET /v1/framework/Post?filters=[["status","in",["published"]]]`; products stay Hanzo
Commerce (`design` == the product `slug` is the join key). library.json/karma-queue/
sync-* are subsumed: studio render → `Asset`, blog → `Post`, social → `SocialPost`,
campaign → `Campaign`; the lifecycle replaces karma-queue. Phase 2 widens cms
`Post`/`Page`/`Article` `status` to this shared lifecycle so ONE state machine governs
all publishable content.

### the product image: the Storefront edge (`storefront.go`)

The OLD image path was a BUILD-TIME batch: studio → S3 → `library.json` → `sync-*` →
site build (`img/<slug>/<role>.webp`). That whole pipeline is replaced by ONE edge on
the publish transition. karma.style already reads its product images at RUNTIME from
Hanzo Commerce (`site/commerce.js`: `GET /v1/store/:store/listing` → `headerImage.url`),
so the seam is: when a catalog `Asset` (kind ∈ {ecom,product,lifestyle}, non-empty
`design`) transitions to `published`, `StorefrontPublish` upserts the org's commerce
store **Listing** keyed by `design` (== product slug) so its `headerImage` points at the
asset's **S3 URL** (`file` object key → `CONTENT_ASSET_PUBLIC_BASE`, default
`https://s3.hanzo.ai/hanzo-studio`). No copy, no re-host, no build step — the Asset stays
the origin, the commerce Listing is the runtime display layer karma already reads.

- ONE path, decomplected exactly like the social `Distributor`: `Storefront` is the edge
  (Hanzo Commerce), `StorefrontPublish` is the stable orchestration fired as a
  best-effort side effect of the `published` edge in `Transition` (never rolls back the
  status, never 5xxes). Non-catalog / non-`Asset` items are skipped (nil result).
- Tenant-scoped: the S2S call is `Authorization: Bearer <COMMERCE_SERVICE_TOKEN>` +
  `X-Org-Id=<org>` over the co-resident/standalone commerce transport
  (`clients/commerce/transport`) — the SAME admin S2S pattern billing/account use; commerce
  trusts `X-Org-Id` ONLY behind the service token, so every write stays on the caller's
  own store (`GET /v1/store/current` resolves it).
- Fail-closed: no `COMMERCE_SERVICE_TOKEN` (or no store yet) ⇒ `not_configured`, recorded
  on the transition result WITHOUT failing the publish. Env: `COMMERCE_SERVICE_TOKEN`,
  `CLOUD_COMMERCE_HTTP_URL` (blank ⇒ in-process when co-resident), `CONTENT_ASSET_PUBLIC_BASE`.
- Scope note: sets the single `headerImage` (what karma renders today), last catalog
  publish wins. A role→gallery `Media` fan-out (front→header, back/life→gallery) is a
  clean follow-up once the storefront renders a gallery.
