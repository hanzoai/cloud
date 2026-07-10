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

## karma migration

karma.style swaps two data URLs onto this CMS (markup unchanged): `journal.json` →
`GET /v1/framework/Post?filters=[["status","in",["published"]]]`; `products.json` stays
Hanzo Commerce (Product is the join key). library.json/karma-queue/sync-* are subsumed:
studio render → `Asset`, blog → `Post`, social → `SocialPost`, campaign → `Campaign`;
the lifecycle replaces karma-queue. Phase 2 widens cms `Post`/`Page`/`Article` `status`
to this shared lifecycle so ONE state machine governs all publishable content.
