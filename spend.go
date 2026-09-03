package cloud

// spend.go answers ONE question for the whole binary — MAY THIS PRINCIPAL SPEND? —
// and it is the only place that answer is computed.
//
// WHY IT LIVES HERE. The edge filters serve.go mounts live in THIS package, so an
// answer computed in a leaf that imports it could never be reached from the edge —
// which is how a binary comes to ship several paywalls and enforce none. One
// predicate, in the one package every gate can import, is what makes the mounted
// gate and the computed answer the same thing.
//
// TWO WAYS TO PAY, ONE ANSWER. The rule is "an active subscription OR a positive
// prepaid balance". Those are two INDEPENDENT facts held by two INDEPENDENT
// authorities, and Stand is the single place they compose:
//
//   - SUBSCRIPTION — resolved by the CALLER, passed in as a Licence. The two callers
//     ask genuinely different questions of commerce ("is org X licensed for the
//     studio product?" vs "does org X hold any live paid plan?"), so the licence leg
//     is an ARGUMENT, not a call made here. Composing it is this file's job; asking
//     it is not.
//   - PREPAID CREDIT — the caller's wallet on the native finance ledger, read here
//     because there is exactly one right way to read it (see below).
//
// THE ADDRESS IS LOAD-BEARING. A money gate that reads a different wallet than the
// debit writes is the bug this codebase has already shipped three times, every time
// by keying the ORG POOL — see apps/principal/wallet.go, which lists them. Read on
// the pool, this predicate would admit every member of the shared signup org for as
// long as the platform's own pool is funded, which is a total bypass and is precisely
// the live free-inference hole (apps/zen/zen.go still gates the pool). So the credit leg
// reads principal.WalletOf's address and nothing else.
//
// CREDIT IS EXACT. finance.Balance returns money.Amount — 18-decimal atto-USD over
// big.Int. The admit boundary is Sign() > 0: zero denies, ONE ATTO admits. No
// threshold, no cent-flooring, no float anywhere on this path.
//
// PROOF, NOT ABSENCE. A caller is Unpaid only when BOTH authorities ANSWERED and both
// said no. If either could not answer, the standing is Unknown — this file never
// converts "the oracle is down" into "this customer has not paid". What to DO about
// an Unknown is enforcement's decision (middleware_spend.go), not the decide's.

import (
	"context"
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/manifest"
)

// creditUnit is the asset the prepaid wallet is denominated in. One asset ships
// (USD, 18-decimal-exact); finance selects the ledger file from it.
const creditUnit = "usd"

// ── the subscription leg (supplied by the caller) ───────────────────────────────

// Licence is a subscription authority's ANSWER about one caller, already resolved.
// The zero value is LicenceUnknown, which is the honest default: a question that
// could not be asked has no answer, and "no answer" is never "not licensed".
type Licence uint8

const (
	// LicenceUnknown — the authority could not answer (commerce absent, query
	// failed, nil result). NOT a statement about the caller.
	LicenceUnknown Licence = iota
	// LicenceNone — the authority answered: no live subscription.
	LicenceNone
	// LicenceActive — the authority answered: a live subscription.
	LicenceActive
)

// ── the answer ──────────────────────────────────────────────────────────────────

// Standing is a caller's resolved commercial standing. The zero value is Unknown,
// which is the honest default: a question not yet asked has no answer, and "no
// answer" is never "has not paid".
type Standing uint8

const (
	// Unknown — at least one authority could not answer (commerce unreachable,
	// ledger unreadable, org unresolvable). NOT a statement about the caller.
	Unknown Standing = iota
	// Unpaid — BOTH authorities answered and both said no: no subscription and no
	// credit. The only proven refusal.
	Unpaid
	// Subscribed — a live subscription admits the caller.
	Subscribed
	// Funded — the wallet holds a positive prepaid balance to burn down.
	Funded
)

// Admits reports whether this standing lets a request through on its own merits.
// Unknown does NOT admit here — it is not an admit, it is an absence, and the
// posture that resolves it is enforcement's, deliberately not hidden inside this
// predicate.
func (s Standing) Admits() bool { return s == Subscribed || s == Funded }

// String renders the standing for logs.
func (s Standing) String() string {
	switch s {
	case Unpaid:
		return "unpaid"
	case Subscribed:
		return "subscribed"
	case Funded:
		return "funded"
	default:
		return "unknown"
	}
}

