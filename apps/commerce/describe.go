// Copyright © 2026 Hanzo AI. MIT License.

// describe.go is commerce's PROSE. Every operation this subsystem serves is
// registered by the embedded hanzoai/commerce module — its own route tables, its
// own handlers, in another module — so there is no doc comment in this repo for
// zipdoc to lift and no typed op to lift it from. Left bare, all 75 published an
// operationId and NOTHING else: an SDK method that cannot explain itself, an MCP
// tool a model cannot pick, a CLI command with no help text.
//
// openapi.Describe is the seam for exactly that operation. It carries the same
// drift-proof property Register has — a description whose route the router does
// not carry never renders — so this file cannot add an operation, only explain
// one that exists. The key is the fiber pattern VERBATIM, which is how the
// projector addresses a live route (openapi/openapi.go From).
//
// The prose is written from the handlers, not from the paths: each summary is
// what the caller GETS, and each description states the gate, the tenant scope,
// what it fails closed on, and the one rule a reader would otherwise get wrong.
// That holds for the derived families at the bottom of this file too — they are
// derived from the ROUTE TABLE'S OWN gate wiring, so a sentence is generated
// rather than repeated, and the generator is still reading the handler.
package commerce

import (
	"net/http"

	"github.com/hanzoai/cloud/openapi"
)

func init() {
	describeAdmin()
	describeBilling()
	describeCatalog()
	describePublic()
	describePlans()
	describeStore()
	describeCheckout()
	describeResources()
}

// ---- /_/commerce — the operator surface the ingress withholds publicly ----

// The health probe is deliberately absent here: it is a typed op (mount.go),
// so its prose is the doc comment zipdoc lifts, and a Describe beside it would
// be a second prose source for one route.
func describeAdmin() {
	openapi.Describe("/_/commerce/providers", http.MethodGet,
		"List the payment providers configured for your own tenant",
		"Returns the caller's own tenant row projected to a public view with the KMS paths "+
			"stripped, so a provider's name and enabled flag are visible and its credential "+
			"location never is. The tenant is derived from the IAM owner claim and from nothing "+
			"else — there is no tenant parameter to supply, so a cross-tenant read is not "+
			"expressible. A tenant admin or a platform admin may call it; a plain authenticated "+
			"user is refused 403 and an anonymous one 401. A caller whose owner claim has no "+
			"tenant row gets a 404 byte-identical to the one a cross-tenant probe would get.")

	openapi.Describe("/_/commerce/providers/:name", http.MethodPut,
		"Turn one payment rail on or off for your own tenant",
		"Flips the enabled flag on the named provider in the caller's own tenant row, so a rail "+
			"can be taken out of service — or put back — without touching its credentials. The "+
			"KMS paths are never read, written or echoed here; this verb owns exactly one bit.\n\n"+
			"Disabling is what a checkout page sees immediately: only ENABLED providers are listed "+
			"by the public tenant read, so a rail turned off here stops being offered rather than "+
			"failing at authorization time. Re-enabling restores the same stored credential, which "+
			"is why this is a switch and not a delete.\n\n"+
			"The tenant is derived from the IAM owner claim and from nothing else — there is no "+
			"tenant parameter to supply, so a cross-tenant write is not expressible. A tenant "+
			"admin or a platform admin may call it; a plain authenticated user is refused 403 and "+
			"an anonymous one 401. A provider name with no row on that tenant is 404, the same "+
			"answer a cross-tenant probe gets.")

	openapi.Describe("/_/commerce/tenants", http.MethodPost,
		"Create a checkout tenant: hostnames, brand, IAM, IDV, providers and backend",
		"Registers a new hosted-checkout tenant so its hostnames resolve to their own branding, "+
			"identity config, payment providers and broker backend. PLATFORM admin only — the "+
			"reserved admin org's owner claim; an org owner with the org-level admin bit is "+
			"refused 403 and an anonymous caller 401, so a tenant can never be minted from inside "+
			"a tenant. A duplicate name is 409 and a malformed hostname 400. The response echoes "+
			"only the identity and timestamps, never the provider records the caller just sent, "+
			"and the mutation is audited by hash rather than by content so a credential that slips "+
			"into the body is not replayable from the log.")
}

// ---- /v1/billing — the console's money surface ----

