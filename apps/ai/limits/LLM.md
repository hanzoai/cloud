# LLM.md — apps/ai/limits

Guidance for apps/ai/limits. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The usage policy decides who pays (`apps/ai/limits`, `/v1/admin/usage`)

ONE engine, asked by the ai gate (`limitAsk`) and zen (`limitGate`/`planSpend`) for
every chat request and decision. Every model is in a class — premium, ours, free —
read from ai's catalog (`controllers.ClassOf`) unless the model policy names one.
A priced request is paid, in order: the plan's included usage of its class (list
price, per seat, per billing period, optional short window), unless the model's
share of it is used → credits — prepaid, then granted credit where
`grantsPay` (Jev: own money only) — when the payer chose `creditsAfterAllowance` AND the
plan's admin `overage` allows it (a payer with no plan pays prepaid as before) → the
model's daily cap for the plan (`free` = no plan) → granted credit for a no-plan
payer → refusal, no figure in any message: `model_cap` (chat: answered by the rule's
`fallback`, default `enso`), `plan_allowance_used` (chat: the free model),
`insufficient_balance` (a payer with no plan: it owes funds, never a plan; the one code the wallet
gate also says, so a program reads the same answer from either; the hit names the `Model`),
`free_plan_cap`. A refusal carries `Credits` when the payer
holds what could pay and has not chosen to; ai turns that into the
`{"kind":"credits","label":"Continue with credits"}` action, else `topup`. A
free-class request from a plan holder counts in the plan's session/day windows
(limited mode cannot be farmed); with no plan the free allowance (`apps/allowance`)
bounds it. `Cash` on a grant rides `UsageEvent.Cash` → `metering.Usage.Cash` →
`money.UsageInput.Cash` → finance never draws granted credit (cash and the line pay).
**Our orgs pay like customers (commerce v1.50.147+).** No org holds a tier by
name: admin, hanzo, lux, zoo, adnexus, bootnode, osage and pars resolve like any
customer, with the tier their recorded subscriptions confer and the balance their
ledger holds, and no X-Tier or ?tier= is honoured for anyone. A plan the owner gives
one of them is a comp, recorded by a SuperAdmin like any plan paid outside checkout:
`POST /v1/admin/customers/<org>/plan` with `"processor":"comp"`, `"priceCents":0`,
the approval in `reference.order` (`comp-<org>-<yyyy-mm>`), why in `terms`, a period
of up to a year. It confers the plan's tier (settled `external:comp`), adds nothing
to MRR, never renews (the next period is a comp recorded again) and is audited as
`admin.customer.plan`. Comps of the eight orgs are the owner's to approve; until one
is recorded an org reads free. Pinned by `TestAnOrgGivenACompReadsItsTier` and
`TestOurOrgsPayLikeCustomersE2E`.