// Stand composes the three independent legs into the one answer.
//
//	lic   — the subscription authority's already-resolved answer.
//	allow — what that subscription's own usage windows say (AllowanceIn).
//	w     — the money address the credit leg reads (principal.WalletOf).
//
// The legs are evaluated cheapest-decisive-first: a licensed caller inside its
// windows never touches the ledger. A caller with no subscription always does —
// that is the pay-as-you-go path, and the common case for a prepaid customer.
//
// The allowance leg is a PARAMETER rather than something resolved in here, and
// it has no wrapper that defaults it. A convenience Stand(lic, w) existed and
// passed AllowanceUnknown for every caller, which meant any new client could skip
// the leg by picking the shorter name and nothing would say so. One name, leg
// always stated.
//
// A plan INCLUDES usage, bounded by nested windows, and prepaid credit is money
// bought separately. So a subscriber who has spent their included usage does not
// simply stop: they fall through to the credit leg and pay as they go, and are
// refused only if they have neither. That is the whole difference this leg makes
// — allowance first, then credit — and it is why a spent allowance does not
// return Unpaid directly.
//
// IT CAN ONLY EVER REMOVE AN ADMISSION, so every uncertainty admits. An absent
// reader, a plan that declares no windows, a ledger that did not answer: all
// AllowanceUnknown, all still Subscribed. Refusing a paying customer because a
// counter was unreadable is a worse failure than serving one request past a
// bound, and this codebase already takes that side everywhere else — "a balance
// that cannot be read is unknown, never zero".
func Stand(ctx context.Context, lic Licence, allow Allowance, w account.Account) Standing {
	if lic == LicenceActive && allow != AllowanceSpent {
		return Subscribed
	}
	creditOK, funded := creditIn(ctx, w)
	if creditOK && funded {
		return Funded
	}
	if creditOK && (lic == LicenceNone || allow == AllowanceSpent) {
		// Both authorities answered; both said no. This — and only this — is proof.
		//
		// A spent allowance counts as the subscription leg saying no, because it
		// IS the subscription answering: the plan includes usage, and that usage
		// is gone. Without this clause such a caller fell through to Unknown, and
		// Unknown ADMITS — so the windows would have been enforced only against
		// customers who had no subscription at all, which is nobody.
		return Unpaid
	}
	return Unknown
}

// creditIn reads the caller's prepaid balance from the native finance ledger and
// reports whether it is positive. ok is false when the ledger is not published
// (split deploy / money layer not co-resident), when the request carries no wallet,
// or when the read FAILS — "a balance that cannot be read is unknown, never zero"
// (clients/finance.Balance). A zero balance IS an answer: (true, false).
//
// The read is against the LIVE books (test=false). Sandbox money must never buy
// live product access.
func creditIn(ctx context.Context, w account.Account) (ok, funded bool) {
	if w.Zero() {
		return false, false
	}
	fin := finance.Current()
	if fin == nil {
		return false, false
	}
	bal, err := fin.Balance(ctx, w.Org(), w.Subject(), creditUnit, false)
	if err != nil {
		return false, false
	}
	return true, bal.Sign() > 0
}

// ── billable(path) — split OUT of price(path) ───────────────────────────────────