func describeBilling() {
	openapi.Describe("/v1/billing/recharge/run-all", http.MethodPost,
		"Platform sweep: top up every org whose balance has fallen below its own threshold",
		"Walks every organization and, for those that enabled auto-recharge and whose available "+
			"balance (balance minus holds) has fallen under their configured threshold, charges "+
			"their default payment method off-session and credits the balance, answering a "+
			"per-org result row for each one it touched. This is the platform cron's door, not a "+
			"customer's: it is gated on the internal service token AND platform scope, so an org "+
			"admin cannot run the fleet-wide sweep. An org above its threshold is skipped "+
			"silently; an org with no default payment method is reported as an uncharged row with "+
			"the reason rather than failing the whole run.")

	openapi.Describe("/v1/billing/invoices", http.MethodGet,
		"List your org's billing invoices",
		"Returns the caller org's invoices with a count, read from that org's own namespaced "+
			"store, narrowable by userId, status or subscriptionId. The org is the one the "+
			"gateway validated and the caller's billing subject is pinned into the query before "+
			"the handler runs, so a read can never widen past the caller. A request that carries "+
			"no resolvable org gets an honest empty list rather than an error or another tenant's "+
			"rows.")

	openapi.Describe("/v1/billing/invoices/:id/pdf", http.MethodGet,
		"Download one invoice as a PDF attachment",
		"Renders the addressed invoice as a single-page PDF and answers it as an attachment named "+
			"after the invoice number. The render is a pure function of the invoice — no "+
			"timestamps, no random ids — so the same invoice always produces identical bytes and "+
			"a re-download is stable. The invoice is resolved inside the caller org's own "+
			"namespace, so an id belonging to another tenant is simply absent and reads as 404; a "+
			"caller with no validated org gets 401 rather than a document.")

	openapi.Describe("/v1/billing/settings", http.MethodGet,
		"The public payment-provider config your card form needs to initialize",
		"Answers the Square application id, location id, environment and live flag the browser's "+
			"card iframe boots against — public values only, never a secret. Resolution lives in "+
			"one place shared with the public tenant projection, so the card form can never "+
			"initialize against a different Square application than the one commerce will "+
			"actually charge. It deliberately does NOT hydrate credentials from KMS: the dialog "+
			"blocks on this call, so it answers from the org and the deployment environment "+
			"without a round trip, and an org with no per-org credentials gets the deployment's "+
			"own public app id.")

	openapi.Describe("/v1/billing/credits", http.MethodGet,
		"List the credit grants on your org's balance",
		"Returns the caller org's credit grants — each with its original amount, what remains "+
			"and when it expires — so a customer can see what was given and what is left before "+
			"metered spend draws it down. It is a READ of the caller's own subject, pinned before "+
			"the handler runs, so a grant belonging to another tenant is simply absent. Granting "+
			"credit is not this route and never has been: minting lands on the mint-gated POST "+
			"/v1/billing/credit, which no browser can reach. Reading an empty balance is an empty "+
			"array, not an error.")

	openapi.Describe("/v1/billing/payouts", http.MethodGet,
		"List your org's payouts, newest first",
		"Returns the caller org's payout records ordered by creation time descending, read from "+
			"that org's own namespaced store. The org is the gateway-validated one and the "+
			"caller's billing subject is pinned before the handler runs, so the list is the "+
			"caller's own and cannot be widened. A request with no resolvable org gets an empty "+
			"array rather than an error.")

	openapi.Describe("/v1/billing/plans", http.MethodGet,
		"The public plan catalog, annotated with the active platform promotion",
		"Returns every subscription tier a buyer can choose, each carrying the platform promo "+
			"currently in effect, optionally narrowed with the category query. Prices come from "+
			"the admin-editable plan authority in the database; the embedded catalog is only a "+
			"loud-failing fallback, so a failed seed or a query error serves the known plans "+
			"rather than a silently blank list. It is a catalog read, not an entitlement read — "+
			"it says what may be bought, never what this caller has.")

	openapi.Describe("/v1/billing/tier", http.MethodGet,
		"The subject's plan tier and the balance a metered call is admitted on",
		"Answers one subject's resolved tier — name, display name, agent ceiling and allowed "+
			"models — with the balance that admits their next metered call: prepaidAvailable, "+
			"creditsRemaining, dailyRemaining and the effectiveAvailable those fold into. The ai "+
			"router reads it per request to pick that caller's rate-limit tier. It sits on the "+
			"org-resolving chain because a tier is org state, and the subject keys are pinned to "+
			"the validated caller before the handler runs, so a browser read is always the "+
			"caller's own; user is required, which only a service-to-service caller can omit and "+
			"be refused 400 for. The tier is an upstream tier claim, or an explicit tier "+
			"override, when either is present — that is the service-to-service contract — and is "+
			"otherwise DERIVED from the org's active and trialing subscriptions, the highest one "+
			"winning, its paid-ness read from the plan catalog by slug rather than from the "+
			"subscription's own stored copy. The rule to get right is effectiveAvailable and not "+
			"prepaidAvailable: granted credits spend too, credits first, so an account funded "+
			"only by a grant reads zero prepaid while holding real spendable credit — and with "+
			"the daily term zero on every tier there is no free allowance behind it, so a "+
			"zero-balance account is gated. A subscription-store error answers 500 rather than "+
			"downgrading to free, so a transient failure never reports a paid subscriber as "+
			"unsubscribed.")

	openapi.Describe("/v1/billing/portal/methods", http.MethodGet,
		"Cards saved against the caller's org, masked — the portal read",
		"Answers the org's saved payment methods as masked descriptors: brand, last four, expiry "+
			"and the processor's reusable reference. No card number and no security code exist "+
			"here to return; both live at the processor and never enter this system.\n\n"+
			"This is the SERVICE-TOKEN face of the same list a customer reads at "+
			"/v1/billing/methods. Both are served here, in this process, and answer the same "+
			"rows; they are two addresses because they admit two different principals, not "+
			"because either forwards to the other.\n\n"+
			"The customer filter is pinned to the VALIDATED caller before the handler runs, so a "+
			"browser sees only its own subject's cards whatever customerId it sends; only a "+
			"caller holding the internal service token may name the subject, and the org it may "+
			"name it within is fixed by the gateway. Cross-tenant is closed by the org namespace "+
			"for both, so an id or a subject from another org resolves to nothing. A caller who "+
			"is neither is refused before the read.")

	openapi.Describe("/v1/billing/portal/methods/:id", http.MethodDelete,
		"Remove a saved card — the portal detach",
		"Detaches the addressed card: the stored reference is removed here AND withdrawn from "+
			"the processor's vault, so nothing is left that a later charge could bill.\n\n"+
			"The service-token twin of the customer's DELETE /v1/billing/methods/{id}, at its own "+
			"address for the same reason the portal list is — a different principal, on the same "+
			"rows, in this same process.\n\n"+
			"The id is resolved INSIDE the caller's org namespace, so another tenant's card is "+
			"not found there and answers 404 — never 403, which would confirm the id exists. "+
			"That bound holds for the service token too: it may act for any subject within the "+
			"org the gateway pinned, and for no subject outside it.\n\n"+
			"Removing the card an auto-recharge or a running lease bills leaves that arrangement "+
			"with nothing to charge; that is the customer's call to make.")

	// ---- the CUSTOMER face of the same three cards ----
	//
	// One family of rows, two doors, both served in THIS process: /v1/billing/*
	// admits a signed-in browser (IAM identity), /v1/billing/portal/* admits the
	// internal service token acting for a subject. Neither forwards to the other,
	// and the prose on each pair must keep saying so — an earlier round published
	// the portal's sentences verbatim at the customer address, where "the
	// SERVICE-TOKEN face of the list a customer reads at /v1/billing/methods"
	// read as a description of itself.

	openapi.Describe("/v1/billing/methods", http.MethodGet,
		"Your saved cards, masked — the customer read",
		"Answers the cards saved against your own account as masked descriptors: brand, last "+
			"four, expiry and the processor's reusable reference. No card number and no security "+
			"code exist here to return; both live at the processor and never enter this system. "+
			"It is what a checkout prefills its payment step from.\n\n"+
			"The customer face of the list a service token reads at /v1/billing/portal/methods — "+
			"same rows, different principal, no hop between them.\n\n"+
			"The subject filter is pinned to the VALIDATED caller before the handler runs, so the "+
			"answer is your own account's cards whatever customerId the request carries, and "+
			"another org's rows are outside the namespace entirely. A caller who is not signed in "+
			"is refused before the read.")

	openapi.Describe("/v1/billing/methods", http.MethodPost,
		"Save a card for later charges",
		"Vaults the card the processor already holds — you send its one-time reference, never a "+
			"card number — as a reusable card on file, and stores the billing address with it. "+
			"That vaulted card is what a subscription renewal or an auto-recharge charges later, "+
			"which is why saving one is the step that makes a monthly plan billable at all.\n\n"+
			"It charges nothing. Saving a card moves no money; the first charge is whatever "+
			"arrangement you then attach it to.\n\n"+
			"The subject is pinned from the validated caller and OVERWRITES the customerId in the "+
			"body while leaving the card fields untouched, so a card can only ever be attached to "+
			"the caller's OWN account whatever the body claims. That pin is the whole control on "+
			"this write, not decoration: this is the one handler in the family that reads its "+
			"subject from the body.")

	openapi.Describe("/v1/billing/methods/:id", http.MethodDelete,
		"Remove one of your saved cards",
		"Detaches the addressed card: the stored reference is removed here AND withdrawn from "+
			"the processor's vault, so nothing is left that a later charge could bill.\n\n"+
			"The customer twin of DELETE /v1/billing/portal/methods/{id}. The id is resolved "+
			"INSIDE your own org namespace, so a card that is not yours is simply not found "+
			"there and answers 404 — never 403, which would confirm the id exists.\n\n"+
			"Removing the card an auto-recharge or a running lease bills leaves that arrangement "+
			"with nothing to charge; that is yours to decide.")

	openapi.Describe("/v1/billing/portal/methods", http.MethodPost,
		"Save a card on a subject's behalf — the portal attach",
		"The service-token twin of POST /v1/billing/methods: it vaults the processor's one-time "+
			"reference as a reusable card on file for the named subject, with its billing "+
			"address, and moves no money doing it.\n\n"+
			"It exists so an internal caller can complete the family it can already read and "+
			"detach. The subject it may name is pinned to the org the gateway fixed, so the "+
			"service token acts WITHIN one tenant and never across tenants; a caller holding no "+
			"service token is refused before the write.")

	// ---- the top-up rails: how money gets IN, other than a card ----

	openapi.Describe("/v1/billing/wire", http.MethodGet,
		"Where to wire funds, and the reference that credits them to you",
		"Answers the receiving bank details for the brand this deployment serves — the account "+
			"the funds actually land in, hydrated per brand rather than hard-coded — together "+
			"with the payment reference to put on the transfer.\n\n"+
			"THE REFERENCE IS THE POINT. It carries your own billing key, and it is how an "+
			"arriving wire is attributed to your account; a transfer sent without it arrives as "+
			"an unidentified receipt. That is why this read is gated at all: an unpinned caller "+
			"would be handed an unattributable reference.\n\n"+
			"Reading it credits nothing and reserves nothing. A wire is settled by an operator "+
			"when the bank shows the funds, so the balance moves on receipt, not on this call.")

	openapi.Describe("/v1/billing/crypto/options", http.MethodGet,
		"Which chains and tokens a crypto top-up can use",
		"Answers the custody processor's LIVE capability list — the chains and the tokens on "+
			"each that this deployment can actually take a deposit on. A payment page renders its "+
			"asset picker straight from it rather than from a list of its own, so a chain the "+
			"processor stops supporting disappears from the picker instead of minting an address "+
			"nothing watches.\n\n"+
			"It is a capability read, not an account read: it says what may be paid with, never "+
			"anything about this caller's balance or deposits.")

	openapi.Describe("/v1/billing/crypto/deposit", http.MethodPost,
		"Get a deposit address for a crypto top-up",
		"Mints a deposit address held by the MPC signer fleet — no single party holds the key — "+
			"on the chain and token you name, and returns it with the intent that tracks it.\n\n"+
			"The account credited is the PINNED caller's, never a value in the body, so a deposit "+
			"cannot be aimed at someone else's balance. A caller who already has an open intent "+
			"gets that same address back rather than a new one, so reloading the page cannot "+
			"spray keygens across the signer fleet.\n\n"+
			"NO BALANCE MOVES HERE. This hands out an address; the chain watcher credits the "+
			"account when a real transfer confirms, which is also why an address handed out and "+
			"never funded costs nothing and expires nothing.")

	openapi.Describe("/v1/billing/crypto/deposit/:id", http.MethodGet,
		"Follow one crypto deposit to settlement",
		"Answers the addressed deposit intent's current state — pending until a transfer is "+
			"seen, confirming while the chain buries it, succeeded once it is credited — so a "+
			"payment page can poll one deposit rather than the whole balance.\n\n"+
			"Scoped to the caller: an intent belonging to another payer is not found and answers "+
			"404, never another account's state. The credit itself is the chain watcher's to "+
			"make; this read reports it and never performs it.")

	openapi.Describe("/v1/billing/alerts", http.MethodGet,
		"List your org's spend caps and rate limits",
		"Returns the caps and alerts keyed to the caller's own billing subject, each with its "+
			"threshold, enforcement flag, soft-warning percentage and current period spend. Any "+
			"authenticated member of the org may read them — only the writes require an admin. "+
			"The rows are keyed on the org subject the enforcement gate itself reads, which is "+
			"why a cap created here is the one that actually binds. A caller with no resolvable "+
			"org or subject gets an empty list, never another tenant's caps.")

	openapi.Describe("/v1/billing/alerts", http.MethodPost,
		"Set a spend cap or rate limit on your org",
		"Creates a cap for the caller's own org and answers the stored row with its current "+
			"period spend. A spend cap is a FINANCIAL SAFETY control, so writing one requires an "+
			"ORG ADMIN, a platform admin, or the internal service token — a plain authenticated "+
			"member is refused 403, because a member who could delete the cap could uncap the "+
			"org's spend and a member who could set a one-cent enforcing cap could deny the whole "+
			"org. The cap is always keyed to the caller's own billing subject: a userId in the "+
			"body is overwritten, never honored, so a cap cannot be planted on another subject. "+
			"At least one of a positive threshold or a positive rateLimitRpm is required, softPct "+
			"must be within 0 to 100, and an org that has reached its row limit is refused 400.")

	openapi.Describe("/v1/billing/alerts/authorize", http.MethodGet,
		"The per-request spend-cap verdict the metering gate consumes",
		"Answers allow, reason, capCents, spentCents and warnPct for a proposed amount against a "+
			"(project, service) scope — the verdict the request-edge metering gate reads before "+
			"admitting a call. It evaluates EVERY covering cap and the most restrictive enforcing "+
			"one wins; soft caps and an enforcing project cap whose project axis is not validated "+
			"never block, they only raise the warning utilization. It is a service-to-service "+
			"read authenticated by the internal service token with the org pinned by the gateway, "+
			"not a browser call. Two rules matter: the spend it scores comes from the finance "+
			"ledger's current-month total, and it FAILS OPEN on unknown spend — a transient read "+
			"failure allows rather than denies, so a backend blip never bills-blocks an under-cap "+
			"customer, while a known overage still denies.")

	openapi.Describe("/v1/billing/alerts/:id", http.MethodDelete,
		"Remove one of your org's spend caps",
		"Deletes the addressed cap and answers 204. Requires an ORG ADMIN, a platform admin, or "+
			"the internal service token — deleting a cap uncaps the org's spend, so a plain "+
			"member is refused 403. Ownership is checked per row and a cap the caller does not "+
			"own is refused as 404 rather than 403, so the response cannot confirm that another "+
			"org's id exists.")

	openapi.Describe("/v1/billing/alerts/:id", http.MethodPatch,
		"Change one of your org's spend caps",
		"Applies only the fields the body actually carries — title, threshold, project, service, "+
			"enforce, softPct, rateLimitRpm — and leaves the rest as stored, answering the merged "+
			"row with its current period spend. Requires an ORG ADMIN, a platform admin, or the "+
			"internal service token, for the same reason creation does: a member who could edit "+
			"the cap could raise it to nothing or drop it to a punitive floor. Ownership is "+
			"checked per row and a cap the caller does not own is refused as 404, never 403, so "+
			"the id space cannot be probed.")

	openapi.Describe("/v1/billing/subscribe/card", http.MethodPost,
		"Subscribe to a paid plan with a card, charged for the first period immediately",
		"Vaults the tokenized card as a reusable card-on-file, charges the first period, and "+
			"creates the subscription — answering the subscription and invoice ids with the "+
			"amount charged. The price is SERVER-AUTHORITATIVE: it is the plan's catalog price "+
			"times billable seats and a client-supplied amount is never consulted, so a scripted "+
			"request cannot underpay; a per-seat plan below its minimum seats is refused, and a "+
			"free plan is refused outright because this address is the paid path. The card PAN "+
			"never reaches this service — the browser tokenizes it and only the single-use nonce "+
			"arrives here. The subject is the caller's own org, with an in-org user honored only "+
			"inside that bound, and an idempotency key (or, absent one, the nonce itself) makes a "+
			"retry replay the first result instead of charging twice.")

	openapi.Describe("/v1/billing/subscriptions", http.MethodGet,
		"List your org's subscriptions",
		"Returns the caller org's subscriptions with a count, narrowable by userId or status, "+
			"read from that org's own namespaced store. The org is the gateway-validated one and "+
			"the caller's billing subject is pinned before the handler runs. A request with no "+
			"resolvable org gets an empty list and a zero count rather than an error.")

	openapi.Describe("/v1/billing/subscriptions/:id/cancel", http.MethodPost,
		"Cancel a subscription, at period end by default",
		"Cancels the addressed subscription and answers its updated state, emitting the "+
			"cancellation event the rest of the platform keys on. The default is to cancel AT "+
			"PERIOD END — a body that fails to parse falls back to it — so the customer keeps "+
			"what they paid for unless atPeriodEnd is explicitly false. The subscription is "+
			"resolved inside the caller's own org namespace, so another tenant's id is a 404, and "+
			"the write carries the browser anti-CSRF gate because it is reachable with an ambient "+
			"cookie.")

	openapi.Describe("/v1/billing/subscriptions/:id/reactivate", http.MethodPost,
		"Undo a pending cancellation and keep the subscription running",
		"Clears the scheduled cancellation on the addressed subscription and answers its updated "+
			"state. It is the inverse of cancel and applies to a subscription that is still "+
			"within its period; one the engine will not reactivate is refused 400 with the "+
			"reason. The subscription is resolved inside the caller's own org namespace, so "+
			"another tenant's id reads as 404, and the write carries the browser anti-CSRF gate.")

	openapi.Describe("/v1/billing/mode", http.MethodPost,
		"Move an org between sandbox and live billing",
		"Flips the org's live flag, which is the single authority for both the payment "+
			"environment and the ledger bucket its transactions land in. This is a money-MINT "+
			"control, not a customer action: it is gated on the internal service token AND "+
			"platform scope, so an ORG ADMIN CANNOT move their own org — otherwise a tenant could "+
			"drop itself into sandbox and stop paying. The rule most callers get wrong is the "+
			"default: an org that has never been flipped transacts in SANDBOX, which is why a "+
			"production-credentialled deployment can still hand a buyer a sandbox card form. When "+
			"the deployment pins the payment environment explicitly, that pin governs and this "+
			"flag only marks the transactions.")

	openapi.Describe("/v1/billing/topup/token", http.MethodPost,
		"Add credit to your balance by charging a tokenized card once",
		"Charges the single-use card token for the given amount and credits the caller's own "+
			"balance, answering the transaction id and the new balance — the one-time top-up "+
			"path, with no payment method saved. The amount is bounded SERVER-SIDE (roughly a one "+
			"dollar floor and a five thousand dollar ceiling by deployment policy) and the check "+
			"runs before any money moves, because the browser cap is not a control against a "+
			"scripted request. The credit lands on the caller's OWN billing subject — the same "+
			"key the usage gate debits — and can never be redirected outside the caller's org. "+
			"Retries are safe: an idempotency key, or absent one the amount within a short "+
			"window, replays the first result, and if that guard store is unreachable the call is "+
			"refused with 503 rather than risking a second real charge.")

	openapi.Describe("/v1/billing/webhooks/:provider", http.MethodPost,
		"Payment-provider webhook intake for settlement and subscription lifecycle events",
		"Accepts a payment provider's event, verifies it, records it for audit, and applies "+
			"subscription lifecycle changes to the matching local row. There is no bearer here "+
			"and there cannot be: the provider's SIGNATURE over the body IS the authentication, "+
			"so a request with no recognized signature header is 400 and one whose signature does "+
			"not verify is 401. The provider path segment is only a hint for dashboard "+
			"configuration — verification picks the processor regardless of what the URL says. "+
			"Redelivery is safe: an event id already recorded is acknowledged as a duplicate "+
			"without re-applying any side effect, which matters because providers retry for days "+
			"until they see a 2xx.")
}

