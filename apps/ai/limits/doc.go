// Package limits is the usage policy: who pays for each AI request, decided before
// it is served. Every gate that serves a model asks it — the ai gate and the
// co-resident zen alike — so one payer's counts hold whichever gate served them.
//
// Every model is in a class: premium (third-party frontier models), ours (Hanzo's
// own) or free (the free lane). A paid plan includes, per seat and per billing
// period, usage of premium and of ours measured at list price — what the caller would
// have paid — with an optional shorter window on each, and two request windows (a
// session and a UTC day) over every request it covers. A priced request is paid, in
// order, by:
//
//  1. the plan's included usage of the model's class, while the model has share
//     left: a model rule may cap what one model (or a glob of them) takes of the
//     class, per plan, per period and per UTC day;
//  2. money, once the plan's usage is spent and the plan allows overage, and always
//     for a payer with no plan: prepaid first — usage credits the payer bought pay
//     for the model it asked for — then granted credit where the model takes it
//     (grantsPay). Nothing is asked of the payer first. Prepaid is the payer's own
//     money, finance.Funds.Own: its cash, then what is left of the org's credit
//     line, which only the org's own account draws — the order finance debits it
//     in. A model granted credit does not pay for is debited from that money alone
//     (Grant.Cash);
//  3. a model's daily cap for the payer's plan, where one is set ("free" for no
//     plan): the requests it serves free to a payer nothing above paid for. A
//     request inside it is free; a cap of 0 means the model needs a paid plan,
//     prepaid or granted credit. A cap is never in front of money: whatever pays
//     comes first, so a payer who holds money is never capped;
//
// and past all of them it is refused: a paid plan waits until its usage reopens or
// buys usage credits (plan_allowance_used, model_cap — which names the Hanzo model
// the payer may choose instead), and a payer with no paid plan is told what pays
// (insufficient_balance). A free-class request a plan covers counts in its request
// windows, so limited mode cannot be farmed; a payer with no plan meets the free
// allowance instead (apps/allowance).
//
// A plan counts when a card paid its period, or when SuperAdmins recorded it
// (client.Subscription.Settled "external:<processor>": Square, wire, or a comp). A
// plan bought with credit or from the balance confers its tier and covers nothing.
//
// GRANTED CREDIT NEVER PAYS FOR A THIRD-PARTY MODEL unless an operator says so.
// Credit is what the platform grants so a person can try what Hanzo serves; a premium
// model is paid by a paid plan or by cash, which only a paying customer adds. A model
// with no rule of its own takes granted credit as its class does (flags.CreditKey:
// premium never, ours always, as seeded), and a premium model nothing else pays for
// is refused 402 insufficient_balance, never served on what credit would have paid.
//
// CLAUDE FABLE 5.2 AND FAST MODE NEED A PLAN THAT INCLUDES THEM BY NAME (#803). Each
// costs well above the rest of its kind, so before anything above is asked, a priced
// request for one is refused 402 entitlement_required unless the payer's plan includes
// the entitlement — fable, fast — by the usage policy's switch for that plan
// (flags.EntitlementKey; flags.SeedEntitled ships it on for the top of the ladder).
// Money does not lift it, and a payer with no plan holds none (entitle.go).
//
// FAST MODE IS PAID FROM PREPAID, ON EVERY PLAN THAT INCLUDES IT. A chat asked in fast
// mode races several providers for one answer (hanzoai/ai controllers fast.go), or
// asks a vendor's own faster lane (speed: "fast"), so it is paid from the payer's own
// money — cash, then the org's credit line — and never from a plan's included usage,
// an Unlimited plan's included, or granted credit; with none of its own money it is
// refused 402 prepaid_required.
//
// One more bound holds besides every one above, and money never lifts it: the
// requests a minute one person may make of models (rate.go).
//
// The figures are the policy record a SuperAdmin edits at admin.hanzo.ai — per plan
// (apps/flags PlanKey), per class (flags.CreditKey) and per model
// (flags.ModelPolicy) — read live.
// Settling records what a covered request used, never past what its class has left.
// There is no debt.
//
// The customer reads every window as a share used, never as money.
package limits
