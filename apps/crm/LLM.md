# LLM.md — apps/crm

Guidance for apps/crm. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The public intake (`POST /v1/marketing/leads`, apps/crm)

One anonymous door for every form on the brand's site, filed into the brand org's Base
(apps/crm, below). The kind decides the records and what follows them:

| intake | records | call offered | code mailed | ad platforms | mailbox |
|---|---|---|---|---|---|
| sales (contact-sales, no `program`) | person + form | `intro_call` minted (`crm.IntroCall`) | no | `generate_lead` | `brand.Sales` |
| program (`program`: beta, research, partner) | person + form | no | once per person (offer.go, `crm.Offered`) | `generate_lead` | `brand.Sales` |
| careers (`job`: the opening's id) | candidate + form | no | no | nothing | `brand.Careers` |

A candidate carries the opening (`role`, `job`, `department`), `location`, the resume as
its `Resume` link and the other `links` (https only, five at most), `background`,
`answers`, `applied`, `stage` (new → hired | rejected) and `consent`. The careers form asks
one optional, unticked question, "Keep me posted about roles and news at Hanzo.
Unsubscribe any time." (`consent: true`): `given` makes the applicant a person on the
brand's "Careers news" list (`lists`, `list_members`); unticked is `declined`, and they
are mailed nothing but the reply. A resume file goes up before the form: `POST
/v1/marketing/leads/resume {name,type,size}` (public, ten an hour per caller, PDF, DOC or
DOCX up to 10 MB) signs a POST form for one object in the brand org's private `careers`
bucket (plane op `s3_upload_form`, apps/s3 plane.go: fixed key, type and size) and answers
its key; the application names it as `resume_file`, and the candidate's Resume link is the
org's own signed download of it (`/v1/s3/buckets/careers/objects/<key>`), useless to
anyone outside the org.
Every field the intake writes reaches its record (`TestEveryIntakeFieldLands`, against a
real base app). The console reads them at crm.hanzo.ai (hanzoai/console `CrmModule`).

### Hanzo CRM is a beta: the `crm` enablement is the one predicate

crm.hanzo.ai is a product any Hanzo account signs in to (IAM app `hanzo-crm`), working
in its own org's CRM. Whether the org has it is the entitlement store's `(org, crm)`
row, and nothing else: an org is given the CRM's collections in its Base only with it
(apps/entitlement `install`), and crm.hanzo.ai opens only for it (`BetaGate`). `crm` is in
`entitlement.Beta`: no org admin switches it on, whatever the plan (`post` answers 403
naming hanzo.ai/beta); it arrives three ways, all on the audit trail:

- **Applying to the beta while signed in.** `POST /v1/marketing/leads` with
  `program: beta` and a signed person (`principal.Signed`) calls `join`, which asks the
  plane op `entitlement_grant` as that person and org (`plane.For`). The grant takes only
  a product in `Beta`, for the caller's own org, records `entitlement.grant` first, and is
  idempotent. A held org (`authz.IsHeldOrg`: hanzo, lux, the shared sign-up orgs) is
  never granted by an applicant; its people get the beta in an org of their own.
- **A SuperAdmin** with `POST /v1/entitlement/orgs/:org {add|remove: ["crm"]}` (admin.hanzo.ai,
  the org's page); the boundary records every SuperAdmin request.
- **Early access on the org's plan.** `held` (holds_rpc.go), which both readers ask —
  `holds` (the plane op `elective` and `listModules` call) and the org's own
  `GET /v1/entitlement/orgs/:org` (the console's `BetaGate`) — gives an org with no row a
  product in `Beta` on that first look when a live plan of the org carries early access
  (@hanzo/plans `licensing.early_access`, every Max rung): the module installed, then
  `entitlement.grant` with reason "the org's plan carries early access", then the row.
  The fact is read off commerce's plan row (`Licensing.EarlyAccess`) through
  `CheckEntitlement`'s `EarlyAccess`, which is the org's across all its live
  subscriptions, whichever product was asked. A held org is never given it so (one
  person's plan in a shared org is not the org's); a SuperAdmin reading another org's
  list sees it as stored; a plan that cannot be read is an error, and the readers refuse
  on it. The row then answers alone, so commerce is asked only while an org has none, and
  it stays if the org leaves Max, as an applicant's does.

The other beta gates are a different predicate and early access does not open them: the
platform flag `beta` (apps/flags `access.go`, SuperAdmin-targeted rules; it shows beta
catalog items, which is how a model in beta is offered) and a capability's `Stage`
(alpha rows are held by the per-capability flag). "Early access to new models" on the
Max card is therefore delivered for products in `Beta` today, not for models.

Org hanzo holds `crm`, so Hanzo's own leads and candidates are its members', read by IAM
role like any org's (a plain member of a held org acts in none of it; its owners and admins
do).

### The CRM's records are the org's Base (apps/crm base.go)

The CRM keeps its records in the org's own Base (hanzoai/base module `crm`), written
over the plane (`base_import`, `base_upsert`, `base_get`, `base_list`) so the base app
stays the one writer. One shape, whoever files it (`apps/crm/base.go`):

| filed | records |
|---|---|
| a lead (sales, beta, research, partner) | a person (`people`, id from the address, lifecycle `lead`) and a form (`activities`, kind `form`, `source_id` = the `lead_…` reference, `data` = every field and answer as filed, `data.kind`) |
| a job application | a candidate (`candidates`, id from the `cand_…` reference, consent `unasked`, resume as the `Resume` link) and its form (`data.kind` = `careers`) |

Ids come from what a record is, so writing the same form twice is one record; a person
the CRM holds is only given values for empty fields (a later form never undoes the team's
work), and every other record is created when missing and otherwise left as it is.

- **Intake** (`apps/marketing/leads.go`): each form calls `crm.Install` (a no-op once the
  brand's Base holds the module; 503 when it cannot), mints the reference (and a sales
  inquiry's `intro_call`) itself, and files through `crm.FileLead` / `crm.FileCandidate`.
  A program code is given once per person: `crm.Offered` reads their earlier forms.
- **Install on grant** (`apps/entitlement` `install`): a product in `Beta` is given its
  Base module (`modules`, `crm` → module `crm`) before it is turned on, on a grant and on a
  SuperAdmin add, acting for the target org; an install that fails refuses the grant.

Tests write to a real base app over the plane (`internal/basetest.Serve`, which serves
`base.PlaneOps` on a peer of the test's own), so every record meets module crm's fields.

### A person who paid is a customer (apps/crm customer.go, apps/commerce emit.go)

A live card payment that settles with a signed-in payer (`payment.email`, the verified address
the request carried) files that payer in the CRM of the org it was paid in (`p.ledger`):
`crm.Customer` sets the person's lifecycle to `customer`, and their type to `customer` unless the
team gave them another (an investor who pays stays an investor), creates the person when the
org's CRM holds none, and links the commerce user (the wallet key the payment was made from, the
`userId` of `GET /v1/billing/subscriptions`) once as a `system` activity on channel `commerce`,
`source_id` `customer:<user>`. The Customers view lists the people whose type is customer; a
person's record reads what they bought, left in a cart and pay for, live, by their address: their orders
and carts from the commerce capability (`GET /v1/commerce/order?email=`, `GET /v1/commerce/cart?email=`,
apps/commerce order.go: an org admin's or SuperAdmin's read of the org's own store, the count and revenue over
every match) and, by the linked user, their plans from billing (`GET /v1/billing/subscriptions?userId=`).
The Sales page reads the org's: customers, orders and revenue, carts left, plans and what they contribute a
month. Only the org paid is written, no CRM is ever installed by a payment (an org without one is an
error, logged and dropped), no money is read or moved and no cap is written. It runs beside the
sale's statement on the event plane (detached, bounded, dropped under pressure), so the payer's
next payment files them. A sandbox payment files nobody. A renewal and an auto-reload (a sweep,
nobody present) name only the billing account, so `customers` (apps/commerce customer.go) reads
IAM's directory once per sweep, as the platform, and files each member by their verified address
(`<org>/<name>`; an org's own plan, an unverified address and a service account file nobody); a
wire files the address its invoice was issued to. Subscribers who paid before any of this are filed
by `POST /v1/commerce/customer/backfill`, the platform SuperAdmin's alone and on the audit trail
(`admin.customers.backfill`) before it runs: the fleet's subscriptions (the board's read) crossed with
the directory, every person who has paid outside a sandbox filed as above. It is idempotent, counts
who it skipped (no verified address, or an org with no CRM) and the orgs it ran out of time before
(`unread`: run it again), and is run once after the release that carries it.
Tests: `customer_test.go`, `apps/commerce/emit_test.go`, `apps/commerce/customer_test.go`.

### Where outreach to a person stands (apps/crm mail.go `reach`)

| filed | person |
|---|---|
| a send (`sent`) | `outreach` sent, `contacted` the send's time if later |
| delivered | stays sent |
| opened, clicked, bounced | that word |
| complained | `unsubscribed` |
| suppressed, failed | nothing |

Forward only, by rank (sent < opened < clicked < replied < bounced = unsubscribed):
a late open never undoes a click, a reply stays, and an older send never makes the
last contact older. Only a send is a contact; a report is not. `replied` is set by a
person in the CRM until inbound mail is filed. A CRM installed before the fields has
none, and `reach` leaves it alone: it grows on the next `Install` (an intake, a
grant) or an org admin's `POST /v1/base/modules/crm` (the CRM's "Update the CRM").
Test: `TestOutreachFollowsTheMail`.

### Writing to a person from the CRM (`POST /v1/marketing/mail`, apps/marketing mail.go)

One email to one address, from a person's CRM record: `{to, subject, body, person?}`,
answering `{sent, at}`. An admin of the acting org or SuperAdmin (`steward`) sends; a
member is refused. It leaves by the org's own path, the one every marketing send takes
(`deliver`: the org's mailbox when it has one, else notify), never to an address that
opted out (`sent:false`, not an error), and inside the mailbox's caps (429 with the time
it frees). It is on the audit trail before it leaves (`marketing.mail.send`: address,
subject and person, never the text; no trail is 503), and with `person` the sent mail is
an outbound `email` activity on their timeline (`crm.FileMail`). Test: `mail_test.go`.

### The invoice names the brand that bills

`GET /v1/billing/invoices/:id/pdf` hands commerce the caller's brand (`biller`: the
brand that signed their token, else `cloud.Brand()`) as `InvoiceRef.brand`, and the
PDF carries that brand's mark, name and address (commerce `api/billing/pdf.go`).

## The investor intake (`apps/marketing/investor.go`)

`POST /v1/marketing/leads` with `program: "investor"` files an investor on the one lead
route: the person (full address, phone) and a form of kind `investor`, subject
"Investor", `data.status` `submitted`, in the brand's Base. It mints no code, states no
`generate_lead` (an ad event on a private round is a solicitation question) and mails no
sales notice. Every answer is refused 400 rather than clipped, because it lands on a SAFE;
`amount` is whole US dollars, at least 25000 ("The minimum investment is $25,000.").

The SAFE is made in Hanzo eSign (`ESIGN_URL`, default https://esign.hanzo.ai) from the
template `ESIGN_INVESTOR_TEMPLATE_ID` (a plain universe value):
`template/get-template-by-id`, then `template/create-document-from-template` with
`distributeDocument`, the investor at signing order 1 and z@hanzo.ai at 2, and the
template's TEXT fields filled by `fieldMeta.label` (Investor, Name, Title, Purchase Amount,
Date of Safe, Address, Email; the company's terms are never sent). Cloud calls eSign AS
ITSELF: its own IAM client_credentials token (`IAM_CLIENT_ID`/`IAM_CLIENT_SECRET`, the pair
KMS syncs), minted for eSign's application `<brand>-sign` (`cloud.MachineFor`, RFC 8707
`resource`); eSign verifies it against hanzo.id's JWKS and maps `hanzo-cloud` to the team
that holds the template (its `IAM_APP_TEAMS`). No template id or no identity is the switch
off: the form is filed and the receipt says
`sent: false`. The form records `data.esign` `{documentId, envelopeId, url}`.

eSign's webhook is `POST /v1/marketing/leads/esign`, posted by eSign as ITS OWN IAM
application (a token minted for `https://api.hanzo.ai`), which the identity boundary
verifies like any other: no caller 401, any caller but `app:admin/<brand>-sign` 403. No
secret is shared either way. It moves the
form whose `source_id` is the document's `externalId`, and only when the document is the
one the form recorded, forward only: investor signed (order 1 SIGNED), countersigned
(order 2 SIGNED), completed; or declined, or voided. `crm.Amend` serialises the
read-and-write, since the last signature and the completion arrive together.

## The beta waitlist (`apps/marketing/beta.go`, `apps/crm/beta.go`)

One record per signed-in account, in the brand's Base: an activity of kind `form`, channel
`beta`, `source_id` `beta:<IAM sub>`, on the person the account's verified address is,
subject "Hanzo beta". It is written whole on every change; `created` is when the account
joined the list. `data`: `user` (principal.Subject), `org` (the acting org), `email`,
`program`, `applied` (RFC 3339, first time a program was set), `answers`
(`[{id, question, answer: []string}]`), `betas` (ids), `requested` (`{id: RFC 3339}`, stamped
server-side when an id joins `betas`, dropped when it leaves), `github` (lowercase login),
`points`, `actions` (`[{action: star|follow, target, points, at}]`), `lead` (the
`lead_…` ref the application was filed under by `POST /v1/marketing/leads`).

- `GET /v1/marketing/beta` the caller's record with `place` and `of`; `PUT` sets program,
  answers, betas and lead (a field left out is kept, betas replace the set); `POST
  /beta/request {betas}` adds; `POST /beta/actions {action, target, github}` counts one.
  All need a signed-in person (`principal.Signed`) and 401 without; writes are 60 an hour
  per account, star checks 20 an hour per account and 300 per deployment, since each spends
  up to three calls of the GitHub App installation's hourly budget the runner reconcile
  shares.
- Points are the server's only, and what each earns is `client/points.go` (see "The public
  standing" below): a star on `hanzoai/*` is 1, VERIFIED by reading the
  login's stars newest first (`GET /users/{login}/starred?sort=created`, three pages of
  100) as the deployment's GitHub App installed on hanzoai (`GITHUB_APP_ID`,
  `GITHUB_APP_PRIVATE_KEY`; anonymous when GitHub refuses it). GitHub has no
  per-user "is starred" route: `/users/{u}/starred/{o}/{r}` answers 404 even for a
  stargazer. A login earns for one account (409 otherwise) and stays the record's once a
  star is counted. A follow of a founder's link (`antje|zach` / `github|linkedin|x`) is 1,
  on trust. Applying and asking for betas earn nothing. A `bug_report` or `bounty` action a
  person files on their own record is stored and shown, and scores nothing on any board: it is
  not verified; the verified ones are the bounty awards the leaderboard keeps.