// Billable reports whether a request CONSUMES resource somebody has to pay a
// provider for, and therefore requires standing before it runs.
//
// THIS IS THE BUG THE SPLIT EXISTS TO KILL. Authorization and pricing were one
// int64: DefaultPrice returned the edge charge, and BillingGate read `cents <= 0` as
// "do not gate". Every LLM path prices at 0 there — deliberately, because the ai and
// zen subsystems meter their own token costs and an edge charge would double-bill —
// so "we charge nothing HERE" silently meant "we authorize NOTHING here", and the
// same fusion made every resource kind an operator prices at 0 un-gated as well
// (Meter.Authorize: `costCents <= 0 -> nil`). One int64 answering two questions is
// why the LLM leak and the non-LLM gap are a single bug. They are two questions now:
// Billable says WHETHER standing is required, DefaultPrice says WHAT the edge charges.
// Pricing something at zero can no longer un-authorize it.
//
// A READ IS BILLABLE ONLY WHERE THE READ IS THE PAID UNIT. GET/HEAD/OPTIONS
// otherwise pass unconditionally — gating reads already caused one outage, and a
// balance view that 402s is unusable. The exceptions are named in price.go's
// paidReads, beside the rule they are the exception to, because an operation that
// debits is not a read whichever verb it answers on.
//
// The path sets are MEASURED, not invented:
//   - inference — the exact paths zen's Claim owns (zen@v1.4.2 proxy.go) plus ai's
//     own tree. These are the free-inference hole.
//   - meteredTrees — meteredApps resolved through the manifest (the non-LLM
//     auth-not-balance gap). It must agree with the composition root: every surface
//     that declares cloud.Metered spends a provider's money and therefore needs
//     standing, and TestMeteredSurfacesRequireStanding fails if the two drift. The
//     exceptions are named there, not guessed here: commerce is the pay path itself
//     and o11y is telemetry ingest, so both charge nothing and declare cloud.Free.
func Billable(method, path string) bool {
	// Consumes (price.go) is the ONE place the read/write rule lives, so the
	// question "does standing apply" and the question "does the edge charge" can
	// never be answered from two copies of it that drift apart.
	if !Consumes(method, path) {
		return false
	}
	if Reachable(path) {
		return false // the path to payment is never gated. Ever.
	}
	if inference[path] {
		return true
	}
	for _, t := range meteredTrees {
		if under(path, t) {
			// Prefixes NEST, and the shorter one here may belong to a different
			// app than the one that actually serves this path. A bare HasPrefix
			// scan then bills the neighbour's surface on this app's standing —
			// a 402 in front of something nobody charges for. It shipped that
			// way: product's four Free reads sat under provisioning's metered
			// /v1/vector and /v1/search/query.
			//
			// The router resolves by longest prefix, so ownership does too. If the
			// app that really serves this path is not metered, nothing is spent and
			// nothing is owed.
			if owner := manifest.OwnerOf(path); owner != "" && !meteredSet[owner] {
				return false
			}
			return true
		}
	}
	return false
}

// meteredSet is meteredApps by name, for the ownership check above.
var meteredSet = func() map[string]bool {
	m := make(map[string]bool, len(meteredApps))
	for _, n := range meteredApps {
		m[n] = true
	}
	return m
}()

// inference is the exact set of bare completion endpoints. zen's Claim owns
// /v1/messages, /v1/chat/completions, /v1/chat and /v1/completions (zen proxy.go);
// /v1/embeddings and /v1/responses fall through to ai's catch-all. Both families
// call a paid upstream, so both need standing regardless of which one serves.
var inference = map[string]bool{
	"/v1/messages":         true,
	"/v1/chat/completions": true,
	"/v1/chat":             true,
	"/v1/completions":      true,
	"/v1/embeddings":       true,
	"/v1/responses":        true,
}