// ---- /v1/catalog — the platform-admin product CMS ----

func describeCatalog() {
	openapi.Describe("/v1/catalog/entries", http.MethodGet,
		"The raw catalog entries, including the unpublished ones",
		"Returns every catalog row as stored — the admin view, which unlike the public "+
			"projection includes entries that are not published. It is cross-tenant platform "+
			"data, so the gate is a PLATFORM admin: an org-level admin is refused 403 no matter "+
			"how privileged they are inside their own org, enforced by the handler itself and not "+
			"only by the route's token middleware.")

	openapi.Describe("/v1/catalog/entries", http.MethodPost,
		"Add a catalog entry",
		"Creates a catalog row from the body and answers it at 201. The slug is required and is "+
			"the globally-unique catalog key, so a second entry claiming a slug already in use is "+
			"refused 409 rather than shadowing the first. PLATFORM admin only — this is "+
			"cross-tenant pricing and packaging data, and an org-level admin is refused 403.")

	openapi.Describe("/v1/catalog/entries/*", http.MethodPut,
		"Replace a catalog entry, keeping its slug",
		"Loads the addressed entry, applies the body over it and answers the stored result. The "+
			"slug is the entry's IDENTITY and is re-stamped from the path after decoding, so a "+
			"slug in the body is ignored and a rename is impossible through this address. The "+
			"slug is matched as a trailing wildcard rather than one path segment because a "+
			"model's slug IS its callable id and those contain a slash — a segment parameter "+
			"would stop at it and leave most catalog rows unaddressable. PLATFORM admin only; an "+
			"unknown slug is 404.")

	openapi.Describe("/v1/catalog/entries/*", http.MethodDelete,
		"Remove a catalog entry",
		"Deletes the entry with the addressed slug and answers 204. The slug is matched as a "+
			"trailing wildcard, not a single segment, because a model slug contains a slash. "+
			"PLATFORM admin only — an org-level admin is refused 403 — and an unknown slug is "+
			"404, so the call is safe to repeat but not silently idempotent.")

	openapi.Describe("/v1/catalog/models", http.MethodPost,
		"Land a syncer's view of the model catalog: upstream costs and machine facts",
		"Takes a batch of model rows and upserts each one's upstream COST and machine-observable "+
			"facts, answering what was created and changed. It deliberately touches nothing a "+
			"human owns — not the retail price, not the markup, not the entitlement tier — so a "+
			"sync can never overwrite an administrator's pricing decision. The gate is a PLATFORM "+
			"principal rather than a platform ADMIN, because the caller is normally a scheduled "+
			"job holding the internal service token, which carries platform scope but no admin "+
			"claim.")

	openapi.Describe("/v1/catalog/models/refresh", http.MethodPost,
		"Refresh the model catalog by reading the upstream provider",
		"Pulls the upstream model list and lands it through the same upsert the push door uses, "+
			"so the rule that a sync owns cost and an administrator owns price holds no matter "+
			"which door a row came through. It takes no body — the upstream is READ rather than "+
			"told. If that upstream cannot be read the call answers 502 and writes NOTHING: a "+
			"sync that cannot see its source must never conclude the source is empty, because "+
			"that conclusion would withdraw every model on sale. The gate is a PLATFORM principal "+
			"so the scheduled job's service token qualifies.")

	openapi.Describe("/v1/catalog/seed", http.MethodPost,
		"Seed the embedded catalog, without disturbing edits already made",
		"Upserts the shipped catalog seed and answers how many entries it created. It is "+
			"idempotent and non-destructive — an entry an administrator has since edited is left "+
			"alone — so it is safe to run against a live catalog to fill in what is missing. "+
			"PLATFORM admin only; an org-level admin is refused 403.")
}

