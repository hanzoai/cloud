# LLM.md — apps/commerce

Guidance for apps/commerce. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The money plane runs locally, and `make e2e` proves the prepaid cycle

`commerce` is co-resident (`apps/commerce/mount.go` → `commercemod.Embed` on cloud's own zip
app), so the billing surface, the ledger, and the gate are all one process. Two facts
about running it that are easy to get wrong in opposite directions:

**There is no /v1/billing or /v1/commerce FORWARDER any more, and forwarding is why.**
`account-bridge` was a 112th app whose whole manifest row was the two BARE stems
`/v1/billing` and `/v1/commerce`. Behind them sat `GET|POST /v1/billing/*` and a
five-method `/v1/commerce/*`, each re-dialing commerce over HTTP with the admin
`COMMERCE_SERVICE_TOKEN`. That token satisfies commerce's
`MayMintMoney = IsServiceToken || IsSuperAdmin`, so **forwarding WAS authorization**:
every path that reached commerce through it executed with PLATFORM authority, not the
caller's, and the only thing between a signed-in org member and `POST /v1/billing/deposit`
was a hand-maintained per-method allowlist that had to stay ahead of every mint route
commerce would ever add. A default-refuse table is the right shape for that job and still
the wrong job to have.

Two consequences worth stating plainly. **The bare stems are now UNCLAIMED** — no row was
widened to inherit them, because a leaf nobody names is surface nobody serves, and a 404
from the `/v1` remainder is a louder failure than a catch-all that answers "sign in to view
billing" to a pricing page (which is exactly what the stems did to the whole self-service
paid path before commerce's row named each leaf). And **the ten merchant store heads**
(`product`, `order`, `user`, …) the `/v1/commerce/*` half allowed had no native handler at
all — but they had no working route either: the forwarder re-dialed `COMMERCE_URL`, which
is unset everywhere and defaults to the public edge, i.e. THIS binary, where `/v1/product`
matches nothing deeper than ai's `/v1`. Deleting it removed a hop, not a surface. The
split-deploy case belongs to `apps/commerce/transport`, whose RoundTripper dispatches
in-process when commerce is co-resident and falls back to plain HTTP when it is not —
under the native handler's own subject-pinning, with no admin token in the browser path.

**At-rest posture is a CAPABILITY question, answered once.** commerce's per-tenant money
stores open a concurrent read pool AND a serialized write pool on the same file, which
needs the LIVE libsqlcipher codec — the pure-Go codec envelope is single-writer and
cannot serve that shape. So `commerceMasterKey` gates on `sqlitedrv.CodecLinked()`, the
the same predicate:

- codec linked (the image: `CGO_ENABLED=1 -tags "libsqlite3 sqlite_fts5"`) ⇒ inject
  cloud's master key; commerce encrypts, and its own `resolveMasterKey` still fails
  closed if the key is absent, so production can never quietly write plaintext.
- not linked (`make build`, which is `CGO_ENABLED=0`) ⇒ hand over nothing, and commerce
  opens its documented zero-config unencrypted dev store.

Injecting regardless is not a stricter posture — on a pure-Go build it is a hard refusal
from `resolveDEK`, `Mount` never reaches `transport.SetApp`, and every S2S billing read
then falls through to the network and DNS-resolves the in-process placeholder. That is
the failure `apps/commerce/transport` documents: balance reads that answer
"Insufficient balance" on funded accounts, with DNS named as the cause.

**The commerce transport is being DELETED, and here is exactly how far that got.**
`apps/commerce/transport` exists so a co-resident caller can reach commerce's S2S
billing surface. It does that by building an `*http.Request` and dispatching it
into the whole shared fiber app — every edge middleware — which is why it carries
`maxDepth = 8` counted in a map keyed on a goroutine id parsed out of
`runtime.Stack`: a middleware that read commerce while serving a commerce read
re-entered the app without bound. The counter is a description of the defect, not
a fix. The fix is to call the OPERATION instead of re-entering the router.