// meteredApps are the subsystems whose surface declares cloud.Metered: money moves
// inside their handlers, so a request to one requires standing. It is a set of
// NAMES, and that is the whole point — WHICH PATHS an app answers is the manifest's
// fact, read from there rather than restated here.
//
// It used to be the paths, and a hand-copied routing table is a routing table that
// goes stale. This one had, in four places, every one of them silently un-billable:
//
//   - provisioning's own tree was assumed to be /v1/provisioning. The manifest
//     routes it at /v1/{datastore,docdb,kv,s3,search,sql,vector} instead, so
//     vector, sql, kv, docdb, search and datastore creates — the EXACT set the
//     non-LLM billing gap was opened for — were not billable paths at all.
//   - projects answered /v1/sites and /v1/platform/sites beside /v1/projects; it
//     owns one prefix now, and the list is that prefix.
//   - venue answers /v1/cloud. It was absent entirely.
//   - tools answered /v1/skills, /v1/plugins and /v1/mcp/servers beside /v1/tools.
//     All three have since folded under /v1/tools, which is the other half of the
//     lesson: reading the manifest is what makes an address move cost nothing here.
//
// And ten Metered surfaces were missing outright (ask, auto, automations, content,
// flow, platform, provisioning, todo, translate, venue). The list had to be
// edited in lockstep with two other files and nothing checked that it was.
// TestMeteredSurfacesRequireStanding now reads Price straight out of every
// plugin/<name>/main.go and fails on a Metered surface missing from here — the check
// spend.go's own comment claimed for a test that did not exist.
var meteredApps = []string{
	// "agent" was here and named nothing: there is no plugin/agent and no
	// apps/agent — only the plural `agents`, which is the orchestrator and is
	// listed below. A name here that no surface answers to is not inert, it
	// gates: standing is required for a path nobody charges for, so a customer
	// is 402'd for free work. Removed rather than given a Price, because there
	// is no surface to price.
	//
	// "auto" was here once and named nothing, which is the same failure arriving by
	// a different road: an older apps/auto was deleted when the native backend
	// replaced the proxy it duplicated, the manifest kept routing /v1/auto to the
	// app then called automations, and the entry priced no surface while still
	// gating one — it cost nobody a charge and cost somebody a 402. The name is
	// back and it is the app's now, so the entry gates exactly the tree it prices.
	"agents",       // the per-run fee, and RUNTIME by the hour — an open session or a resident bot (apps/agents/meter.go).
	"ai",           // LLM token costs (ai self-meters).
	"ask",          // the answer engine's per-question fee.
	"auto",         // per-run automation fee.
	"cloudflare",   // Workers AI + provisioning.
	"code",         // /ask synthesizes and /search embeds, both through the metered AI client.
	"company",      // the $999 formation, gated and debited in providers.go; the genesis anchor rides inside it.
	"compliance",   // one identity inquiry opened at the vendor, on the deployment's key.
	"content",      // studio renders (GPU).
	"crawl",        // the browser render a thin page escalates to; the static fetch is free.
	"dataset",      // the scan that materialises a set, priced per source row read.
	"domain",       // registrations, renewals and transfers, at the registrar's price.
	"exec",         // one program run in a sandbox.
	"flow",         // flow executions.
	"functions",    // serverless invoke.
	"knowledge",    // a connector piece executed on the engine's pods; native-Go pulls are free.
	"lsp",          // code intelligence: a cold checkout+index is billed, a warm query is not.
	"meet",         // one seat on the media server; the lobby beside it is a free read.
	"ml",           // predict + train (compute).
	"platform",     // builds and runs (compute).
	"projects",     // site hosting fee.
	"provisioning", // sql/kv/vector/docdb/s3/search/datastore creates.
	"risk",         // per-screen fee inside each op.
	"s3",           // object-storage data plane.
	"sandbox",      // the lease, gated and debited around the pod (priced at zero), and the time it is HELD, at the agent-hour rate (apps/sandbox/meter.go).
	"security",     // scan fee.
	"share",        // one tunnel account provisioned on the fabric; reading it back is free.
	"space",        // the drive and file plane over the same object store; one operation, one fee.
	"seo",          // measurement resold at the vendor's own per-call price.
	"tel",          // numbers, messages and calls, at the carrier's price.
	"tools",        // per-tool dispatch.
	"todo",         // per-project/issue fee.
	"translate",    // per-character fee.
	"validator",    // one validator node materialized on the cluster, 200Gi, until deleted.
	"visor",        // GPU clusters (compute).
	"wallet",       // ring keygen, threshold signing and Safe proposals; KMS custody is free.
	"websearch",    // the bought engines (Brave, Mojeek API); the keyless ones are free.
	"zen",          // zen SKU token costs (zen self-meters).
}

// meteredTrees is meteredApps resolved through the fleet's ONE routing table, at
// init. Each entry is a root WITH its trailing slash; Billable matches the bare root
// too.
//
// The union of the manifest's declared prefixes and the /v1/<name> convention is
// deliberate. They answer different questions — the manifest names the paths the
// light host ROUTES to an app, the convention names the tree the app OWNS — and a
// surface answers both: ml is routed /v1/ml/models and also owns
// /v1/ml/models/{name}/predict, which appears in neither list alone. A union can only widen coverage, and this
// gate's asymmetry is that gating too little is a leak while gating too much is an
// outage only for paths that are NOT metered — which a union over metered apps
// cannot reach.
//
// A bare "/v1" is the one prefix skipped. ai carries it as the router's TERMINAL
// catch-all (the last rows of manifest.Apps), not as a claim to own every path;
// honouring it would make every /v1 request billable and put a 402 in front of
// notify, crm and tasks the moment enforcement is switched on. ai's real surface is
// /v1/ai, which the convention supplies, and the bare completion endpoints it shares
// with zen are the `inference` set above.
var meteredTrees = meteredPrefixes()