// ---- /v1/commerce — the public, host-resolved checkout surface ----

func describePublic() {
	openapi.Describe("/v1/commerce/admin/catalog", http.MethodGet,
		"The catalog projection with cost and margin included",
		"Returns the brand-scoped catalog carrying the administrative economics the public "+
			"projection withholds — upstream cost and margin percentage — for the margin surface "+
			"the platform console administrates. The brand comes from the query and defaults to "+
			"hanzo. PLATFORM admin only, enforced by the handler on top of the route's IAM gate: "+
			"an ORG-level admin is refused 403 precisely so upstream cost and margin never reach "+
			"a tenant.")

	openapi.Describe("/v1/commerce/catalog", http.MethodGet,
		"The public product catalog projection for a brand",
		"Returns the brand's published catalog — the shared source docs, the console sidebar and "+
			"the pricing pages all read — with the brand taken from the query and defaulting to "+
			"hanzo. It is public and cacheable, and it is the projection that deliberately omits "+
			"cost and margin; those live only on the platform-admin projection.")

	openapi.Describe("/v1/commerce/currencies", http.MethodGet,
		"The reference currency list the price and settings pickers render",
		"Returns every reference currency as one global list, so a store settings form or a "+
			"product price picker binds real rows instead of a hardcoded array. It is a "+
			"default-namespace read shared by every tenant rather than per-org data, and it is "+
			"public and cacheable.")

	// The deposit-proxy trio and the webhook relay are deliberately absent
	// here: the module removed those routes — deposits are commerce's own rails
	// now (topup/token, wire, crypto) and the real webhook receiver is POST
	// /v1/billing/webhooks/:provider — and prose for a route that does not
	// exist never renders, so keeping it would only preserve a dead claim.
	openapi.Describe("/v1/commerce/tenant", http.MethodGet,
		"The public tenant configuration a checkout page boots from",
		"Answers the branding, identity issuer and client id, identity-verification config, "+
			"enabled payment providers, return-URL allowlist and public payment application "+
			"config for the tenant the request HOST resolves to. It is genuinely public and "+
			"unauthenticated — a checkout page calls it before anyone has signed in — and it "+
			"carries the same public payment config the authenticated config read does, so the "+
			"card iframe can never initialize against a different application than the one that "+
			"will be charged. Only ENABLED providers are listed and no credential path is ever "+
			"projected. An unresolvable host answers a constant 404 that does not echo the host, "+
			"so the endpoint cannot be used to enumerate tenants; a successful answer is cacheable "+
			"for a minute.")
}