- Place: one more than records with more points, or as many and an earlier `created`
  (`data.points > N` filters through Base's JSON path). `beta_requested`,
  `beta_starred` and `beta_followed` are stated on the brand's event plane with the IAM
  sub as subject and `user`/`org` facts.
- **Who is in the beta and who asked** (`apps/marketing/members.go`): `GET
  /v1/admin/marketing/beta`, a SuperAdmin's read (`principal.SuperFrom`, 403 otherwise; the read is
  on the audit trail first as `marketing.beta.members`, no trail is 503), answers every waitlist record
  (`crm.Waiting`) as `{person, email, user, program, applied, betas: [{id, name, requested,
  enabled}]}`. `enabled` is whether the platform switch `beta.<id>` names the record's IAM id in its
  `user` rule (the switchboard's `users`, apps/flags `named`); a beta is listed when the record asked
  for it or holds it. The org's `crm` entitlement is a different source and is not read. The CRM's
  Beta page (@hanzo/crm, `/crm/beta`) draws it. Test: `members_test.go`.
- Base's own `plugins/waitlist` (`/v1/waitlist`) is a different thing: an anonymous,
  email-keyed list with turnstile and ref codes, holding no IAM subject and no application.

## The newsletter, by double opt-in (`apps/marketing/newsletter.go`, `apps/crm/newsletter.go`)

Two public ops with no principal; the brand is the server's (`cloud.Brand()`), as on the
lead form, and an X-Org-Id or bearer on the request names nothing. Both are `spec.Open`.

- `POST /v1/marketing/subscribe {email, source?, locale?}` answers `202 {"status":"pending"}`
  and records nothing. It mails `<brand Domain>/newsletter/confirm#t=<token>`: the token
  rides the fragment, so it reaches no access log and no Referer, and the site's page posts
  it only after a press, so a mail scanner confirms nothing. The answer and the work are the
  same for a new, a subscribed and an unsubscribed address: the op reads no subscription.
  Refusals, in order: 503 (brand unregistered or without a site domain, the link key
  unreadable, or the brand's Base cannot hold the CRM), 429 (10 an hour per caller IP,
  window `marketing:newsletter`), 413 (body over 4 KiB), 400 (no address, over 320, not one
  address), 429 (1000 mails an hour per deployment `marketing:newsletter-mail`, then 3 a day
  per address `marketing:newsletter-address`, counted under the address's SHA-256). No sales
  mail, no `generate_lead`.
- **The confirmation mail is transactional.** The person asked for it and it offers nothing,
  so it does not pass `deliver`: an address on the brand's opt-out list is mailed, which is
  the only way a person who left comes back. The PLATFORM list (admin org) is honoured: no
  mail, same answer. It leaves by the notify plane op (`tellFn`, `notify_send`) as the brand
  org, HTML with its plain-text alternative, detached; the credential is the brand org's
  notify email provider at KMS `orgs/<brand>/notify/<svc>/<key>`, the one the sales notices
  already use. It is signed with the brand's name and `brand.Info.Postal` (hanzo: the
  address hanzo.ai/legal and commerce's invoice give).
- **The token is stateless**: HMAC-SHA256, 16-byte prefix, under KMS
  `marketing/newsletter-hmac` (sealed on the first subscribe, like the unsubscribe and click
  keys), over version, issued, an 8-byte nonce, org, address, source, locale; strict
  base64url; honoured seven days.
- `POST /v1/marketing/subscribe/confirm {token}` answers `200 {"status":"confirmed"}`; 429
  past 30 presses an hour per IP, 400 for a token missing, malformed, altered, signed under
  another key or for another brand (or when no key was ever sealed), 410 for a genuine one
  past seven days, 503 when the CRM cannot be held. The FIRST press lifts the brand org's
  opt-out for the address (`Unsuppress`), then files `crm.FileSubscriber`: the person
  (fill-empty), the "Newsletter" list and its member (`consent_at`), and last a form
  (channel web, `data.kind` newsletter, subject "Newsletter subscription", `source_id`
  `newsletter_<MAC hex>`) holding email, source, locale, `requested_at`, `confirmed_at`,
  `confirmed_ip` and `consent: "double opt-in"`. A later press of the same link answers 200
  and changes nothing, so an old link never undoes a later unsubscribe. A company's domain
  opt-out and the platform list are not the person's to lift, and `deliver` still refuses
  them.
- Nothing sends the newsletter yet. A send is a sequence enrolled with the list's people,
  through `deliver`. Commercial mail (`email.Footer`) states no postal address today.
- Tests: `apps/marketing/newsletter_test.go`, against a real base app and a real KMS.