func meteredPrefixes() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(meteredApps)*2)
	add := func(p string) {
		root := strings.TrimSuffix(p, "/")
		if root == "" || root == "/v1" {
			return
		}
		if root += "/"; !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	for _, name := range meteredApps {
		add("/v1/" + name)                             // the tree the app OWNS.
		for _, p := range manifest.PrefixesFor(name) { // the paths the host ROUTES to it.
			add(p)
		}
	}
	return out
}

// ── the invariant: the path to payment is never gated ───────────────────────────

// Reachable reports whether a path must be served REGARDLESS of standing.
//
// This is not a scope selector — which routes a gate guards is decided by where it is
// applied. It is a SAFETY PROPERTY: a customer who has not yet paid must still be
// able to reach the paths that let them pay, and a lapsed one must be able to cure
// their own lapse. Gate those and you deadlock every prospect and every lapsed
// customer at once — a self-inflicted total revenue stop that looks like success in a
// naive test, because everything returns 402. So the list lives INSIDE the predicate:
// no future wiring mistake can gate the pay path, whatever it wraps.
//
// Gating too little is a revenue leak we fix next week. Gating too much is an outage.
// The list is therefore deliberately generous, and anything ambiguous belongs on it.
//
// Traced from the console subscribe flow (console src/components/products/
// PlansModule.tsx -> src/lib/api/plans.ts): PlansApi.plans() reads /v1/billing/plans
// through the per-tenant billing proxy, and checkout drives the /v1/billing/* money
// surface — which also carries the INBOUND PROVIDER WEBHOOKS at
// /v1/billing/webhooks/:provider. Gating an inbound payment webhook loses payments
// outright, so that prefix is load-bearing twice over.
func Reachable(path string) bool {
	// Compare what the ROUTER matched, not what the client typed. Every rule below
	// is a lowercase literal, and fiber serves /V1/CODE/ASK from the route registered
	// at /v1/code/ask — so an un-normalised path fell past the /v1 test into "the SPA
	// shell", and one capital letter exempted a metered operation from the whole
	// money rule. RoutePath is this package's one normalisation (risk.go); `under`
	// below folds through it too, so the two halves of this function agree.
	path = RoutePath(path)
	// Liveness / readiness / metrics — a gate must never hide whether the process is
	// up, or an incident becomes invisible at exactly the wrong moment.
	switch path {
	case "/health", "/healthz", "/readyz", "/livez", "/metrics":
		return true
	}
	if strings.HasSuffix(path, "/health") {
		return true // the per-subsystem HIP-0106 probe, /v1/<name>/health.
	}
	if !strings.HasPrefix(path, "/v1/") {
		return true // the SPA shell + static assets that render the paywall screen itself.
	}
	// The auth surface, under EVERY spelling it has ever answered on. This list
	// matches by STRING and does NOT follow a route, so a spelling that drifts out
	// of the list puts a 402 in front of SIGN-IN — the outage the trailing-slash and
	// casing bugs already caused twice.
	//
	// It said these had "moved" to /v1/ai/*. They had not: production answers
	// /v1/signin, /v1/signout and /v1/get-account, while /v1/ai/signin,
	// /v1/ai/signout and /v1/ai/account are all 404. So the list exempted three
	// paths that do not exist and gated the three that do — flipping the enforce
	// flag would have locked every unpaid user out of logging in, which is exactly
	// the failure the comment warned about, pointed the wrong way.
	//
	// Both spellings stay listed. This file's own rule is that the list is
	// deliberately generous and anything ambiguous belongs on it: gating too little
	// is a revenue leak, gating sign-in is an outage. Every case below is pinned by
	// apps/entitlement.TestPayPathStaysReachable, which sends each one through the
	// enforcing gate and requires a 200.
	switch path {
	case "/v1/signin", // auth: session bootstrap (the console posts the OAuth code here).
		"/v1/signout",
		"/v1/get-account", // auth: the account read AuthGate loads before anything else.
		"/v1/ai/signin",   // the namespaced spellings, kept so a future move cannot regress this.
		"/v1/ai/signout",
		"/v1/ai/account",
		"/v1/entitlement": // the paywall's OWN projection — what the shell renders upgrade UI from.
		return true
	}
	for _, sub := range reachableTrees {
		// A sub-tree covers its own ROOT as well as everything under it, and the
		// comparison is scope.go's `under` — the one this package already trusts to
		// mean "inside my subtree", folded through RoutePath. Matching only "<root>/"
		// is how a paywall ends up refusing the exact URL its own 402 points at; a raw
		// prefix test is how one capital letter walks past a gate. One rule, one
		// function, every form.
		if under(path, sub) {
			return true
		}
	}
	return false
}