// ---- /v1/plans — the platform-admin subscription plan authority ----

func describePlans() {
	openapi.Describe("/v1/plans/entries", http.MethodGet,
		"The raw plan authority rows",
		"Returns every plan row as stored — the administrative view behind the public plan "+
			"catalog. The plan authority is cross-tenant pricing data, so the gate is a PLATFORM "+
			"admin enforced by the handler itself: an org-level admin is refused 403 no matter "+
			"what they may do inside their own org.")

	openapi.Describe("/v1/plans/entries", http.MethodPost,
		"Add a subscription plan",
		"Creates a plan from the body and answers it at 201. The slug is required and globally "+
			"unique — a duplicate is 409 — and the row is marked authoritative on creation, so "+
			"the corrective seed will leave it alone. Price, annual price and the contact-sales "+
			"flag are stored exactly as sent, never coerced, so the difference between a free "+
			"plan and a quote-only plan survives. PLATFORM admin only.")

	openapi.Describe("/v1/plans/entries/:slug", http.MethodPut,
		"Edit a plan, leaving the fields you omit alone",
		"Loads the addressed plan, applies the body over it and answers the stored result, so a "+
			"partial edit never silently zeroes a price or the contact-sales flag. The slug is "+
			"IMMUTABLE: a body naming a different slug is rejected outright before anything is "+
			"written, because a rename would orphan every subscription that stored the old id — "+
			"deprecate and create instead. An admin edit marks the row authoritative so the seed "+
			"stops correcting it. PLATFORM admin only; an unknown slug is 404.")

	openapi.Describe("/v1/plans/entries/:slug", http.MethodDelete,
		"Remove a plan from the authority",
		"Deletes the addressed plan and answers 204. It removes the plan from the catalog buyers "+
			"choose from; it does not touch subscriptions already sold against it, which keep "+
			"their stored plan id. PLATFORM admin only — an org-level admin is refused 403 — and "+
			"an unknown slug is 404.")

	openapi.Describe("/v1/plans/seed", http.MethodPost,
		"Seed the embedded plan catalog, without overwriting administrative edits",
		"Upserts the shipped plan rows and answers how many were created and how many corrected. "+
			"It is idempotent and non-destructive — a row an administrator authored or edited is "+
			"left as it stands — so it is safe against a live authority and fills only what is "+
			"missing or has drifted. PLATFORM admin only, and a deployment with no seed source "+
			"wired answers 500 rather than quietly seeding nothing.")
}

// ---- /v1/store — storefronts, their listings, and the catalog they overlay ----

