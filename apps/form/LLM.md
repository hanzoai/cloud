# LLM.md — apps/form

Guidance for apps/form. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Forms (`apps/form`, `/v1/form`; the module is github.com/hanzoai/form)

The service is hanzoai/form, composed like hanzoai/iam: `app.Use(forms.New(deps))`.
apps/form is only what cloud knows: each org's Base over the plane (base_import,
upsert, get, list and base_delete, added for it), the caller from the identity
boundary (org admin = `cloud.Admin`; a signed-in respondent = `principal.Signed`
plus the validated X-User-Email), the rate window, the platform settings (fleet
config product `form`, SuperAdmin ops GET/PUT `/v1/admin/form/settings`), the
event plane (form_viewed/started/submitted, product `form`, `person_id` never an
address), the CRM (`crm.FileForm`: activity kind form, source_id `form:<response>`,
org-level when the person is unknown), notify for forward/welcome mail, and KMS
for webhook secrets at `orgs/<org>/form/WEBHOOK_<ID>@prod`. Public ids are
`<org>.<slug|id>`, resolved through `form_index` in the admin org's Base, so an
unknown org opens nothing. Seed the brand's video-feedback and enterprise-feedback
forms with `scripts/form-seed.sh` (an org-admin bearer of the brand org).

Who a respondent is: only a person presenting their own token in a header
(`cloud.Presented` + `principal.Signed`); a session cookie names nobody, since
any sibling page can send it. An address typed into a form is a CLAIM
(`Filing.Verified=false`): `crm.FileForm` files it marked "(unverified)", on the
person only when the CRM already holds them, changing nothing, joining no list.
A claim on the brand's Newsletter list is asked the double opt-in instead
(marketing plane op `marketing_subscribe`, the public subscribe's core). Rates
count by `address.Buckets` (/64 and /48), an unread address in one shared bucket.
Forward and welcome mails have separate per-form hourly budgets (settings
`limits.forward`, `limits.welcome`); a welcome asks `marketing_clearance` first.

The CRM's event feed (apps/marketing `feed.go`): a durable consumer `crm` on the
EVENT stream files allowlisted brand-org events that are VOUCHED (the org's
secret key) or plane-sourced (a peer app) on the person an `email` or a held
`person_id` attribute names (`crm.FileEvent`, source_id `event:<id>`). A browser's
event, on the publishable key anyone can read out of the site, is dropped before
it counts against the rate. The allowlist and the rate (default 600 a minute) are
fleet config product `crm` (`feed`, `rate`), edited at GET/PUT
`/v1/admin/marketing/feed`; a kept attribute may not be one the event plane never
stores (`event.Sensitive`). The six shipped events are all stated by the site's
browser today, so by default the feed files NOTHING until a server states them
(vouched or over the plane).