// reachableTrees are the /v1 sub-trees a spend gate may never refuse. Every entry is
// written as a root WITH its trailing slash and covers the bare root too.
//
// /v1/account/ is one entry where it used to be two: the org create and switch the
// shell resolves before it knows which org it is buying for answered at a bare
// /v1/orgs, and folding it under the capability that serves it brought it inside a
// tree this list already had.
//
// The SuperAdmin plan and catalog CMS is inside /v1/commerce/ for the same reason,
// and it got there the hard way. It was exempt under a "/v1/plans/" entry; the plan
// capability then took /v1/plan, that entry followed it, and the CMS rows — still at
// /v1/plans/{entries,seed} — were left outside every tree here, one flag flip away
// from a 402 on the surface an operator prices the catalog from. The rows now answer
// under commerce's own root, which this list already had, so the exemption is a
// property of where they live rather than a second entry to keep in step.
var reachableTrees = []string{
	"/v1/billing/",     // the whole money surface: subscribe, top up, AND the inbound provider webhooks.
	"/v1/commerce/",    // the checkout, the tenant reads, AND the SuperAdmin plan + catalog CMS.
	"/v1/iam/",         // IAM login / OAuth token exchange / .well-known OIDC discovery.
	"/v1/account/",     // keys, csrf, appearance, avatar, embed and the org create + switch.
	"/v1/admin/",       // platform sudo — including the cockpit that holds this gate's kill switch.
	"/v1/plan/",        // the plan catalog — WHAT to buy (@hanzo/plans) — and its sub-routes.
	"/v1/models/",      // the model catalog the shell reads for discovery, and /v1/models/:id.
	"/v1/waitlist/",    // admission's join API — an un-admitted user must still reach it.
	"/v1/flags/",       // the guard's public mode read; also how the kill switch is observed.
	"/v1/entitlement/", // per-org enablement reads/writes that sit beside the projection.
}

// ── the allowance leg ───────────────────────────────────────────────────────────

// Allowance is what a subscription's own usage windows say about one caller,
// already resolved. The zero value is AllowanceUnknown, which is the honest
// default and the one that matters most here: this leg can only ever REMOVE a
// subscriber's admission, so a question that could not be asked must never be
// read as an answer.
type Allowance uint8

const (
	// AllowanceUnknown — nothing could be determined: the reader is absent, the
	// plan declares no windows, the ledger did not answer. NOT a statement about
	// the caller, and admits.
	AllowanceUnknown Allowance = iota
	// AllowanceWithin — the authority answered: usage is inside every window.
	AllowanceWithin
	// AllowanceSpent — the authority answered: at least one window is exhausted.
	AllowanceSpent
)

// AllowanceChecker reports whether a subscriber is still inside the usage their
// plan includes. It is an OPTIONAL capability resolved by type assertion, the way
// PlanChecker is: a deployment whose commerce cannot answer simply does not
// implement it, and the gate behaves exactly as it did before this existed.
type AllowanceChecker interface {
	// WithinAllowance reports (spent, ok). ok=false means the question could not
	// be answered and the caller must not read spent.
	WithinAllowance(ctx context.Context, org, account string) (spent bool, ok bool)
}

// AllowanceIn resolves the allowance leg — the twin of the spend gate's own
// licence(), exported because apps/entitlement runs the same ladder and a second
// copy of this is how the two come to disagree about what a spent plan means.
//
// Every failure is AllowanceUnknown — absent reader, empty address, a reader that
// could not answer — because this leg may only ever REMOVE an admission.
func AllowanceIn(ctx context.Context, a AllowanceChecker, w account.Account) Allowance {
	if a == nil || w.Zero() {
		return AllowanceUnknown
	}
	spent, ok := a.WithinAllowance(ctx, w.Org(), w.Subject())
	if !ok {
		return AllowanceUnknown
	}
	if spent {
		return AllowanceSpent
	}
	return AllowanceWithin
}