Worse than the recursion: most of those paths are not registered in this binary at
all. commerce's own `api.Route()` bundle is behind `//go:build cloud` and is never
compiled here, so `/v1/billing/usage/rollup` and `/v1/billing/transactions`
dispatch to a 404 and `/v1/billing/balance` re-enters cloud's OWN customer handler
and 401s. Split into per-app binaries — the real deployment, ~25 processes — the
base URL is empty everywhere but commerce's process, so each reader reported itself
"not configured" and answered a silent zero. That is not a degraded path; it is a
path that has never worked in either shape.

CONVERTED (they ask the ledger by name, over `plane/commerce`): `apps/books`,
`apps/usage`, `apps/payout` (and with it `apps/referrals`, `apps/affiliates`,
`apps/authors`). `plane.FinanceSpend` is the op that answers "what has this org
consumed" — the ledger's own windowed sum, the same figure the rolling spend cap
reads. `plane.FinanceTxns` now takes a `TxnsIn` naming the books (`test`) and the
page size, because sandbox money and real money are physically separate files and a
reader that posts test rows into real revenue restates the company's income.

**A finance usage entry carries its scope.** `finance.post` writes the debit's
`project` and `service` onto the entry (`sqlstore.addEntryScope` adds the columns to
an older file), and `SumUsageSince(org, test, project, service, since)` narrows on
each non-empty axis: a cap on one service sums that service, an org-wide cap sums
everything. An entry written before the columns existed is unscoped and counts
toward the org-wide sum only. Pinned by `apps/commerce/scope_cap_test.go`.

STILL ON THE TRANSPORT, and why: `apps/metering` (`/v1/billing/tier`),
`apps/admin/commerce` (`/v1/billing/subscriptions`,
`/v1/costs`), `apps/content/storefront` (`/v1/store/current`, `/v1/product/{handle}`),
`apps/billing` (the `portal/methods` proxy). Each of these
answers from commerce's OWN datastore through a handler in the `hanzoai/commerce`
module whose logic lives in unexported helpers — so a plane op for them means the
payments.go pattern (export a value-taking core from the module, declare the op on
it here), NOT a second implementation in cloud. Writing the verdict twice is how a
spend cap and a rate limit come to disagree about which requests they bind. Until
those land, `maxDepth`, the goroutine-id parsing and `CLOUD_COMMERCE_HTTP_URL`
stay — the conversion is not done, and nothing should pretend otherwise.

**A co-resident peer call does not touch a socket.** `plane.Ask` asks
`zip.Serving(app)` first (zip v1.26.1+): when this process serves that app's
canonical socket, the op runs through `zip.Here` — the op's own invoke client, the
same validate/authorize core REST and MCP land on — with no dial, no encode and no
router. Measured under `strace` with `connect()` traced: six op handlers ran, and
the only four `connect()` syscalls in the binary were the test harness's own
readiness probes. The decision is made once, inside the one dispatcher, so a caller
names the op and never learns where it ran. See `apps/commerce/here_test.go`, which
proves it by UNLINKING the socket and showing the nested read still answers, with a
mutation that breaks the direct path and shows the call fail.

**The gate only enforces on a kind that costs something.** `ResourceMeter.Gate`
short-circuits to allow for `costCents <= 0`, and most per-kind fees default to 0, so a
suite that does not price a kind proves nothing about billing. `e2e/run.sh` prices one
(`CLOUD_TODO_FEE_CENTS`) and passes the SAME number to the suite as
`E2E_TODO_FEE_CENTS`; spec 136 refuses to run at 0 rather than passing emptily.
`todo` is the client because its gated create depends on nothing but the local store —
a 402 there is the billing gate, not object storage or an exec runtime answering first
(both of which precede the gate on the deploy and invoke paths).

**A customer org arrives funded, by policy.** A new customer org holds the $5 starter
credit (`commerce billing/credit`.StarterCreditCents — the same "$5 free credit" the
developer plan advertises, a non-cash TRIAL grant tagged `starter-credit` and spendable
on non-premium metered usage only). That is why the local suite can exercise the funded
path without seeding a balance: it is the real self-service path, where a fresh org can
buy work the moment it signs up. Note the billing SUBJECT differs by org kind — a
customer org pools at the org (`acme`), while the brand org resolves per-user
(`hanzo/z`), which is `principal.Subject` → `account.Payer`, not an inconsistency.

