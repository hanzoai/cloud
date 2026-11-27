# sdk/meter — check the balance before, record the cost after

**The SDK holds the contract, a driver holds the books.** `meter.Client` applies the
rules every act meets (a user, an org, a non-negative sealed debit) and hands the
money to a registered `meter.Ledger`. The prepaid-wallet debit (`finance`), the
commerce HTTP read, the plane crossing and the USAGE-stream hand-off are
`internal/driver/ledger`, which registers from init; `internal/driver` and `cmd/cloud`
blank-import it (`drivers.go`, pinned by tests), so every binary that serves links it.
With no ledger linked a process that was not told to bill meters nothing (`meter.Free`);
one that was (`CLOUD_ENV` set, or a `Config.BaseURL`) gets a client that refuses every
call with `ErrNoDriver` — a binary that stopped debiting must fail loudly, never look
healthy. Amounts are `hanzoai/money` values; the ledger folds them into the credit
unit with `cloud/money` at the boundary. Holds against an in-flight balance
(`Commitments`, `Hold`) are here too, over `sdk/coord`.

The one client every Hanzo surface meters through. Commerce's finance ledger is
the books; this package asks it whether a payer can pay (`Authorize`) and debits
what the work cost (`Record`). There is no second copy: `hanzoai/commerce` kept
one under `metering/` (with a `meter-proxy` sidecar and a deploy renderer) and it
is deleted. No deployment ran the sidecar.

## Who pays

A debit's address is two halves, `(Org, User)`: the LEDGER that holds the balance
and the ACCOUNT within it. Both come from one rule, `hanzoai/account.Payer`
(`principal.Payer`, `cloud.PayerOf`), and a caller passes what it resolved:

- In a tenant's own org the account is the org's pool, so `User == Org`.
- In the signup org (`account.SignupOrg`, `hanzo`), whose members are strangers to
  each other, the account is the person, `hanzo/<name>`. The pool beside them is
  the platform's, and nobody's call is charged to it.
- `Actor` (`org/sub`) is attribution only, never the account.

An in-process AI call carries its payer on the request (`ChatRequest.Payer` and
its siblings, an `account.Account`); a request that names an org and no payer is
refused (`cloud.ErrNoLedger`) before a model is asked.

## No switch opens the gate

A balance or a spend cap the ledger cannot read refuses the call. What a caller
sees:

| Reached | Answer |
|---------|--------|
| HTTP edge (`BillingGate`), typed op (`Toll`, `Denied`) | `503 {"error":{"code":"balance_unavailable","message":"Billing temporarily unavailable"}}`, or `spend_cap_unavailable` for an unread cap; the handler does not run |
| In-process AI (`deps.AI`) | the meter's error; the model is not asked |
| Out of funds | `402 insufficient_balance` |
| Over a spend cap | `402 spend_cap_exceeded` |

`METERING_FAIL_OPEN`, `METERING_DISABLED`, `METERING_TIER_AWARE`, `METERING_TEST`
and cloud's `BILLING_FAIL_OPEN` are gone; nothing reads them
(`TestAMeteringOutageRefusesTheBillableCall` sets all of them and is refused).

A client with no commerce URL is not off: it asks the process that owns the
ledger over the plane. Only `plane.ErrNoPeer` — no commerce anywhere in the
deployment — is read as "nobody bills", and then nothing is charged either.

## Idempotency

`Usage.Ref` is the ledger's key for one act (`UNIQUE(kind, program, ref)` in
finance). `Seal` mints one per act when the caller has none; a caller that can
re-drive the same act (a retried transport, a second pod) names the act itself so
it moves money once.

## Files

| Path | What |
|------|------|
| `client.go` | `Client`: `Authorize`/`AuthorizeVerdict` (gate), `Balance`, `Record` (debit), `Post`, `Drain`. |
| `ledger.go` | `Ledger`, `Register`, `Free`, `ErrNoDriver`. |
| `usage.go` | `Usage`, `AuthInput`, `Verdict`, the errors. |
| `middleware.go` | `Middleware` (net/http) + `IdentityFromGatewayHeaders`. |
| `unit.go` | `UnitName`: the words a metered unit reads as on a bill. |
| `reserve.go` | `Commitments`, `Hold`: money committed but not yet spent. |
| `internal/driver/ledger` | the production ledger: finance in process, commerce over HTTP or the plane; `Post`/`Drain` over the USAGE stream. |

## Who pays for an in-process AI call, at what price, and how often