**Granted credit never pays for a premium (third-party) model.** A model with no rule
of its own takes granted credit as its class does: `ai_credit_<class>` (`flags.CreditKey`,
seeded premium false, ours true; admin.hanzo.ai `PUT /v1/admin/usage/policy/credit/:class`),
and every seeded premium rule says `grantsPay: false`. A premium model nothing else pays
for is refused `insufficient_balance` (402), whatever credit the payer holds; with cash it
is `PaysPrepaid` with `Cash`, so its debit is drawn from cash (then the line) and never
from credit. Pinned by `TestCashPaysForAThirdPartysModelAndCreditNever` (a real ledger),
`TestAModelNoRuleNamesTakesCreditAsItsClassDoes`, `TestTheHookSaysWhoPays`.
**A signed-in app's Free chat that asks a third party's model nothing pays for** is handed to
Enso at no charge as it always was, unless the SuperAdmin switch `ai_handover_premium`
(`flags.HandoverKey`, seeded on; admin.hanzo.ai flags cockpit) is off: then the app gets the 402
`insufficient_balance` a program gets, with `topup_url` (`handover` in `apps/ai/limits_gate.go`
answers hanzoai/ai's `object.HandoverFunc`; only premium has a switch, Hanzo's own classes follow
the plan). **What a picker reads to show Top up**: `GET /v1/billing/balance` `prepaid` (cash +
line, never granted credit) and `GET /v1/ai/limits` `pays` (per class — premium, ours — plan,
prepaid, credits or none, the policy's own answer; absent when the wallet cannot be read); a
model's price is `/v1/models` `pricing`, its modality `endpoint` and `outputs`. **Every charge
names its person and its key**: `debit` (`apps/ai/ai.go`) sends `Actor`, `Key`
(`<org>/<id>`), `Class`, `Units`, `PaidBy` and the call's COGS with the amount, and the usage log
(`treasury_usage.actor`, `.api_key`, added to an existing file on open) keeps them;
`finance.UsageRow` reads them back.
**Prepaid is `finance.Funds.Own`** — cash + the line left, everything but granted
credit — read by `limits.walletFunds` from the co-resident ledger or, over the plane,
from `client.Balance` back into `finance.Funds` (`balanceFunds`; an empty `line` is a
ledger that reports none). So an org with a line and no cash gets Jev and models past
their free cap; the line is the org's own account's (`finance.lined`), so a member's
wallet (`<org>/<name>`, every person in the signup org `hanzo`) draws none of it.
Pinned by `TestALinePaysForJevUntilItIsDrawn`, `TestAMembersWalletDrawsNoLine`,
`TestALineAdmitsAModelPastItsFreeCap`, `TestAWireOpensJevAndItsReversalClosesIt`.

The model policy is ONE JSON switch, `ai_model_policy`, keyed by model id or a `*`
glob (`limits.Match`: exact beats glob, longer glob beats shorter). A rule's `caps`
are per plan `{periodCents, dailyRequests}` per seat — the most of the plan's class
allowance its models take (a glob's models share one `share` row set) — and
`fallback` the Hanzo model past it. Seeds: kai and hanzo/kai (ours, grants pay,
free 50/day, priced at half Jev), `typesafe/jev*` and `~typesafe/jev*` (premium on
chat and decisions alike, real money only, free 0), and `flags.TopPremium` — the costliest tenth of
premium by list price, grouped by glob — each capped at `CapShare` (30%) of every
plan's premium allowance (max-20x: $15 of $50). The payer's choice lives in the
limits store (`credits` table, keyed org+subject): `PUT /v1/ai/limits
{"creditsAfterAllowance":bool}` — a pooled org wallet's admin, or a person for their
own wallet; audited `ai.limits.credits` with before/after, no trail no write.

The record is flags, so the gate reads what the admin page writes within a flag
TTL: per plan `ai_plan_{session,day,premium,premium_window,premium_window_hours,
ours,ours_window,ours_window_hours,overage}_<plan>` (seeds `flags.PlanSeeds`; Max
20x $50+$50), per model `ai_model_policy`. ANY slug works: the limiter holds a payer
to a plan the policy NAMES — seeded, or ALL NINE switches saved (`Limiter.named`;
`limits.Named` is the fresh read the admin gate asks) — and reads an unnamed slug as on
no plan. Every switch read goes through `Limiter.value`: a Set answer is remembered in
the limits store (table `known`), and an error or an unset answer (the flags app
degrades an outage to Set=false) reads as the last known value, so a named plan keeps
its figures through a flag outage and a restart, never free, never refused; a never-saved
slug (dns-pro) stays no plan. A switch no seed registers is absent from the flag board,
so `readPolicy` reads it from the plane. `PUT …/plans/:plan` is all-or-nothing
(`savePlan`): fixed order, `session` LAST, a failure puts back every switch it wrote
(a seeded rung's unset switch puts back its seed), attempt then outcome on the trail
(`admin.usage.plan.attempt`, `admin.usage.plan`). Slugs beginning `window-`/`hours-`
are refused (`flags.Keyed`: they would key another plan's switch). The way on
(`Hit.Upgrade`, `Standing.Upgrade`) is the cheapest ACTIVE, non-per-seat row in the
rung's category priced above it that the policy names, from commerce's
`billing_plan_rows`, held a minute; per-seat plans (team) have none. SuperAdmin edits it
at `GET /v1/admin/usage/policy` (every plan-authority row plus the seeds, each with
`named`), `PUT …/plans/:plan` (any row's slug; an unseeded slug with commerce unreadable
is 503), `PUT|DELETE …/models/:model` (`~` for `/`, exact id in `?model=`). Plan ROWS are
written only through `POST /v1/admin/plans` / `PUT /v1/admin/plans/:slug`
(apps/admin/plans.go → commerce plane `billing_plan_create|set`,
apps/commerce/authority_rpc.go): attempt then outcome on the trail
(`admin.plan.create|set[.attempt]`, 503 with no trail), credit fields kept, reload fields
(`reloadsPerDay`, `reload`) replaced only when stated, category lower-cased, a
`personal` row refused an annual total or a yearly interval, and a paid plan (price or
annual total) going active or private in any category but `dns` refused until its own
usage is saved (add draft → save usage → activate; `scripts/ladder-writes.sh` does it
with readbacks). AUTO-RELOAD IS PER PAYER (commerce ReadAutoRecharge/WriteAutoRecharge take the payer):
`GET|PUT /v1/billing/recharge` read and write the rule of the wallet the caller pays
from (`payer`/`steward`): the org account's is its admins', a signup-org person's own
wallet is theirs (`hanzo/alice`), passed over the plane as `AutoRechargeIn` /
`AutoRechargeSetIn{Subject, Edit}`, never from a body. A plan row's `limits.reload`
(threshold, amount) is switched on by every purchase of it (card, saved card, wallet
and balance when a card is on file, a trial's first paid period); the sale answers
`reload.state` armed|kept|needs_card (`client.Sale.Reload`), and `limits.reloadsPerDay`
is the payer's daily bound (0 none). The sweep reads the balance cloud's spend gate
reads and credits each reload to it, one charge per low-balance event.
Commerce's HTTP plan writes and `POST …/plans/seed` are not mounted; the boot seed
still reconciles. `perSeat` and `contactSales` are restated only when the statement
carries them (pointers on `client.PlanSet`), and `limits` carries an organization's
order in whole dollars (`spendLimits`, `spendLimit`, `balanceMonths`,
`balanceCeiling`), kept when a save omits them; `checkOrder` refuses a merged set
a checkout cannot offer. Commerce reads seat terms (per seat, `minSeats`,
`maxMembers`) off the row, so `ladder-writes.sh enterprise` (usage 0/0/0c with
overage, then the row: $240 a seat a year, 20 to 500 seats, the order, the card's
`enterprise_lines`) is what puts Enterprise on sale by card; until then its row is
contact sales and the card refuses it. The sale (`POST /v1/billing/subscribe/card`
with `balanceCents`) is one charge of seats x year + the starting balance, which
commerce credits itself, keyed on the payment (it is taken back on a refund or a
dispute); a replay is the first answer byte for byte (`saleView`); the sale states
`order_completed` with `org`, `tab` (also `item_category`), `seats`, `order_id`,
`payment_method`, `setup`, `amount_cents` and `balance_cents` (revenue excludes the
balance), and every sale or refusal is `billing.plan.sale` on the audit trail
(`saleAudit`). A money write for an org the token does not list (an `X-Org-Id` the
boundary discarded, `principal.Unopened`) is 409: subscribe/card and the lead. A
signed-in lead (`POST /v1/marketing/leads`) files under the token's address, takes
`upgrade` only from an admin acting in that org, takes `payment` card|invoice (an
Enterprise invoice request is also `invoice_requested`), and one
`X-Idempotency-Key` from one address is one lead. An organization also orders by
INVOICE and pays by wire (`POST /v1/billing/subscribe/invoice`, the card's gate less
the card; commerce `OrderInvoice`, apps/commerce `invoice_rpc.go`): a row with
`limits.invoiceDays`/`graceDays` (set in admin, not by a script) raises an OPEN
invoice, mails it (notify, HTML + text: amount, due date, reference set apart,
bank one field to a line, "Your plan starts when we confirm your payment.", the
void date, `hostedUrl` + `pdfUrl`) and opens nothing. The bank is read live from
KMS (`orgs/<brand>/wire`), never stored. `hostedUrl` is the brand's `Pay`
(`brand.Info`, Hanzo `https://hanzo.ai/pay`) + `/invoice/<link>`; a brand with
none is 503. The order needs an X-Org-Id the CALLER sent (`chose`: the boundary
writes X-Org-Id for a request that selected none and marks it
`principal.Defaulted`) and a live IAM admin (`admits`), whatever the row. Its
refusals carry a `code` (commerce `Refusal`, kept by `saleFault`):
`seats_out_of_range`, `balance_out_of_range`, `org_required`,
`not_sold_by_invoice`, `plan_held`, `order_placing`, `key_reused`,
`wire_not_configured`, and 409 `invoice_open` with `invoiceId`, `number`,
`hostedUrl` (a NEW link; every link stays valid). The plane carries a refusal's
code and sentence but not its data, so the open invoice crosses as
`InvoiceOrdered.Open` and the route refuses it. `client.InvoiceOrder` IS
commerce's `InvoiceOrder` (read through JSON in `orderView`, lines
`{description, quantity, unitCents, amountCents}`), held equal by
`TestTheInvoiceRailIsOneShape`. Anyone holding the link reads
`GET /v1/billing/invoices/hosted/:link` (and `/pdf`) as the host's brand; a link
read on another brand's host is 404; answers are `no-store`, `noindex`,
`no-referrer`, and carry no address or receipt. The link is in the PATH, so the
ingress access log and zip's request log and span (`url.path`) hold it. SuperAdmin
acts: `POST /v1/admin/customers/:org/invoices/:id/paid|void|refund`. paid keys
the wire by `core.WireKey` (commerce `WireKey` is the same rule), reads the
invoice in that org first (`billing_order_read`: no claim for an org whose books
do not hold it, or for an invoice another wire paid), then claims and settles
under the wire lock (`core.ClaimWire`: refused when another org holds the wire or
this org holds it as a standing credit grant). A grant of a wire claimed for an
invoice is refused. It is `admin.customer.invoice.paid` (with `reopened`); a
settlement answered again (`replayed`) writes no second row and mails or states
nothing. Net terms: the platform switch `billing_net_terms` names the approved
orgs. Events: `invoice_created`, `invoice_paid`, `invoice_voided` (no address, no
bank detail); `order_completed` (payment_method wire, the invoice's worth: an
overpayment is not revenue) only at paid. Mail goes as the invoice's brand. The
renewal sweep also voids invoices past due + grace (`expired`). Org creation is
`POST /v1/account/orgs`; a name it refuses says why (too long, a character that does
not show, no letters a-z or digits for its address). `GET /v1/admin/usage/margin` is the
per-plan worst-case and actual margin plus each plan's top models by share of its
class and the orgs that used it (`orgs`), from warehouse `cloud_usage` keyed on
`owner` — the ledger that paid — never `organization`, the caller's home org. Every
billed row is use; only rows with a known cost (`costed`) set the cost ratio. Usage
figures are cents to the nano-dollar (float), so sub-cent use never reads $0. A
model's class there is the policy rule's, then the family prefix. The customer reads `GET /v1/ai/limits`:
per class a percent rounded up to fives, a state, who pays next (plan|credits|none),
`credits_after_allowance`, `paused` (model or glob, fallback, resets_at), limited
reason and actions — never a figure. The free allowance counts on admission, per
person (`person(ctx)` = caller home org/user). `auto`, `zen-router` and an absent
model are routed among Hanzo classes only unless the org allowlists premium (ai
`controllers.routable`).

Every priced endpoint asks (ai `controllers.Entitled`: chat, decisions, embeddings,
rerank, speech, transcriptions, the live transcript's open, voice/music/foley,
images, videos), so a Max org at $0 prepaid is served all of them by its plan. A
live transcript's push or close is asked with `Session` (the model its open named)
and counts nothing — the open is the one request, as the rate limiters count it. A
grant that counted something carries `Release`; ai calls it when the answer is 400
or above, and `store.release` takes back the request row (and the session it alone
opened) and each daily count it took. The free allowance gives back the same way:
`object.Standing.Release` → `allowance_give` (`AllowanceIn.At`, when the unit
was taken: the window it counted in, a session that opened after it being another)
→ `coord.Unquota`, never below zero.

zen's Claim runs ahead of ai's catch-all, so a direct zen SKU request meets zen's
gate alone (a sibling's plane call is gated by the sibling). `withReply` parks a
ticket on the request: the gate counts a covered request (free lane, or a priced
SKU like zen-embedding, Admit without Session) and notes on the ticket the plan
settle and what to give back; the meter settles a covered priced charge against
the plan and debits no wallet; an answer of 400 or above (or an error, unless a
stream started) runs every give — plan release and the free allowance unit. A
plan's refusal of a priced zen SKU stands (`refused`: 429 for windows/free caps,
402 otherwise); credits past the plan go to the wallet. zen's gate writes the same
`X-Hanzo-Usage`, `-Usage-Class`, `-Paid-By` headers ai's does, and the edge's
`Access-Control-Expose-Headers` lists them with `X-Hanzo-Fallback` and
`X-Hanzo-Usage-Reason` so a browser can read them (`middleware_edge.go`).

## One per-minute limit on model calls (`apps/ai/limits/rate.go`, `apps/ai/rate.go`)

The model requests one PERSON may make in a minute, on every surface. One count,
`cloud.Window("rpm")` (the deployment's coord, one window per `client.Person`,
"<home org>/<name>", the key the free allowance counts by), charged by
`limits.Limiter.Pace` and asked at the two doors a model is reached through. Nobody
is admitted uncounted (`pacer`, apps/ai/rate.go): a caller the boundary cannot place
in an org is counted by `user:<id>`, one it cannot name by `ip:<address>`
(`geo.IP`), and the two public routes (`/v1/chat/public`,
`/v1/audio/transcriptions/public`, which ask for no account) by address alone, one
window across both:

- HTTP: `ai.Paced`, installed by zen's `front` ahead of its Claim and of every route
  ai mounts after it (plugin/ai mounts zen first), so the API, the chat apps, a Zen
  SKU, decisions, speech, media and the MCP door's `aiChat`/`aiDecide` (which re-enter
  this router, `calls.go`) spend one count. POST on `controllers.Entitled` paths,
  retrieval (`/v1/embeddings`, `/v1/rerank`) and live-transcript pushes/closes aside.
- The plane: `buy` (`chat_rpc.go`), for every sibling's call (coding runs, bots,
  agents), before zen or the model API is asked.

Every paced answer carries OpenAI's `x-ratelimit-limit-requests`,
`x-ratelimit-remaining-requests`, `x-ratelimit-reset-requests` (a duration, "59s",
"1m0s", whole seconds rounded up) from the count that decided, before the first
byte, a stream's included; the edge exposes them (`corsExposeHeaders`). Past it: 429,
`Retry-After` (whole seconds), the same headers, body
`{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error",…}}` stating no
figure, and no model is reached. A plane refusal is `client.Chat{Status: 429}`.

    PUT  /v1/admin/usage/policy/rate {rpm}  adminSetUsageRate: SuperAdmin, 1..1,000,000; audited
                                            admin.usage.rate.attempt (503 when the trail refuses)
                                            then admin.usage.rate (before/after); GET policy has `rate`
    GET  /v1/ai/limits/rate                 aiRateLimit: {org, rpm (in force), default, own}
    PUT  /v1/ai/limits/rate {rpm}           aiSetRateLimit: org owner/admin or SuperAdmin
                                            (cloud.Admin) and `cloud.CSRF` (403 for a page's
                                            cookie), 0 clears, > default 400, shared signup
                                            org 400; audited ai.limits.rate.attempt then
                                            ai.limits.rate; withheld from MCP (`limit` noun)

**Fast mode is paid from prepaid on every plan that includes it.** A chat asking for
it (`X-Fast`, or the body's `fast`, read as hanzoai/ai `wantsFast` reads it, or a
vendor's own `speed: "fast"`, in `limitAsk`) is `Ask.Fast`. It needs the `fast` plan
entitlement first (below); then `Admit` answers `PaysPrepaid` with `Cash` (cash, then
the org's line, never granted credit), counting nothing of the plan, an Unlimited one
included; with no money of its own the payer is refused 402 `prepaid_required`. Fast mode in code is
the provider race at `fastWidth = 2` (hanzoai/ai controllers/fast.go); 3x and 4x do
not exist. "Served past the NVIDIA lane" is not represented: no route, provider or
lane names NVIDIA's keys, and nothing tells the who-pays decision which provider will
serve (the paid/free lane of `controllers.Seat` is who pays, not who serves).

Pinned by `TestAPersonsFortyFirstRequestInAMinuteIsRefused`,
`TestAnOrgSetsLessThanTheDefaultAndNeverMore`,
`TestTheDefaultIsTheSwitchAndHoldsThroughAnOutage`,
`TestFastModeIsPaidFromPrepaidOnAnUnlimitedPlan` (limits);
`TestTheFortyFirstModelRequestIsRefusedWithItsStanding`,
`TestEverySurfaceSpendsOneCount`, `TestAnOrgsAdminSetsItsRequestsAMinute`,
`TestTheGateTellsThePolicyAFastRequest` (ai); `TestAZenSKUAndAnAIModelSpendOneCount`
(zen); `TestTheRateIsSetBySuperAdminAndAudited` (admin).

## Claude Fable 5.2 and fast mode need a plan entitlement (`entitle.go`, #803)

Two things cost well above the rest of their kind, so a plan serves them only when it
includes them BY NAME: `fable` (Claude Fable 5.2, by every name it answers to:
`anthropic/claude-fable-5.2`, `:batch`, `~anthropic/claude-fable-latest`,
`claude-fable-5-2`) and `fast` (fast mode, ours or a vendor's `speed: "fast"`). Which
model needs one is its capability record's `Entitlement` (apps/ai/capability); which
plan includes one is the usage policy's switch `ai_entitle_<name>_<plan>`
(`flags.EntitlementKey`), shipped on for `flags.SeedEntitled` — Max 15x, Ultra,
Ultimate, Unlimited and the retired Max 20x and Max 50x — and off for every other plan
until a SuperAdmin saves it true. The served rung (`plan.id`) decides; the holder's
plan when the payer holds none (`holder`).

`Admit` asks first, before the fast-mode, free-lane, plan, money and cap steps: a
priced request that needs an entitlement its plan does not include is refused 402
`entitlement_required`, whatever the payer holds (included usage, prepaid cash,
credit), and a payer with no plan holds none. Nothing is counted. The `Hit` names the
entitlement (`Name`), the model for `fable` (`Model`), and the cheapest plan on sale
the policy names that includes it — above the payer's own in its category, else any
(`Upgrade`, ai's upgrade action and `X-Hanzo-Upgrade-Url`). A request with no list
price needs none. A sibling's call over the plane never meets `Admit`, so `buy`
(apps/ai `entitled`) asks `Limiter.Entitle`, the same question alone, and answers 402
in the model API's error shape with `entitlement` and `upgrade` members, or 503
`usage_unavailable` when the plan cannot be read.

An entitled call is billed as before: Fable 5.2 at its listing's rates (OpenRouter's
list plus OpenRouter's 5.5% and the platform's 5.5%: input $8.9042/M, output
$44.521/M, cache read $0.222605/M, 5-minute write $11.13025/M, 1-hour write
$17.8084/M on 2026-11-19), premium class, from the plan's included usage, then
prepaid. Fast mode bills each raced attempt as its own usage row at those rates. A
vendor's `speed: "fast"` ($16/$80 list) is not served on the shared account at all
(hanzoai/ai `sharedFields`: `speed` only `standard`), so no shared-account call is
billed at standard rates for fast tokens; on an org's own connected account the
vendor bills the org.

**The read says it ahead of time.** `Read` fills `Standing.Entitlements` for every
name in `flags.Entitlements` with the same two answers the refusal uses (`includes`,
`entitledWay`), and `GET /v1/ai/limits` (apps/ai `view`) carries it as `entitlements`:
`{"fable": {"included": false, "upgrade": "max-15x"}, "fast": {...}}`. A model picker
hides or marks Claude Fable 5.2 and fast mode for a plan without them instead of
letting the request meet the 402; a switch a SuperAdmin flips moves the read as it
moves the refusal. No label rides it (a version number reads as a figure, and the read
carries none). Pinned by `TestTheStandingSaysWhichEntitlementsThePlanIncludes`
(limits) and `TestTheUsageReadSaysWhichEntitlementsThePlanIncludes` (apps/ai).

Pinned by `TestFableNeedsAPlanThatIncludesIt`, `TestFastModeNeedsAPlanThatIncludesIt`,
`TestAPlanThatIncludesThemIsServedAsBefore`,
`TestTheRefusalNamesTheEntitlementAndAPlanThatIncludesIt`,
`TestTheUsagePolicysSwitchDecides`, `TestOnlyABilledRequestNeedsAnEntitlement`,
`TestEntitleAnswersThePlanesDoor`, `TestTheEntitlementsAgree` (limits);
`TestTheHookRefusesFableToAPlanWithoutIt`, `TestAVendorsFastLaneIsFastMode`,
`TestASiblingsCallNeedsTheEntitlementToo` (ai).

## A live transcript is one request (`middleware_ratelimit.go`, `cloud.SetAdmitted`)