What `make e2e` now holds: an org below the fee is refused **before** the work runs and
is not charged for the refusal; an org that can cover it is admitted and debited exactly
the fee, with exactly one usage row; draining the balance re-arms the refusal (which is
what proves the debit is real rather than cosmetic); and one org's spend never moves
another's ledger. The specs are `describe.configure({mode:'serial'})` — not by
preference, but because they all move the same balance and the suite is otherwise
`fullyParallel`.

### A priced call is admitted on the whole balance

A wallet holds cash (paid) and credit (minted: grants, promos, Hanzo's own balance), and
the org's own account may also hold a credit line; all three pay for any call. The ai
gate's reader (`apps/ai/balance.go`) and zen's `commerceGate` weigh
`finance.Funds.Total` (cash + line left + credit); a metered debit draws cash, then the
line, then credit (`finance.drawUsage`), a payment or hold cash then credit (`draw`).
Pinned by `TestGrantedCreditAdmitsAPricedCall`, `TestThePlaneReadWeighsCreditAndCash`,
`TestGrantedCreditPaysForAPricedCall` and `TestGrantedCreditPaysForAZenCall`.

### The credit line, and every served call counted (`finance/line.go`, `finance.record`)

- **Line.** `treasury_limit` in the org's own books (default 0), set by
  `finance_limit` (SuperAdmin, `cloud.Who(ctx).Admin`, audited `admin.credit.limit`
  before/after through `deps.Audit`, 503 when the trail does not take it) behind
  `PUT /v1/admin/orgs/:org/credit {limitCents}`; read by `finance_line` /
  `GET /v1/admin/orgs/:org/credit`; `GET /v1/admin/credit` lists every org with a
  line or a debt. Only the org's own account (`cash`/`wallet`, no member suffix)
  draws on it — in the signup org a person's wallet never does.
- **Owed is cash below zero**: the amount due and the receivable. Granted credit never
  pays it; the next cash deposit does, first. What cash, line and credit cannot cover
  lands on cash too (debt past the limit), because the call already ran. A legacy
  negative credit account folds into cash (`fundsOf`). `Funds` = {Cash, Credit, Line,
  Owed, Limit}; `client.Balance` appends Line, Owed, Limit (empty = a ledger that does
  not report them: unknown); `/v1/billing/balance` adds `creditLimit`, `due`, `line`.
- **On account** (`apps/commerce/account.go`). The platform switch
  `billing_on_account` (org names, SuperAdmin-set, audited by the switch store) puts
  an org's OWN account on a line with no limit: `finance.Funds` answers `Account`
  and `Line = open` ($1B, constant, never reduced by Owed) through the reader commerce
  installs (`finance.SetAccount`), so every gate that reads funds (limits `Admit`, the
  ai balance gate and its reservations, the edge meter) admits on money; quotas,
  windows, model caps and spend caps still apply. Usage posts as on any line, to Owed.
  A signup-org person's wallet is never on account. Payments from the balance (x402,
  job holds) still need cash, and no plan is ever paid from the balance: the line pays served calls
  only. `treasury_owing` holds when the account last went from owing nothing to owing
  (`Line.Since`; 0 for a debt older than the table); `Line.Used` is the org account's
  usage this UTC month. `billing_on_account_alert` (cents, 0 = off) puts an org past
  it on the audit trail (`billing.account.owing`) and the pulse, once a UTC day per
  process. The switches are read once, then refreshed in the background every 30s;
  a flag plane that is down keeps the last answer. A payment is the wire grant below.
- **Not a commerce invoice.** commerce's `billinginvoice` store is reachable from the
  embed, but its collect waterfall (and dunning) draws credits then balance first —
  granted credit paying a cash-only debt, and a draw against the same negative
  wallet. The receivable stays on the books, listed by `GET /v1/admin/credit`.
