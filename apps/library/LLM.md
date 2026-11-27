# LLM.md — apps/library

Guidance for apps/library. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The Library and Scheduled: what a person made, and what runs without them

**The Library is `apps/library` at `/v1/library`, the one asset store.** Chat,
Dev, Studio and workflows file what a person made through it and nowhere else.
Rows live in one table, `library.items`, on hanzo-sql (`locus.Tenants`:
orm/tenant, RLS forced on `org_id`, a login provisioned once at boot with its
password derived from the data-plane master). Bytes live in one platform bucket,
`hanzo-library` (`LIBRARY_BUCKET`), under `<orghash>/<ownerhash>/<id>/<name>`.
Org buckets are deliberately not used: `/v1/s3` serves an org bucket to every
member, which would bypass the owner's choice not to share. Reads hand out URLs
signed for an hour after the visibility check. A URL opened in place serves
markup, SVG and script as `text/plain`.

- An item is its org's and its owner's (`principal.SelfFrom`). Another member
  reads it only once it is shared, and changes it never. Another org gets 404 by
  id. A row with no owner is nobody's: writes refuse a call that names nobody,
  and `GET /v1/library/unowned` is the SuperAdmin's read-only, audited view.
- Siblings write over the plane as the person they act for:
  `zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org, User})` →
  `client/library.LibraryPut` (≤10 MiB), or `LibraryReserve` + PUT +
  `LibraryCommit` (≤2 GiB). `Key` makes a write idempotent per
  (owner, source, key).
- `CLOUD_SQL_ADMIN_DSN` is the hanzo-sql superuser URL from secret `postgres`
  key `url` (SQLAlchemy spelling; `adminURL` reads every spelling of it). Key
  `url-go` held a placeholder: a plugin started on it exits at boot with "not a
  postgres URL", and the host answers 503 "no instance running".
- Writers today: Studio (its outputs), Chat (@hanzo/app `src/keep.ts`: blocks an
  answer writes, files on request), Dev (`apps/coding/library.go`: the run's
  patch and its deliverables, never edited source), automations (each run's
  answer as a document, `apps/auto/agent.go keep`).

**Scheduled is `apps/auto`, the one scheduler.** Schedules are automations on
the one durable engine. An automation records the product it was made in
(`source`), and `once` runs one time (at a local moment, or now) and then
switches itself off. `GET /v1/auto/jobs` lists every run of yours across
products: automation and workflow runs, plus background runs a product records
with the plane op `auto_job_track` (Studio renders), all in five states
(scheduled, running, succeeded, failed, cancelled). `GET /v1/auto/events` is the
same rows as SSE, polled from the store every 2 s, with a stream held 15 min.
`POST /v1/auto/cron` reads words by rule and falls back to `whenModel` (zen5).

**Every flow, automation, run and job is its person's.** `held`/`heldRun`
(job.go) answer anyone else 404, org admins included, because an org admin
governs the org and not its members' work. The builder records who made a flow.
A flow recorded for nobody, including legacy builder flows and drafts, is
listed to nobody.