**One payer.** A program in this process that calls a model names the account that
pays on the request: `ChatRequest.Payer` (and `Embed`/`Rerank`/`TranscribeRequest`),
an `account.Account` resolved where the work started — `principal.Payer(c)` on a
request, `cloud.PayerOf(ctx).Wallet` on a stated caller, a payer persisted on the
row a background job runs from (a knowledge file, an index job, a session, a
resident agent's `Payer`). `BillingOrg`, `principal.Ledger` and `LedgerFrom` are
gone. A request that names an org and no payer is refused (`ErrNoLedger`) before a
model is asked: in the signup org (`hanzo`) an org standing in for its member is the
platform's own pool, which is how a stranger's calls landed on Hanzo's books. A
request with no org is the platform's own work and is billed to nobody.

- **Workers AI on the org's own token** is charged one BYO fee a call
  (`CLOUD_AI_BYO_FLOOR_UUSD`, 100 micro-USD), never a share of an inference price:
  the model API lists no price for a Workers AI model.

**One run a tick.** A schedule that charges is claimed by the firing, so N pods
produce one run and one charge:

- *Agent cron* (`apps/agents/scheduler.go`). Each due minute is a row in the org's
  own store (`agent_firings`), shipped fenced (`OrgStore.Sync`) before the run; the
  run is named by the firing (`firingRun`) and its fee keyed on the run
  (`Ref "agent_<run>"`). A deposed owner's ship is refused; a new owner, or a pod
  restarted inside the minute, finds the claim on the copy it hydrated
  (`TestADueMinuteIsOneRunAndOneChargeAcrossAHandover`). A scheduled run is paid by
  the agent's stamped `Payer`, the account its resident hours bill.
- *Automations* (`apps/auto`). Every scheduled kind is claimed by the tick the
  engine fired (`firing`: the schedule's last tick at or before the fire), not only
  daily ones, and the run fee is keyed on it (`feeRef`): a second fire of a tick —
  another engine, or one that crashed before re-anchoring — runs nothing and charges
  nothing (`TestATickFiredTwiceIsOneRun`). Every taken tick is a row
  (`automations_ticks`), so a start held up behind a later tick is not run again.
- *Platform cron* Jobs are named by the tick and pokes land on commerce effects that
  carry their own keys (recharge window, renewal period). Social posts and
  marketplace deadlines take a deployment-wide lease and cannot double.

**Open, and architectural**: apps/auto, social, marketplace, platform compute and
projects storage keep their rows in one SQLite file per pod (`locus`), not an
`OrgStore`. A deployment lease over such a file sweeps only its holder's slice, and
an automation armed on a pod an org has left keeps firing there. Production runs one
replica (Recreate), so no slice is orphaned today; moving these stores onto
`OrgStore` is what makes a second replica safe.

**Open, and the owner's**: work no person started still pays from its org's account
— a chat bridge's turns (`agents.RunOnBehalf`: the org that connected the workspace
pays, by design), an index of a synced linked repository (`git` `indexPayer`), an
automation's run fee, a resident agent or session from before payers were stamped.
In a tenant org that account is the tenant's. In the signup org it is the
platform's pool, which is right for the platform's own work and wrong for a
stranger's; the rows do not say which. A plain signup-org member is refused an org
scope today (403 "an org scope is required"), so it is staff whose work lands there.
Refusing unattended work in the signup org, or stamping the person on a bridge link
and a repository link, is the decision this leaves.

## What one unit of metered work costs — the meter authority, asked over the plane

The platform meters work in several apps — a GB-month of block storage, a
thousand characters translated, a risk screen. Each priced its own with a
compiled constant plus an env override and the same eight-line resolver copied
beside it. An env var keeps no history, so nothing could answer what we charged
in March or who changed it, and three copies of one rule is three chances for
them to disagree.

The price is a ROW now, in commerce's meter authority, edited at admin.hanzo.ai
with an audit trail. The apps that meter are separate processes and cannot open
that store, so they ask the process that owns it — `plane.BillingRate`, published
by `apps/commerce/rate_rpc.go`, the same shape `resource_billing_peer.go` uses for
the spend gate and for the same reason.

**It takes no subject.** What a GB-month costs does not depend on whose bytes they
are; a plan may include some of it and a wallet pays for the rest, and both of
those are asked elsewhere.

**The floor is a PRICE, not an error path.** `cloud.RateNano` (and the unit
wrappers below) take the caller's compiled constant as a floor, and a meter with
no row, an authority that is unwell, and a process with no commerce beside it all
return it. Every floor is what that app charged before the authority existed, so
an unreadable price keeps charging exactly what it charged yesterday. Failing
instead would stop a customer's storage provisioning over a missing row.

**`Found` rides beside the number** because ZERO IS A PRICE — something the
platform meters and gives away — and it must not read the same as "nothing is
published", which is what sends a caller to its floor. A bool carries a
difference a number cannot. A row that is stored but not sold (`Status` draft or
archived) reads as absent, the same answer the public plan catalog gives.

**Ask in the unit you bill in.** `RateCents` and `RateMicros` take the floor in
the unit the answer comes back in, so a call site names one number and does no
arithmetic. That is not sugar: three apps each held a factor and multiplied up
then divided down — six expressions and four constants for two facts, one of
which was already declared elsewhere in the tree. The factors are unexported,
because a caller holding one is a caller doing the conversion itself.

**Testing a conversion needs two tests.** A round trip CANNOT see a wrong factor:
multiply and divide by the same wrong number and the floor comes back unchanged.
Measured — with `nanoPerCent` wrong by 10x, `TestRateUnitsRoundTripTheFloor`
passes and only `TestTheUnitFactorsAreWhatTheyClaim`, which reads the factors in
one direction against arithmetic tied to the seeded rates, fails.

Two products are deliberately OUTSIDE the authority, and a test in commerce
refuses them: a MODEL is priced by its `ModelRoute` row (`InputPrice`/
`OutputPrice`, resolved org-first and read ahead of config and the compiled
table), and a TOOL is priced by its marketplace listing, set by the publisher who
is paid for it. A row here would be a second answer to a question that already
has one, on the money path, with no stated precedence.

## What a metered unit reads as on a bill — `metering.Name`

A debit's unit is `Usage.Model`: an AI model id, or the word a surface meters by
(`session`, `op`, `desktop/visor`). The ledger keys on it and stores no name, so
`metering.Name` is the one place a unit becomes words. `/v1/billing/usage` stamps
it on every row as `name`, and `/v1/usage/summary` names each `byCategory` line
with it, from the entry's memo (the unit), never its ref (an act id). A unit with
no entry reads as itself, which is right for a model. A new sandbox class needs a
name or `TestEveryClassIsNamedOnTheBill` goes red.

## What the money plane measured: one road for telemetry, and a quantity for the meter

The ledger is exact, double-entry, idempotent and PRIVATE. That is right for
money and useless for the operator's question, because "is anything still being
billed" means reading every org's books and summing. It is not a question about
money; it is a question about whether a code path runs, so it is a measurement —
and nothing measured it.

Nothing COULD. cloud carried an `O11yClient` on `Deps` — a Counter/Timing/Span
surface with a disabled stub — and `build.go` never assigned the field. So it was
nil in every deployment, the one call site in the binary (`apps/auto`'s run
counter) returned at its own nil check, and every other product emitted nothing.
Two instrument APIs, and the unwired one is the one products were told to use.

Four series, and which labels each carries is decided by which question it
answers, not by symmetry:

    hanzo_usage_gate_total{verdict}             allow | no-funds | cap-spent | unknown
    hanzo_usage_debits_total{outcome}           posted | unbilled | failed
    hanzo_usage_charged_usd_total{product,org}
    hanzo_usage_tokens_total{product,org,direction}

The two plane-level series carry NO product label and are seeded at zero across
their whole vocabulary, so silence reads as zero — `metrics_plane.go`'s doctrine,
and the same doctrine forbids seeding per product: every `cloud.Service` gets a
Meter named after its app and most never charge for anything, so a seed per
product would mint a hundred series that can only ever read zero, which is the
false "it stopped" pointed the other way. The per-product question is asked of the
attribution counters instead, which are born on a product's first real charge.

`unbilled` is the third outcome and not a rounding of the other two: a debit into
a deployment with no commerce anywhere is correct, and the SAME reading in a
deployment that has one is revenue leaving with a green error rate.

The verdicts are split because they belong to different people. no-funds and
cap-spent are the customer's to fix; unknown is the operator's. Folded into one
"denied" counter, a commerce outage looks exactly like a fleet of broke customers.

`hanzo_usage_tokens_total` is the only place the QUANTITY survives. `Usage` carries
the token counts and `apps/commerce/meter_rpc.go` rehydrates them across the plane
under a comment saying a field this handler drops is lost as completely as one the
crossing never had — and then `money.UsageInput` has no field for them, so they were
dropped at the next hop, at the ledger. The ledger is right to hold dollars and
nothing else; the quantity belongs on the telemetry plane, so that is where it is.

Commerce's side of the same act had the mirror defect (`api/billing/usage.go`). A
meter event's `Value` is a QUANTITY — `AggregateUsage` sums it and hands the sum to
`PricingRule.CalculateCost` — and `RecordUsage` wrote `req.Amount`, the debit in
cents. Non-zero it was priced a second time; on the preferred `amountMicros` path
`amount` is absent, so the value was 0 and the usage line vanished. It records the
tokens now, BEFORE the cent rounding, because a sub-half-cent charge rounds to
$0.00 and is acknowledged without a debit — fair for money, silent erasure for
work, and per-token pricing makes most single calls sub-half-cent. It goes through
`engine.IngestUsageEvent` rather than the hand-rolled copy of it that lacked the
dedup, so a retried completion counts its tokens once.

There is no per-request vCPU-second or GB-second meter, and that is the pricing
model rather than a hole in it. API and inference are debited per request and per
token; an instance is debited a create fee plus a recurring GB-day footprint
(`apps/provisioning/dedicated.go`); a tenant VM is a monthly plan row in the
infra-tiers catalog. A per-request compute cost is therefore something a benchmark
MODELS to compare against a competitor who prices that way, not something anything
here bills. What these counters add is the measured $/request per product, once
live traffic runs through them — the figure that has been modelled until now.