- **Cash a SuperAdmin records** (a wire, paid outside the processor) is the ONE grant
  path, not a route of its own: `POST /v1/admin/grants` or
  `/v1/admin/customers/:org/credit` with `source: "prepaid"`, `payment:
  "wire:<bank reference>"`, US dollars only (finance holds one asset; another
  currency is refused, never booked as dollars), at most $100,000 (`maxCashCents`, one
  line, the owner's to raise). It lands through `finance.Deposit{Cash: true}`
  (`funding:cash` → `cash[:<name>]`), the same deposit a card settlement makes, so it
  pays what is owed first; undone by `POST /v1/admin/grants/:id/reverse`.
  - **One wire credits once across EVERY org, decided by the BOOKS**
    (`apps/admin/core/wire.go`). A deposit ref is unique only inside one org's file.
    `WireKey` keys a wire by its reference's letters and digits (`FT24-1001-XQ`,
    `ft24.1001.xq` and `wire:WIRE:FT241001XQ` are one key; non-ASCII refused); `KeyOf`
    reads any older spelling the same way. Before any money moves a claim row
    (`admin.customer.credit.claim`) is written with `Audit`, which answers its error:
    no claim, no money. Every org the trail names for the key (claim, grant, failed
    grant) and the org asked for then answer from their books (`finance_wires`, the
    org's `wire:` deposits with `Reversed`): standing for the same account and amount
    answers the first receipt with `already` (nothing moves; a missing grant row is
    restored from the books, else an `admin.customer.credit.again` row); standing
    anywhere else is 409 and a `deny` row; reversed or absent posts under the first
    free ref (`wire:X`, `wire:X~1`, …). So a grant or reversal row lost after the money
    moved, or a reply lost after the deposit committed, cannot hide the money.
    Recordings and `ReverseGrant` share a process lock; a deployment that declares
    more than one writer (`Base.Peered`) refuses wires with 503.
  - `user` must be a member IAM holds (`FindMember`); `memo` must name the account
    (`MemoKey` of `client.WireReference(subject)`), 409 otherwise.
  - `GET /v1/admin/payers?reference=TOPUP-…` reads a statement's reference back to the
    accounts it could name, off the admin directory, exact matches first (the reference
    is lossy: `acme-co` and `acme/co` print alike). `GET /v1/admin/grants?payment=wire:`
    lists the wires. `client.WireReference` is commerce's rendering, held equal by
    `apps/commerce TestWireReferenceIsCommerces`.
  - Pinned by `apps/admin/customer/wires_test.go`, `wires_loss_test.go` (a lost reply,
    lost grant and reversal rows, separators, a legacy spelling), `wire_test.go`,
    `payers_test.go`, `apps/admin/core/wire_test.go`.
- **Crypto top-ups reach these books** as cash since commerce v1.50.177: see "A
  confirmed crypto deposit is cash" below.
- **A plan a SuperAdmin records** (`POST /v1/admin/customers/:org/plan`, processor
  one of `client.RecordProcessors`, never prepaid money, the payment's ids in
  `reference`) opens on `principal.PayerFor(org,
  user)`: `user` names a member, as on a grant. In the signup org (`hanzo`) a person
  pays from `hanzo/<name>`, so their plan needs `user`; without it the plan lands on
  the org's own account, which their tier and model gate never read. Pinned by
  `TestRecordPlan_LandsOnTheMembersWallet`. A self-serve card sale needs none of
  this: `POST /v1/billing/subscribe/card` opens the row and its paid invoice in one
  act, and `apps/commerce/plan_limits_test.go` reads it back through the limiter.

### A confirmed crypto deposit is cash (`/v1/billing/crypto/*`)

- **Mint.** `POST /v1/billing/crypto/deposit {chain, token, amountCents?}` (apps/billing
  `rails.go`) asks commerce's `billing_crypto_options` first and refuses an asset the
  menu lacks with 400 + `{chains, tokens, assets}` (pairs: a chain and a token each on
  offer are not a pair on offer); an empty asset menu or no custody is 503. Then `billing_crypto_mint` → `commercebilling.CreateCryptoDeposit`, payer = the
  caller's wallet (`subjectFor`), never the body. A wallet-created account is its own
  org `wallet-evm-<hex>` paying from its pool, so subject = org.
- **Answer** (`client.CryptoDeposit`, POST and `GET .../deposit/{id}`): id, status,
  chain, token, depositAddress, addressTag, expiresAt, then appended (commerce
  v1.50.178) amountCents (what was named), minimumCents (smallest deposit that credits:
  `minimumCents(Terms)` in the module, fee + 1 cent; absent when nothing watches the
  asset), feeCents (`CRYPTO_DEPOSIT_FEE_<CHAIN>`, absent = none), requiredConfirmations
  (`RequiredConfirmationsForChain`: base 20, ethereum 12), confirmations,
  creditedCents, instructions (`apps/commerce/rails_rpc.go` `instructions`). The mint
  has no minimum of its own. Status: pending → confirming (seen, shallower than
  required) → succeeded (credited).
- **Credit.** commerce's watcher (`billing/depositledger`, started by `commerce.Embed`)
  credits a transfer once it is requiredConfirmations deep through
  `creditledger.Credit` → `apps/commerce/ledger.go` → `finance.Deposit{Cash: true,
  Ref: "crypto-deposit:<chain>:<tx>:<event>"}` at (org, payer, network's books), then
  writes its own `cdep_` row and advances the intent. The ref is the on-chain event, so
  re-scans, restarts and replicas credit once; a re-priced pass funds the amount first
  recorded. Before commerce v1.50.177 the watcher wrote only commerce's own row, so a
  confirmed deposit marked its intent succeeded and funded nothing; a row from then is
  funded when its transfer is scanned again, and one already behind the cursor needs a
  manual grant.
- **Read it back.** `GET /v1/billing/balance` → `cash` (also `available`); in code
  `finance.Funder.Funds(org, payer, cloud.SandboxOnly()).Cash`. Never `credit`: a paid
  deposit is not minted money. Pinned by `TestConfirmedCryptoDepositIsSpendableCash`
  (apps/commerce: real mint, real watcher store, fake custody and chain) and
  `TestADepositIsMintedOnlyForAnAcceptedAsset` (apps/billing).
- **An agent can do all of it.** A caller presenting an API key or Bearer is exempt from
  the browser anti-forgery control (`Intended`: a presented credential cannot be forged
  into), so `POST /v1/billing/crypto/deposit` works from a
  key with no cookie and no wallet library. `GET /v1` names the capability, the 402 on
  an exhausted balance names `topup`, and `/llms.txt` walks wallet → IAM org → key →
  deposit.

## Commerce reads secrets through `deps.KMS`, and declares its own webhook route

- **Webhook route.** `POST /v1/commerce/webhooks/:provider` is declared by commerce's
  `setupRoutes` on this app, and nowhere here: two declarations of one path is a zip
  composition failure. Its prose stays in `describe.go`.
- **Square.** The verifier has no freshness window — Square keeps `created_at` across
  retries, and a 5-minute window refused every retry of an event first sent during a
  deploy (`Recreate`). Replay is stopped by event id and by money postings keyed on
  payment/refund/dispute id.

## An entitlement is commerce's, asked by name (`internal/host/commerceplane.go`, `apps/commerce/entitlement_rpc.go`)

## A plan coupon runs for months (`apps/admin/coupons.go`, `apps/commerce/coupon_rpc.go`)

A plan coupon is commerce's (`api/promo`): a percent off each of the first `months`
billing cycles of one purchase (1-36; 0 is 1, the first month), granted to the
subscription at the sale from its first paid period; a cycle is an invoice
(commerce LLM.md "A plan coupon runs for months"). A SuperAdmin reads and sets one at
`GET|PUT /v1/admin/coupons/:code` (plane `billing_coupon`, `billing_coupon_set`, audited
`admin.coupon.set`); the PUT states the whole coupon, so `months` left out is 1. A
change of months moves no subscription already sold: `POST /v1/admin/coupons/:code/extend`
`{"apply":false|true}` (plane `billing_coupon_extend`, audited `admin.coupon.extend`)
grants the coupon's current months to every live subscription bought with it, from its
first paid period, never shortening one; the dry run answers the same rows and writes
nothing. Buyers see `couponMonths` beside `couponPercent` on
`GET /v1/billing/plans?coupon=CODE` and on the sale receipt.

## Plans are paid by card; prepaid pays usage

The rule lives in commerce (hanzoai/commerce LLM.md "A plan is paid by card"); cloud
carries it to the surfaces.

- **Never from prepaid money.** `POST /v1/billing/subscribe/card` with `sourceId`
  balance/credits is 400 `plan_needs_card`; renewals, dunning and
  `billing_invoice_collect` charge a plan's invoice to the card alone. Wallet plans pay
  their first period and go past due at renewal unless a card plan follows.
  `POST /v1/admin/customers/:org/plan` refuses prepaid spellings and any processor
  outside `client.RecordProcessors` (square, paypal, wire, ach, check, crypto, comp;
  held equal to commerce's by `apps/commerce TestRecordProcessorsAreCommerces`) with
  400 before commerce is asked; `GET /v1/admin/processors` (`adminProcessors`) serves
  the list the UI offers.
- **Auto flow.** A plan recorded outside checkout lapses at its period end (the sweep
  reports `lapsed`). `billing_renew` (`planeRenew`) invoices and charges due card
  subscriptions at period end and never sends automated pre-due reminder emails. A
  card sale while an external or wallet plan runs is scheduled at its end (receipt
  `startsAt`, `startsCents`, `follows`).
- **Admin overrides** (apps/admin/customer/override.go, SuperAdmin, 503 with no audit
  store, audited; the attempt, `<action>.attempt`, is on the trail BEFORE commerce is
  asked and a trail that does not take it is 503 with nothing asked, as is
  `admin.customer.plan` for a recorded plan): `POST /v1/admin/customers/:org/subscriptions/:id/period`
  (`adminAdjustPlan` → `billing_subscription_period`, `admin.customer.plan.period`),
  `POST …/subscriptions/:id/cancel` (`adminCancelPlan` → `billing_subscription_cancel`
  Privileged, `admin.customer.plan.cancel`), `GET /v1/admin/customers/:org/prices`
  (`adminCustomerPrices` → `billing_customer_prices`) and
  `PUT /v1/admin/customers/:org/prices/:plan` (`adminSetCustomerPrice` →
  `billing_customer_price`, `admin.customer.price`): a customer's own per-seat price,
  charged by the card sale and every renewal, the invoice showing list and the
  difference as "Customer price"; records are checked against it. Subscription rows
  carry `customerCents`; `/v1/billing/tier` carries `planName`.
- **Unbounded class allowance.** A plan's premium or ours switch saved as `-1`
  (`flags.Unbounded`) includes all of the class's use (`allowance.unbounded`, never
  pro-rated, no window); `GET /v1/ai/limits` answers `unbounded: true`, percent 0.
  Admin save accepts -1 with no window. Pinned by `TestAnUnboundedClassIsNeverUsedUp`.

## Commerce's own Base is two schemas of hanzo-sql and a bucket (`apps/commerce/base.go`)

Besides its per-org datastore the commerce module keeps one hanzoai/base: its cache collection
(`commerce_kv`) and settings, in `<CLOUD_DATA_DIR>/commerce/base/{data,auxiliary}.db`, opened by the module
itself. Since hanzoai/commerce v1.50.211 (`EmbedConfig.Base`, `store.Host`) and hanzoai/base v1.5.110
(`BaseAppConfig.DataDB/AuxDB/Files`) the host hands it the pools and the filesystem:

- With `CLOUD_SQL_DSN` set, `placeBase` asks `locus.Placed` for subsystems `commerce-base-data` and
  `commerce-base-aux` (system namespace, owned by app `commerce` in `manifest.SQLStores`). A pool is a fresh
  `sql.DB` on the schema, pgx in describe-exec mode (Base's collections change tables while it runs), drawing on the
  process's connection budget; Base asks for two of each (readers, writer), and the module sizes them 4 and 2 open.
- **The move** (`CLOUD_SQL_MOVE=lazy|sweep`; unset leaves the files): the old PLAIN files (not the data plane's
  encrypted ones: `sqlpool.move` takes a `legacy` source path) are first brought to the module's current schema by
  opening the Base on them (`prepare`), then moved by the usual mover: tables made by opening the Base on the schema,
  rows copied, every table's row count and checksum read back, then the registry says `moved`. The files are left
  as the rollback. A move that fails leaves both on files, tried again after the backoff. Base runs one dialect over
  its two databases, so the opener gives the database not being made a `locus.Scratch` schema, dropped after.
  If data moved and aux failed, aux is born empty on the server (it is the Base's request log); data on the server
  and aux anywhere else is refused.
- The mover copies `numeric` as a float (Base's Number fields on PostgreSQL), and `Resync`/`Analyze` quote a
  module's camelCase table names.
- Files: `Host.Files` is the object store (`sdk/object`), bucket `COMMERCE_BASE_BUCKET` (default `hanzo-commerce`,
  which must exist). The module uploads nothing today.
- Tests: `TestCommerceBaseMovesOffItsFiles` (a Base on files with KV rows, moved, read through the server, written
  after, second placement moves nothing, files untouched), run on PostgreSQL only; hanzoai/commerce `store`'s KV suite
  runs on both engines (`COMMERCE_TEST_POSTGRES`).

## The store's orders and carts are listed (`order.go`, `cart.go`)

`GET /v1/commerce/order` and `GET /v1/commerce/cart` (typed ops, `?email=` for one buyer, `limit` at most 200) list
the org's own store, newest first. An org admin or SuperAdmin reads (a list names every buyer); the org is the
validated principal's. The count and the revenue (paid less refunded, by currency) or the carts left (never
became an order) are taken over every match, not the page. The CRM's customer record and Sales page read them
(apps/crm LLM.md, "A person who paid is a customer"). A cart opened here stores its address lower-cased, which is how a query matches it. Tests: `order_list_test.go`.

## The agent checkout protocols mount on their operator's file (`agentic.go`, `acp.go`, `ucp.go`, #424)

Agents paying through native custody follow the crypto links in billing discovery:
read `GET /v1/billing/crypto/options` with the buyer's credential, then
`POST /v1/billing/crypto/deposit` with the selected chain and token. A plan purchase
also supplies `planId`. Use the returned deposit address, token contract and any
required destination tag. Poll `GET /v1/billing/crypto/deposit/{id}` for settlement.
The deposit status is authoritative for this flow, and options reflect the custody
and watcher configured in that environment.

ACP (API 2026-04-17) and UCP (2026-08-25) are `hanzoai/commerce` `agentic/acp` and `agentic/ucp` (HIP-1223), served here because their `Commerce` backends call `api/billing` and commerce's datastore in-process: one unit of one catalog item (`credit-<cents>` or a plan slug), USD, paid with a Stripe shared payment token, the same checkout `/v1/billing/checkouts` opens.

- **Off until configured.** `CLOUD_ACP_CONFIG` / `CLOUD_UCP_CONFIG` each name a JSON file (the OSS host's `CLOUD_X402_CONFIG` shape). Unset: no route is registered, so `/v1/commerce/acp/*`, `/v1/commerce/ucp/*`, `/.well-known/acp.json` and `/.well-known/ucp` answer 404, and the purchase discovery (`GET /v1/billing/discovery` links) names neither.
- **Refused at start, never mounted on a guess.** An unknown field, a missing value or an unreadable key leaves that protocol unmounted, logs why, and reports `commerce/acp` or `commerce/ucp` degraded on `/v1/health` (the release smoke refuses it). The rest of commerce keeps serving.
- **Operator values.** ACP: `origin`, `handler` {`id`, `psp` = `stripe`, `merchantId`}, `order` (https URL with `{id}`), `retryAfterSeconds`, `maxBodyBytes`; optional `links`, `documentation`, `locales`. UCP: `origin`, `handlers` (the profile's payment_handlers), `handler` (the SPT instance id), `order`, `orders`, `signingKey` {`kid`, `secret`: KMS ref of a PKCS#8 PEM, P-256/P-384/Ed25519}, `trusted` (platform profile URLs), `requireSignature`, `signatures` {`windowSeconds`, `skewSeconds`}, `fetch` {`timeoutSeconds`, `entries`, `maxTtlSeconds` ≥ 60, `concurrent`}, `maxBodyBytes`; optional `continue`, `storefront`, `links`, `webhooks` {`attempts`, `backoffSeconds`, `timeoutSeconds`}.
- **The buyer is cloud's.** `withBuyer` puts the validated org (`principal.Org`), the wallet in it (`meter.PayerIn`) and the host on the request context (`c.Locals` is the adapted request's context); no validated principal is the protocol's own 401.
- **Routing.** The well-known files are on commerce's manifest row; `TestTheProtocolDocumentsAreRoutedToCommerce` holds the row to `WellKnown`.
- **Not mounted.** AP2: its spec defines no wire; UCP's AP2 Mandates extension carries it, and `agentic/ucp` passes it through untouched. MPP: a gate in front of the priced route, whose store and seller live only in this process while the routes worth pricing are other processes'. ACP request signatures and the order webhook are not wired.
- **UCP idempotency sweep.** Once UCP is mounted, `ucp.go` schedules `ucp.Idempotency.Sweep` hourly under `coord.Default().Lead("commerce:ucp-sweep")` with a bounded batch of 500 rows per tick. Requires `hanzoai/commerce >= v1.50.251` (`#83`), which scopes UCP records under `"ucp"` and leaves billing webhook guards (`webhook`, `billing-intent`) and other ledgers untouched.

## A plan link lends an org the plan of another account (`planlink*.go`, `/v1/billing/link`)

An org with no live paid plan of its own can USE a plan its owner already pays for on another account: a person's wallet in the shared signup org (`hanzo/<username>`) or an org they own. One plan is lent to at most ONE org, and an org borrows at most ONE plan; the whole org is covered. A link is a row `plan_links(org PRIMARY KEY, source UNIQUE, linked_by, linked_at)` in a system store (`planlinks`, `manifest.SQLStores`: commerce) opened by `Use` with `locus.Use`, dialect-clean, tested on both engines. The two keys decide a race; the reads before a write only name which one broke. A store that cannot open degrades commerce like a failed embed.

- **What a link changes: two reads, nothing else.** `planeTier` and `ActivePaidPlan`, when the org holds no live plan of its own (an own plan always wins), read the plan of the SOURCE account through the canonical per-subject `ReadTier(sourceOrg, sourceSubject)` — never a scan of the source org, which in the signup org would read other people's plans. `planeTier` answers the source's plan, plan name and tier limits with the borrowing org's own `User` and `Balance` and an empty `Subscription` (the row is not the org's to cancel); `ActivePaidPlan` answers the source's slug. "No tier override" stays true: a link says whose subscriptions to read, never which tier to serve. A source whose plan lapsed confers nothing, and the link stays until unlinked (`live: false`). A read that cannot be completed, or a store that is not open, is an error, never Free and never a grant.
- **A link confers gates, not money.** Tier, plan name, entitlements (`ai_entitle_*`) and Unlimited's wallet exemption follow the plan. The included monthly allotment is NOT lent: commerce mints it only from a subject's own subscription rows, so a linked org is granted none, its `planeRollup` (plan resolved from its own rows) shows no plan and no included cents, and its metered usage and spend-cap window are its own wallet's. `TestALinkMintsAndShowsNoIncludedAllotment`. A `?plan=` preview on the rollup is the existing projection and is unchanged.
- **Who may link, decided at the route and re-asked here.** `apps/billing/link.go` states the caller's facts from their credential (never the body): `Privileged` (`administers`: an owner or admin of the org they act in, not the signup org's admins) and `Held`, the accounts they may lend — their own wallet when their IAM row's owner is the signup org (`iam.SelfFrom`), and each org of their signed `orgs` that IAM says they OWN (`cloud.Owns`), never a held org. A plain member of an org holds nothing of it. Commerce refuses: the signup, held or reserved org or the plan's own org (400 `org_cannot_link`), a non-admin (403), an org with a live plan of its own (409 `org_has_plan`), an org already linked (409 `org_already_linked`), an account not held or with no live paid plan (400 `no_plan_to_link`), a plan already lent (409 `plan_already_linked`).
- **Who may unlink.** An owner/admin of the borrowing org, or whoever holds the source account, from any org they act in (`DELETE ?from=`), so a person removed from the org can take their plan back. Unlinking what is not linked is 404. The drop is conditional on the link as read.
- **One rule for the read and the write.** `linkRefusal` and `unlinkRefusal` answer the writes' refusals AND the read's `canLink` / `canUnlink`; the routes never carry a second copy.
- **Visibility.** A change is read by the gates after the sdk's tier answer ages out (`tierTTL`, 30 s) and by a cold `ActivePaidPlan` at once; an ownership answer (`cloud.Owns`) is kept a minute, so a person who just lost ownership can still lend for up to that long.
- **Not covered.** One plan per org is the CTO's decision (widening is additive); the signup org cannot borrow; no money, credit or subscription row crosses ledgers. Link and unlink are on the audit trail as `billing.plan.link` / `billing.plan.unlink` (refusals included).