func describeStore() {
	openapi.Describe("/v1/store/", http.MethodGet,
		"List your org's storefronts as a page",
		"Answers a pagination envelope — page, display, the rows, and a total count — read from "+
			"the caller org's OWN namespaced database, so one tenant can never list another's "+
			"stores. Sorting defaults to the store slug and is overridable with sort; display is "+
			"the page size and page applies only alongside it, and either one that is not a "+
			"positive integer is refused rather than silently ignored. The limit query overrides "+
			"the reported COUNT only and never the rows returned. A request that resolves no org "+
			"namespace is served an empty page, never an unscoped scan. Readable with an admin "+
			"token, a store-scoped token, or the anonymous published storefront key.")

	openapi.Describe("/v1/store/", http.MethodPost,
		"Create a storefront",
		"Creates a store from the body inside the caller org's own namespaced database, so the "+
			"row is physically isolated to that tenant from its first write, and answers it at 201 "+
			"with a Location header naming its id. Requires an admin or store-write token: the "+
			"anonymous published storefront key may READ stores but never create one. A body that "+
			"fails to decode is 400.")

	openapi.Describe("/v1/store/access", http.MethodGet,
		"Whether a store is entitled to trade, and why",
		"Answers allowed, the store id, and a status of trial, active, payment_required, "+
			"store_required or unavailable — the entitlement check a merchant surface gates on. "+
			"The rule that surprises people is that entitlement is PER STORE, not per org: the "+
			"store needs its own current subscription on the entry plan, either trialing with a "+
			"trial end still ahead or active with a period end still ahead, so an org-wide "+
			"balance or a sibling store's plan unlocks nothing here. The store comes from the "+
			"X-Store-Id header and otherwise falls back to the org's first store; neither "+
			"resolving is store_required with allowed false, and a backing-store failure is 503 "+
			"with status unavailable — a retry signal, not a denial.")

	openapi.Describe("/v1/store/current", http.MethodGet,
		"Resolve your org's active storefront without naming an id",
		"Returns the caller org's store resolved FROM THE AUTHENTICATED ORG rather than from a "+
			"path id — which is how an admin dashboard or a storefront edge learns the store id it "+
			"should then read and write against. An X-Store-Id header selects a specific store, "+
			"resolved only inside the caller's own namespace, so a foreign id cannot cross the "+
			"tenant boundary and answers 404 instead. With no header the org's first store is "+
			"returned, and an org that has none yet has its canonical default provisioned lazily "+
			"and idempotently, carrying no payment credentials. Only when there is no org in "+
			"context, or provisioning fails, does it fall back to a placeholder store literally "+
			"named default, which a storefront edge should treat as unconfigured.")

	openapi.Describe("/v1/store/token", http.MethodPost,
		"Mint your org's least-privilege storefront read key",
		"Answers a freshly minted token carrying ONLY the published-read permission — enough for "+
			"a logged-out shopper's storefront to read your published catalog and nothing more, "+
			"with no write and no admin scope. It is org-bound, signed with the org's own secret "+
			"and subject to the org id, so unlike a shared service token it can never act on "+
			"another tenant. Minting ROTATES rather than accumulates: the previous storefront "+
			"token is dropped first and is invalid immediately, so re-minting is how you revoke. "+
			"Admin is enforced by the handler as well as the route, because the route's token gate "+
			"does not apply on the identity path and a plain member must not be able to mint their "+
			"org's key.")

	openapi.Describe("/v1/store/:storeid", http.MethodGet,
		"Fetch one storefront",
		"Reads the addressed store from the caller org's own namespaced database, so an id "+
			"belonging to another tenant is simply absent there and answers 404 rather than "+
			"leaking its existence. The body is the stored entity including its embedded listing "+
			"override map. Readable with an admin or store-read token and also with the anonymous "+
			"published storefront key, which is what lets a logged-out storefront resolve the "+
			"store it is rendering.")

	openapi.Describe("/v1/store/:storeid", http.MethodPut,
		"Replace a storefront outright",
		"This is a true REPLACEMENT, not a merge: the stored key is preserved but the body is "+
			"decoded onto a fresh entity, so every field the body omits is written back as its "+
			"zero value. Use the partial update when you mean to change part of a store. The id "+
			"is resolved inside the caller org's own namespace, so an unknown or foreign id is a "+
			"404 before anything is written. Requires an admin token, or one holding both store "+
			"read and store write.")

	openapi.Describe("/v1/store/:storeid", http.MethodPatch,
		"Change part of a storefront",
		"Loads the stored store and decodes the body over it, so only the fields the body names "+
			"change and everything else keeps its stored value — the difference from the full "+
			"replace, which clears what it is not told. Answers the merged entity. The id is "+
			"resolved inside the caller org's own namespace, so an unknown or foreign id is 404. "+
			"Requires an admin token, or one holding both store read and store write.")

	openapi.Describe("/v1/store/:storeid", http.MethodPost,
		"Method-override tunnel for clients that cannot send PUT, PATCH or DELETE",
		"Re-dispatches the request into the handler the intended verb would have reached, taking "+
			"that verb from a _method form value or query parameter and then from the "+
			"X-HTTP-Method-Override header, the header winning when both are present. Only PUT, "+
			"PATCH and DELETE are accepted; anything else resolves to 405. The trap is the "+
			"default: naming NO override at all is treated as a partial update, never as a "+
			"create. Authorization is whatever the underlying operation requires, since the real "+
			"handler runs.")

	openapi.Describe("/v1/store/:storeid", http.MethodDelete,
		"Delete a storefront, keeping a recoverable copy",
		"Removes the addressed store and answers 204 with no body. Before the live row goes, the "+
			"entity is written once more under a tombstone kind, so the deletion leaves a "+
			"recoverable copy rather than destroying the record outright; the store's listing "+
			"overrides live inside that row and go with it. The id is resolved inside the caller "+
			"org's own namespace, so an unknown or foreign id is 404. Requires an admin or "+
			"store-write token.")

	openapi.Describe("/v1/store/:storeid/trial", http.MethodPost,
		"Start this store's no-card trial on the entry plan",
		"Creates a trialing subscription for the addressed store on the entry plan and grants "+
			"that plan's trial credit, answering 201 when this call actually started one and 200 "+
			"with a reason otherwise — not_new when the store already has billing history, "+
			"trial_not_configured when no entry plan is wired. The window is always the SEVEN-DAY "+
			"no-card trial, because this address never presents a card; the longer card-present "+
			"window is reached only by adding a card afterwards. Entitlement is per store while "+
			"the billing subject is the org, so every store an org owns takes its own trial. "+
			"Admin-gated and namespaced to the caller's org: no resolvable store is 404 with "+
			"store_required, and a backing-store failure is 503.")

	openapi.Describe("/v1/store/:storeid/bundle/:key", http.MethodGet,
		"Fetch a bundle as this storefront sells it",
		"Returns the stored bundle with the store's listing for it laid over the top — every "+
			"non-empty listing field wins, and the currency is forced to the store's own — so the "+
			"caller reads what this storefront actually sells rather than the catalog-wide "+
			"record. The overlay is keyed by the item's ID: a listing filed only under a slug or "+
			"SKU does not reach it, unlike the listing reads, which do fall back to those. An "+
			"unknown store or key is 404. Readable with an admin token or the anonymous published "+
			"storefront key.")

	openapi.Describe("/v1/store/:storeid/product/:key", http.MethodGet,
		"Fetch a product as this storefront sells it",
		"Returns the stored product with the store's listing for it laid over the top — non-empty "+
			"listing fields replace the catalog values and the currency is forced to the store's "+
			"own — which is what lets two storefronts sell the same catalog product at their own "+
			"price, name and media. The overlay is keyed by the product's ID, so a listing filed "+
			"only under a slug or SKU does not apply here. An unknown store or key is 404. "+
			"Readable with an admin token or the anonymous published storefront key.")

	openapi.Describe("/v1/store/:storeid/variant/:key", http.MethodGet,
		"Fetch a variant as this storefront sells it",
		"Returns the stored variant with the store's listing for it overlaid — non-empty listing "+
			"fields replace the catalog values and the currency is forced to the store's own — "+
			"which is what makes per-storefront pricing of a shared variant possible. The overlay "+
			"is keyed by the variant's ID, never by its slug or SKU. An unknown store or key is "+
			"404. Readable with an admin token or the anonymous published storefront key.")

	openapi.Describe("/v1/store/:storeid/listing", http.MethodGet,
		"The storefront's whole listing override map",
		"Returns every override this store applies to catalog items — name, price, list price, "+
			"media, availability and the hidden flag — keyed by product or variant id, in one "+
			"read. A listing is an OVERRIDE, not a product: the catalog item exists independently "+
			"and this map only says how this storefront presents it. Read from the caller org's "+
			"own namespaced database, so a store id belonging to another tenant is 404. Readable "+
			"with an admin token or the anonymous published storefront key.")

	openapi.Describe("/v1/store/:storeid/listing/:key", http.MethodGet,
		"Fetch one listing override, by item id or by its slug or SKU",
		"Looks the key up in the store's listing map first and, failing that, matches it against "+
			"each listing's slug and then its SKU — so a storefront holding only a product's URL "+
			"slug can still resolve the override. That fallback is unique to the listing reads; "+
			"the item overlay routes match by id alone. A key matching none of the three is 404, "+
			"as is a store id outside the caller org's namespace. Readable with an admin token or "+
			"the anonymous published storefront key.")

	openapi.Describe("/v1/store/:storeid/listing/:key", http.MethodPost,
		"Add a listing override under a new key",
		"Creates the override and answers the store's ENTIRE listing map at 201 with a Location "+
			"header — not just the entry that was added. A key already present is refused 400: "+
			"creation never silently overwrites, so changing an existing listing has to be an "+
			"explicit replace. The stored listing has its currency stamped from the store's own, "+
			"which the replace path does not do. The key is matched exactly here, with none of "+
			"the slug or SKU fallback the read allows. Admin-gated and resolved inside the caller "+
			"org's namespace.")

	openapi.Describe("/v1/store/:storeid/listing/:key", http.MethodPut,
		"Upsert a listing override",
		"Decodes the body over the existing listing when the key is present, so fields it omits "+
			"keep their stored values, and builds the listing from the body alone when the key is "+
			"new. Answers 200 when it replaced something and 201 with a Location header when it "+
			"created it; either way the body is the store's entire listing map, not the single "+
			"entry. Unlike creation, this path does NOT restamp the listing's currency from the "+
			"store. Admin-gated, with the store resolved inside the caller org's namespace.")

	openapi.Describe("/v1/store/:storeid/listing/:key", http.MethodPatch,
		"Confirm a listing override exists and re-save the store",
		"Requires the key to already be present — an absent one is 404 — and answers the store's "+
			"listing map at 200. Read the behaviour before relying on it: the decoded body is "+
			"applied to a COPY taken out of the map and is never assigned back, so the stored "+
			"listing is unchanged and the map returned is exactly the map that was already there. "+
			"An actual edit to an existing listing has to go through the upsert, which does write "+
			"its result back into the store. A body that fails to decode is still 400. "+
			"Admin-gated and namespaced to the caller's org.")

	openapi.Describe("/v1/store/:storeid/listing/:key", http.MethodDelete,
		"Remove a listing override",
		"Drops the key from the store's listing map and re-saves the store, answering 204 with no "+
			"body. It UN-OVERRIDES rather than deletes: the product, variant or bundle itself is "+
			"untouched and simply reverts to its catalog values on this storefront. A key that is "+
			"not present is 404, and so is a store id outside the caller org's namespace. "+
			"Admin-gated.")
}

// ---- /v1/store checkout — the two-step and one-step payment flows ----
//
// The /checkout-prefixed addresses bind the SAME handlers as their shorter
// siblings: the prefix is the newer spelling of one operation, not a second
// behaviour. Each is described on its own terms because a reader lands on one
// address at a time, and the alias is named where it is the thing they would
// otherwise get wrong.

func describeCheckout() {
	openapi.Describe("/v1/store/:storeid/authorize", http.MethodPost,
		"Authorize a new order against a storefront, holding the funds without settling them",
		"Tallies a new order for the addressed store from the user, payment and order body, "+
			"reserves its items, runs the processor authorization and answers the saved order with "+
			"a Location header pointing at it. The gate is a token carrying admin or published "+
			"scope, so a published storefront key is enough; no token is 401 and a token with "+
			"neither bit is 403. The store is loaded BEFORE any payment work and its currency "+
			"OVERRIDES whatever the body asked for, so a store that will not load ends the call "+
			"with 500 and nothing is charged. On any authorization failure the reservations are "+
			"released and the order and payment are persisted as cancelled, so a failed attempt "+
			"still leaves a durable record. Capture is a separate call.")

	openapi.Describe("/v1/store/:storeid/authorize/:orderid", http.MethodPost,
		"Authorize an order that already exists, holding the funds without settling them",
		"Continues the order named in the path rather than minting a new one, holding funds for "+
			"it. The order is loaded from the caller org's own store, so an id belonging to "+
			"another tenant is a 404. The rule most callers get wrong is that the body's order "+
			"object is MERGED onto the loaded order before the tally — this is not a read-only "+
			"reference, and a field sent here overwrites what is stored. The gate, the store "+
			"resolution and the currency override behave exactly as on the bodiless-id sibling, "+
			"and settling is still the capture call's job.")

	openapi.Describe("/v1/store/:storeid/capture/:orderid", http.MethodPost,
		"Capture a previously authorized order and settle the payment",
		"Settles the order named in the path — the second half of the two-step flow — and answers "+
			"the updated order with a Location header. Dispatch follows the order's STORED payment "+
			"type, and a successful capture is the moment the rest of the system learns about the "+
			"sale: order and payment rows are updated, coupon redemptions, referral, cart and "+
			"stats are written, the confirmation email goes out, and the paid and completed events "+
			"are emitted. A capture failure releases the order's inventory reservations and "+
			"answers 400, so a failed settlement never leaves items held.")

	openapi.Describe("/v1/store/:storeid/charge", http.MethodPost,
		"Authorize and capture a new order in one call",
		"Runs authorization and capture back to back against a freshly created order — the "+
			"one-step flow for callers with no reason to hold funds. It takes the authorize body "+
			"and inherits every authorize rule: the store's currency wins over the body, the items "+
			"are reserved before the processor is called, and the amount bounds the processor "+
			"enforces still apply. There is no order id on this address, so it can never continue "+
			"an existing order. Either half failing answers 400, and the capture side effects — "+
			"confirmation email, redemptions, stats, the paid and completed events — run only when "+
			"both halves succeed.")

	openapi.Describe("/v1/store/:storeid/paypal/pay", http.MethodPost,
		"Start a PayPal authorization for a new order",
		"Runs the ordinary store authorize flow — the route binds that very handler, so the body, "+
			"the store resolution, the tally, the reservations and the failure behaviour are the "+
			"authorize address's, unchanged. It reaches PayPal only when the body's payment type "+
			"says so; nothing about this path forces the processor, so a card-typed payment posted "+
			"here authorizes on the card processor instead. A successful PayPal authorization "+
			"stamps a pay key onto the payment, which is the key the confirm and cancel addresses "+
			"filter on. It is the older entry point; the plain authorize address is the one to "+
			"build against.")

	openapi.Describe("/v1/store/:storeid/paypal/confirm/:payKey", http.MethodPost,
		"PayPal confirm by pay key — refuses, because a pay key alone does not identify the order",
		"Intended to mark every payment carrying the given pay key as paid and flip the order to "+
			"paid, it cannot do that from this address and does not pretend to: the shared "+
			"checkout handler resolves its order from an ORDER ID path parameter that this route "+
			"does not carry, so it always works against a fresh untyped order and the confirm "+
			"dispatch refuses it with 400 before the pay key is ever queried. The token gate, the "+
			"namespace and the store lookup all run ahead of that, so a missing token is still "+
			"401 and an unloadable store still 500. Drive a PayPal return through an address that "+
			"carries the order id.")

	openapi.Describe("/v1/store/:storeid/paypal/cancel/:payKey", http.MethodPost,
		"PayPal cancel by pay key — refuses, because a pay key alone does not identify the order",
		"Intended to void the payments carrying the given pay key, stamp them cancelled and "+
			"cancel the order, it never reaches that work: the shared checkout handler reads its "+
			"order from an ORDER ID path parameter this route does not carry, leaving an untyped "+
			"order that the cancel dispatch refuses with 400 before the pay key lookup runs. "+
			"Authentication, namespacing and store resolution happen ahead of the refusal, so a "+
			"missing token is 401 and an unloadable store 500. Cancelling a real PayPal "+
			"authorization needs an address that carries the order id.")

	openapi.Describe("/v1/store/:storeid/checkout/authorize", http.MethodPost,
		"Authorize a new order against a storefront, holding the funds — the checkout spelling",
		"Authorizes a new order for the addressed store and holds the funds, answering the saved "+
			"order with a Location header. It binds the identical handler as the shorter authorize "+
			"address, so the two are ONE operation at two spellings and not two behaviours; the "+
			"checkout prefix is the newer one. Every rule carries over: admin or published scope "+
			"on the token, the store loaded first with its currency overriding the body, items "+
			"reserved before the processor call, and reservations released with the order "+
			"persisted cancelled on failure. Nothing is settled here.")

	openapi.Describe("/v1/store/:storeid/checkout/authorize/:orderid", http.MethodPost,
		"Authorize an existing order, holding the funds — the checkout spelling",
		"Continues the order named in the path rather than minting one, and shares its handler "+
			"byte for byte with the unprefixed authorize-by-id address. The order is loaded from "+
			"the caller org's own store, so another tenant's id is a 404, and the body's order "+
			"object is merged onto the loaded row before the tally — a field sent here overwrites "+
			"what is stored. Store resolution, the token gate and the currency override behave as "+
			"on every other authorize address; settle with the capture address and the same order "+
			"id.")

	openapi.Describe("/v1/store/:storeid/checkout/capture/:orderid", http.MethodPost,
		"Capture a previously authorized order and settle it — the checkout spelling",
		"Settles the authorized order named in the path and answers the updated order with a "+
			"Location header, running the same handler as the unprefixed capture address. Dispatch "+
			"follows the order's stored payment type. Success is what triggers the downstream "+
			"work — order and payment updates, redemptions, referral, cart and stats, the "+
			"confirmation email, and the paid and completed events — while a failure releases the "+
			"order's inventory reservations and answers 400.")

	openapi.Describe("/v1/store/:storeid/checkout/charge", http.MethodPost,
		"Authorize and capture a new order in one call — the checkout spelling",
		"Performs authorization and capture back to back against a newly created order for the "+
			"addressed store, on the same handler as the unprefixed charge address. It takes the "+
			"authorize body and inherits every authorize rule, including the store's currency "+
			"winning over the body and the items being reserved before the processor is called. "+
			"There is no order id on this address, so it can never continue an existing order. "+
			"Either half failing answers 400, and the capture side effects run only when both "+
			"succeed.")

	openapi.Describe("/v1/store/:storeid/checkout/paypal/pay", http.MethodPost,
		"Start a PayPal authorization for a new order — the checkout spelling",
		"Begins a PayPal authorization by running the ordinary store authorize flow, since the "+
			"route binds that exact handler — body, store resolution, tally, reservations and "+
			"failure behaviour are the authorize address's, unchanged. The processor is chosen "+
			"from the body's payment type, so this path reaches PayPal only when that type says "+
			"so. A successful PayPal authorization stamps a pay key onto the payment, which is the "+
			"key the confirm and cancel addresses filter on. Build against the plain authorize "+
			"address instead.")

	openapi.Describe("/v1/store/:storeid/checkout/paypal/confirm/:payKey", http.MethodPost,
		"PayPal confirm by pay key — refuses, exactly as the unprefixed address does",
		"Meant to mark the payments carrying the given pay key as paid and set the order to paid, "+
			"it cannot reach that work from this address: the shared checkout handler takes its "+
			"order from an ORDER ID path parameter this route does not carry, so the order is "+
			"always fresh and untyped and the confirm dispatch refuses with 400 before the pay key "+
			"is queried. The token gate, the namespace middleware and the store lookup all run "+
			"ahead of that, so authentication and store failures surface first. Behaviour is "+
			"identical to the unprefixed confirm address; the checkout prefix changes nothing "+
			"here.")

	openapi.Describe("/v1/store/:storeid/checkout/paypal/cancel/:payKey", http.MethodPost,
		"PayPal cancel by pay key — refuses, exactly as the unprefixed address does",
		"Meant to void the payments carrying the given pay key, stamp them cancelled and cancel "+
			"the order, but the shared checkout handler resolves its order from an ORDER ID path "+
			"parameter this route does not carry. The result is an untyped order and a cancel "+
			"dispatch that refuses with 400 before the pay key lookup ever runs. Token gate, "+
			"namespacing and store resolution happen first, so a missing token is still 401 and an "+
			"unloadable store still 500. It is the same handler as the unprefixed cancel address, "+
			"with the same outcome.")
}

// ---- the generated resource surface ----

// resources are the CRUD families the commerce module registers wholesale
// (hanzoai/commerce api/resources, bound onto the /v1/commerce group in
// mount.go) — one collection route and one item route each, seven operations to
// a family, 119 in all.
//
// So they are DERIVED, for the same reason openapi.DescribeSPA derives the two
// SPA addresses: prose written 119 times is prose that drifts 119 ways, and the
// next resource the module adds would arrive undescribed and stop the surface
// gate again. Adding a family here is one line.
//
// WHAT THE TABLE CARRIES IS THE GATE, and that is the whole reason it is a table
// of structs rather than a list of names. The seventeen are NOT uniform, and the
// axis they differ on is the one a caller gets wrong: three are admin-only, six
// sit behind the subscription paywall and answer 402, and five check a per-method
// token permission on top of whichever of those applies. One sentence repeated
// seventeen times is green and WRONG — it tells a reader that a plain member's
// token can write a wallet, and that the only refusal on a product is 404. A
// description that is confidently incorrect about a money path is worse than the
// bare operationId it replaced, because nothing downstream can tell it is wrong.
//
// The shape talk stays plain, though. These are generic store resources and a
// caller learns what a `product` is from the product, so each sentence states the
// address, what the method does to it, and what it refuses — then stops. A family
// that outgrows this comes OUT of the table and gets an explicit Describe
// instead — not as WELL: Describe panics on a duplicate key, so the hand-written
// sentence and this loop cannot both claim one address, and leaving the family
// here while adding prose above would abort the binary at init.
type gate int

const (
	// gateToken is an IAM token and nothing more (commercemid.TokenRequired()).
	gateToken gate = iota
	// gateSubscribed additionally passes paywall.Require.
	gateSubscribed
	// gateAdmin additionally carries permission.Admin.
	gateAdmin
)

type resource struct {
	kind string
	gate gate
	// scope marks the kinds util/rest carries a DefaultPermissions table for,
	// which CheckPermissions enforces per method. For every other kind that
	// lookup MISSES, and the miss logs a warning and ALLOWS — so claiming a
	// scope check on those would be prose the server does not honour.
	scope bool
	// why is the one fact about THIS family that outranks its shape. Only the
	// money and credential families have one; the rest are plain store rows and
	// inventing a distinction for them is how a table like this starts lying.
	why string
}

var resources = []resource{
	{kind: "collection", gate: gateSubscribed, scope: true},
	{kind: "disclosure", gate: gateToken},
	{kind: "discount", gate: gateSubscribed},
	{kind: "movie", gate: gateToken},
	{kind: "note", gate: gateToken},
	{kind: "product", gate: gateSubscribed, scope: true},
	{kind: "return", gate: gateToken, scope: true},
	{kind: "saleschannel", gate: gateSubscribed},
	{kind: "stocklocation", gate: gateSubscribed},
	{kind: "submission", gate: gateToken},
	{kind: "subscriber", gate: gateToken, scope: true},
	{kind: "tokentransaction", gate: gateToken},
	{kind: "transfer", gate: gateAdmin,
		why: "A transfer RECORDS that a payable was paid out-of-band; it does not move money. " +
			"Commerce executes no payout, so writing one settles a debt in the books and nowhere " +
			"else — which is why the family is admin-gated when the rest of the merchant CRUD is not."},
	{kind: "variant", gate: gateSubscribed, scope: true},
	{kind: "wallet", gate: gateAdmin,
		why: "A wallet holds blockchain accounts and the keys generated for them, so it is " +
			"admin-gated on reads as much as writes."},
	{kind: "watchlist", gate: gateToken},
	{kind: "webhook", gate: gateAdmin,
		why: "A webhook carries the delivery endpoint and the access token sent with it, so it is " +
			"admin-gated: this is outbound credential material, not catalogue."},
}

// refusals states what this family turns away and with what, in the order a
// request meets the gates: the group's own IAM check, then the route's, then the
// per-method permission.
func (r resource) refusals() string {
	s := "\n\nAn anonymous call is refused 401."
	switch r.gate {
	case gateAdmin:
		s += " The token must carry the admin permission — a plain member's token is refused 403 " +
			"here, on reads as well as writes."
	case gateSubscribed:
		s += " The org's subscription is checked on every call: with no active subscription, trial " +
			"or redeemed invite the answer is 402 subscription_required, and a billing store this " +
			"process cannot read fails closed with 503 billing_unavailable rather than serving. A " +
			"platform service token or a superadmin passes without that check."
	}
	if r.scope {
		s += " This kind also carries a per-method token permission, so a token that authenticates " +
			"but lacks the scope for this method is refused 403; the admin permission satisfies " +
			"every one of them."
	}
	if r.why != "" {
		s += "\n\n" + r.why
	}
	return s
}

func describeResources() {
	for _, r := range resources {
		k := r.kind
		coll := "/v1/commerce/" + k + "/"
		item := "/v1/commerce/" + k + "/:" + k + "id"
		tail := "\n\nScoped to the caller's own tenant: the store is keyed by the org the edge " +
			"resolves from the verified token, and a client-sent X-Org-Id is deleted before " +
			"routing rather than trusted, so one tenant's " + k + " is not addressable by another " +
			"even by exact id — an id outside the caller's tenant answers 404, the same as one " +
			"that does not exist." + r.refusals()

		openapi.Describe(coll, http.MethodGet,
			"List "+k+" records",
			"Returns this tenant's "+k+" records as a pagination envelope — page, display, count, "+
				"models and facets — so the records are under `models` and not at the top level. "+
				"`display` sets the page size and `page` the 1-based page (paging needs both; "+
				"`display` alone just caps the result), and `sort` names the field to order by. "+
				"When the request carries no resolvable org namespace this answers 200 with an "+
				"EMPTY page rather than an error, so an empty `models` means nothing was readable "+
				"for this tenant — not necessarily that no records exist."+tail)
		openapi.Describe(coll, http.MethodPost,
			"Create a "+k+" record",
			"Creates one "+k+" from the request body and returns the stored record, including the "+
				"id every other operation on this resource addresses it by."+tail)

		openapi.Describe(item, http.MethodGet,
			"Read one "+k+" record",
			"Returns the one "+k+" with this id."+tail)
		openapi.Describe(item, http.MethodPut,
			"Replace a "+k+" record",
			"Replaces the "+k+" with this id: the body is decoded onto an EMPTY record that keeps "+
				"only the existing key, so every field the body omits is reset to its zero value. "+
				"That is the whole difference from PATCH, and it is how a partial PUT silently "+
				"clears fields. An id that does not exist answers 404 — this verb never creates."+tail)
		openapi.Describe(item, http.MethodPatch,
			"Update part of a "+k+" record",
			"Loads the stored "+k+", decodes the body over it and writes the result, so fields the "+
				"body omits keep the values they already had. An id that does not exist answers "+
				"404."+tail)
		openapi.Describe(item, http.MethodDelete,
			"Delete a "+k+" record",
			"Removes the "+k+" with this id, after writing a copy aside under an internal deleted "+
				"key — so the record stops answering here but is not erased from storage. An id "+
				"that does not exist answers 404."+tail)

		// POST on the ITEM address is the method-override door. Describing it as
		// "creation, but on an id" would be wrong twice: it does not create, and it
		// is the one address where a POST can DELETE.
		openapi.Describe(item, http.MethodPost,
			"Update one "+k+" record, or tunnel another method at it",
			"The method-override door, not a second create — creation is POST on the collection. "+
				"With no override this does exactly what PATCH does: the fields present in the "+
				"body are written and the rest are left alone. Set `_method` (form field or query "+
				"parameter) or the `X-HTTP-Method-Override` header to PUT, PATCH or DELETE and it "+
				"performs THAT method instead, so a POST to this address can replace or DELETE the "+
				"record. Any other override value is ignored and the call stays a PATCH."+tail)
	}
}
