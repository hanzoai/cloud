// Copyright © 2026 Hanzo AI. MIT License.

// describe.go is commerce's PROSE. Nearly every operation this subsystem serves is
// registered by the embedded hanzoai/commerce module — its own route tables, its
// own handlers, in another module — so there is no doc comment in this repo for
// zipdoc to lift and no typed op to lift it from. Left bare, each published an
// operationId and NOTHING else: an SDK method that cannot explain itself, an MCP
// tool a model cannot pick, a CLI command with no help text.
//
// The count is 176 of 183, and it is DERIVED rather than written down here:
// typed_wire_test.go reads it off the live router and holds the two ledgers that
// account for every one of them. This file used to say 75, which was true when it
// was written and has been wrong for a long time — a number in prose is a
// measurement with no way to fail, which is exactly what the gate replaced.
//
// openapi.Describe is the client for exactly that operation. It carries the same
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
	describeWebhooks()
	describeCatalog()
	describePublic()
	describePlans()
	describeStore()
	describeCheckout()
	describeMerchant()
}

// ---- /v1/commerce — the tenant-admin read ----

// The health probe is deliberately absent here: it is a typed op (mount.go),
// so its prose is the doc comment zipdoc lifts, and a Describe beside it would
// be a second prose source for one route.
func describeAdmin() {

	openapi.Describe("/v1/commerce/deposits", http.MethodGet,
		"Read the crypto deposit watcher's runtime state, asset by asset",
		"Reports whether the deposit watcher is running, its poll interval, and one row per armed "+
			"asset: chain, token, contract, pooled address and the last block that asset's cursor "+
			"reached. That last block is the only way to see a watcher that is up but no longer "+
			"advancing, which is what a stalled deposit rail looks like from outside. SuperAdmin "+
			"only — the reserved admin org's owner claim; an authenticated caller without it is "+
			"refused 403 and an anonymous one 401. It is READ-ONLY by design: arming an asset "+
			"stays a CRYPTO_DEPOSIT_* deployment act and is deliberately not a button here, so "+
			"there is nothing on this surface that can start crediting a customer's balance. The "+
			"asset's RPC endpoint is reduced to scheme://host before it is returned, because a "+
			"managed node URL carries its API key in the path or query and echoing it verbatim "+
			"would publish that credential to every reader of this status.")
}

// ---- /v1/commerce/webhooks — the processor's own endpoint ----

// The rest of what this function used to describe is gone from this file, not
// deleted: the whole /v1/billing family is served by apps/billing now, as typed
// ops whose prose is the doc comment zipdoc lifts. A Describe here for a route
// this app no longer registers would render nothing anyway — the client is
// additive metadata on live routes — so what moved is the prose along with the
// route it explains.
func describeWebhooks() {
	openapi.Describe("/v1/commerce/webhooks/:provider", http.MethodPost,
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

// ---- /v1/commerce/catalog — the platform-admin product CMS ----

func describeCatalog() {
	// THE RATE AUTHORITY'S CRUD (api/rate/handlers.go in hanzoai/commerce). Its
	// handlers carry good doc comments, but zipdoc lifts prose only from source in
	// THIS repo, so a comment one module over reaches no reader. These say the same
	// thing at the client that can be read.
	openapi.Describe("/v1/commerce/rates/entries", http.MethodGet,
		"List what one unit of each metered thing costs",
		"Returns the rate authority's rows — the prices every metered charge resolves against. "+
			"Narrow with ?product= to show one surface at a time rather than every rate at once. "+
			"SuperAdmin only: a rate is cross-tenant money, so the handler asks for the reserved "+
			"admin org's owner claim itself rather than trusting the bundle's token gate.")
	openapi.Describe("/v1/commerce/rates/entries", http.MethodPost,
		"Add a rate",
		"Creates one rate. Product AND meter are both required, because together they are the "+
			"identity: a rate keyed on the metered thing alone would let one product's price "+
			"overwrite another's under the same name. A slug that already exists is refused rather "+
			"than silently replaced. SuperAdmin only.")
	openapi.Describe("/v1/commerce/rates/entries/:product/:meter", http.MethodPut,
		"Edit a rate, and mark it as operator-set",
		"Edits one rate and MARKS it edited, which is the whole contract with the importer: an "+
			"operator's price outranks the document it came from, so a later import leaves this row "+
			"alone. Without that mark a price set here would apply, work, and silently revert on the "+
			"next import. Only the editable fields move; identity and bookkeeping are not writable "+
			"from the body. SuperAdmin only.")
	openapi.Describe("/v1/commerce/rates/entries/:product/:meter", http.MethodDelete,
		"Remove a rate outright",
		"Deletes the row. ARCHIVING is usually what is wanted instead — a deleted rate cannot "+
			"price a historical charge, so a past invoice that has to re-resolve its rate finds "+
			"nothing to read. Reach for status=archived unless the rate never priced anything. "+
			"SuperAdmin only.")
	openapi.Describe("/v1/commerce/rates/import", http.MethodPost,
		"Load the published price document, reconciling rather than replacing",
		"Takes an array of rates and seeds the authority from it — the same reconcile the boot "+
			"catalog runs, driven from admin instead. It RECONCILES: a row that matches is left alone, a row that "+
			"has drifted is corrected, and a row an operator edited is skipped — so importing the "+
			"same document twice is a no-op and importing a corrected one moves exactly the rows "+
			"that changed. Answers what it received, created, corrected and left unchanged, so an "+
			"import that changes nothing reads as nothing to do rather than as a failure. An empty "+
			"array is refused 400. SuperAdmin only.")
	openapi.Describe("/v1/commerce/catalog/entries", http.MethodGet,
		"The raw catalog entries, including the unpublished ones",
		"Returns every catalog row as stored — the admin view, which unlike the public "+
			"projection includes entries that are not published. It is cross-tenant platform "+
			"data, so the gate is a PLATFORM admin: an org-level admin is refused 403 no matter "+
			"how privileged they are inside their own org, enforced by the handler itself and not "+
			"only by the route's token middleware.")

	openapi.Describe("/v1/commerce/catalog/entries", http.MethodPost,
		"Add a catalog entry",
		"Creates a catalog row from the body and answers it at 201. The slug is required and is "+
			"the globally-unique catalog key, so a second entry claiming a slug already in use is "+
			"refused 409 rather than shadowing the first. PLATFORM admin only — this is "+
			"cross-tenant pricing and packaging data, and an org-level admin is refused 403.")

	openapi.Describe("/v1/commerce/catalog/entries/*", http.MethodPut,
		"Replace a catalog entry, keeping its slug",
		"Loads the addressed entry, applies the body over it and answers the stored result. The "+
			"slug is the entry's IDENTITY and is re-stamped from the path after decoding, so a "+
			"slug in the body is ignored and a rename is impossible through this address. The "+
			"slug is matched as a trailing wildcard rather than one path segment because a "+
			"model's slug IS its callable id and those contain a slash — a segment parameter "+
			"would stop at it and leave most catalog rows unaddressable. PLATFORM admin only; an "+
			"unknown slug is 404.")

	openapi.Describe("/v1/commerce/catalog/entries/*", http.MethodDelete,
		"Remove a catalog entry",
		"Deletes the entry with the addressed slug and answers 204. The slug is matched as a "+
			"trailing wildcard, not a single segment, because a model slug contains a slash. "+
			"PLATFORM admin only — an org-level admin is refused 403 — and an unknown slug is "+
			"404, so the call is safe to repeat but not silently idempotent.")

	openapi.Describe("/v1/commerce/catalog/models", http.MethodPost,
		"Land a syncer's view of the model catalog: upstream costs and machine facts",
		"Takes a batch of model rows and upserts each one's upstream COST and machine-observable "+
			"facts, answering what was created and changed. It deliberately touches nothing a "+
			"human owns — not the retail price, not the markup, not the entitlement tier — so a "+
			"sync can never overwrite an administrator's pricing decision. The gate is a PLATFORM "+
			"principal rather than a platform ADMIN, because the caller is normally a scheduled "+
			"job holding the internal service token, which carries platform scope but no admin "+
			"claim.")

	openapi.Describe("/v1/commerce/catalog/models/refresh", http.MethodPost,
		"Refresh the model catalog by reading the upstream provider",
		"Pulls the upstream model list and lands it through the same upsert the push "+
			"endpoint uses, so the rule that a sync owns cost and an administrator owns price "+
			"holds no matter which endpoint a row came through. It takes no body — the upstream "+
			"is READ rather than told. If that upstream cannot be read the call answers 502 and "+
			"writes NOTHING: a sync that cannot see its source must never conclude the source "+
			"is empty, because that conclusion would withdraw every model on sale. The gate is "+
			"a PLATFORM principal so the scheduled job's service token qualifies.")

	openapi.Describe("/v1/commerce/catalog/seed", http.MethodPost,
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
	// /v1/commerce/webhooks/:provider — and prose for a route that does not
	// exist never renders, so keeping it would only preserve a dead claim.
	// The name is /org, not /tenant. commerce deleted its own tenant registry —
	// "the IAM org is the org; commerce keeps no registry of its own" — and the
	// route moved with the concept, to host → brand → IAM org. Prose for an
	// address that no longer answers is a dead claim, and this one outlived the
	// route long enough that the SPA went on asking for it.
	openapi.Describe("/v1/commerce/org", http.MethodGet,
		"The public org configuration a checkout page boots from",
		"Answers the branding, identity issuer and client id, identity-verification config, "+
			"enabled payment providers, return-URL allowlist and public payment application "+
			"config for the org the request HOST resolves to. It is genuinely public and "+
			"unauthenticated — a checkout page calls it before anyone has signed in — and it "+
			"carries the same public payment config the authenticated config read does, so the "+
			"card iframe can never initialize against a different application than the one that "+
			"will be charged. Only ENABLED providers are listed and no credential path is ever "+
			"projected. An unresolvable host answers a constant 404 that does not echo the host, "+
			"so the endpoint cannot be used to enumerate orgs; a successful answer is cacheable "+
			"for a minute.")
}

// ---- /v1/commerce/plans — the platform-admin subscription plan authority ----

func describePlans() {
	openapi.Describe("/v1/commerce/plans/entries", http.MethodGet,
		"The raw plan authority rows",
		"Returns every plan row as stored — the administrative view behind the public plan "+
			"catalog. The plan authority is cross-tenant pricing data, so the gate is a PLATFORM "+
			"admin enforced by the handler itself: an org-level admin is refused 403 no matter "+
			"what they may do inside their own org.")

	openapi.Describe("/v1/commerce/plans/entries", http.MethodPost,
		"Add a subscription plan",
		"Creates a plan from the body and answers it at 201. The slug is required and globally "+
			"unique — a duplicate is 409 — and the row is marked authoritative on creation, so "+
			"the corrective seed will leave it alone. Price, annual price and the contact-sales "+
			"flag are stored exactly as sent, never coerced, so the difference between a free "+
			"plan and a quote-only plan survives. PLATFORM admin only.")

	openapi.Describe("/v1/commerce/plans/entries/:slug", http.MethodPut,
		"Edit a plan, leaving the fields you omit alone",
		"Loads the addressed plan, applies the body over it and answers the stored result, so a "+
			"partial edit never silently zeroes a price or the contact-sales flag. The slug is "+
			"IMMUTABLE: a body naming a different slug is rejected outright before anything is "+
			"written, because a rename would orphan every subscription that stored the old id — "+
			"deprecate and create instead. An admin edit marks the row authoritative so the seed "+
			"stops correcting it. PLATFORM admin only; an unknown slug is 404.")

	openapi.Describe("/v1/commerce/plans/entries/:slug", http.MethodDelete,
		"Remove a plan from the authority",
		"Deletes the addressed plan and answers 204. It removes the plan from the catalog buyers "+
			"choose from; it does not touch subscriptions already sold against it, which keep "+
			"their stored plan id. PLATFORM admin only — an org-level admin is refused 403 — and "+
			"an unknown slug is 404.")

	openapi.Describe("/v1/commerce/plans/seed", http.MethodPost,
		"Seed the embedded plan catalog, without overwriting administrative edits",
		"Upserts the shipped plan rows and answers how many were created and how many corrected. "+
			"It is idempotent and non-destructive — a row an administrator authored or edited is "+
			"left as it stands — so it is safe against a live authority and fills only what is "+
			"missing or has drifted. PLATFORM admin only, and a deployment with no seed source "+
			"wired answers 500 rather than quietly seeding nothing.")
}

// ---- /v1/commerce/store — storefronts, their listings, and the catalog they overlay ----

func describeStore() {
	openapi.Describe("/v1/commerce/store/", http.MethodGet,
		"List your org's storefronts as a page",
		"Answers a pagination envelope — page, display, the rows, and a total count — read from "+
			"the caller org's OWN namespaced database, so one tenant can never list another's "+
			"stores. Sorting defaults to the store slug and is overridable with sort; display is "+
			"the page size and page applies only alongside it, and either one that is not a "+
			"positive integer is refused rather than silently ignored. The limit query overrides "+
			"the reported COUNT only and never the rows returned. A request that resolves no org "+
			"namespace is served an empty page, never an unscoped scan. Readable with an admin "+
			"token, a store-scoped token, or the anonymous published storefront key.")

	openapi.Describe("/v1/commerce/store/", http.MethodPost,
		"Create a storefront",
		"Creates a store from the body inside the caller org's own namespaced database, so the "+
			"row is physically isolated to that tenant from its first write, and answers it at 201 "+
			"with a Location header naming its id. Requires an admin or store-write token: the "+
			"anonymous published storefront key may READ stores but never create one. A body that "+
			"fails to decode is 400.")

	openapi.Describe("/v1/commerce/store/access", http.MethodGet,
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

	openapi.Describe("/v1/commerce/store/current", http.MethodGet,
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

	openapi.Describe("/v1/commerce/store/token", http.MethodPost,
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

	openapi.Describe("/v1/commerce/store/:storeid", http.MethodGet,
		"Fetch one storefront",
		"Reads the addressed store from the caller org's own namespaced database, so an id "+
			"belonging to another tenant is simply absent there and answers 404 rather than "+
			"leaking its existence. The body is the stored entity including its embedded listing "+
			"override map. Readable with an admin or store-read token and also with the anonymous "+
			"published storefront key, which is what lets a logged-out storefront resolve the "+
			"store it is rendering.")

	openapi.Describe("/v1/commerce/store/:storeid", http.MethodPut,
		"Replace a storefront outright",
		"This is a true REPLACEMENT, not a merge: the stored key is preserved but the body is "+
			"decoded onto a fresh entity, so every field the body omits is written back as its "+
			"zero value. Use the partial update when you mean to change part of a store. The id "+
			"is resolved inside the caller org's own namespace, so an unknown or foreign id is a "+
			"404 before anything is written. Requires an admin token, or one holding both store "+
			"read and store write.")

	openapi.Describe("/v1/commerce/store/:storeid", http.MethodPatch,
		"Change part of a storefront",
		"Loads the stored store and decodes the body over it, so only the fields the body names "+
			"change and everything else keeps its stored value — the difference from the full "+
			"replace, which clears what it is not told. Answers the merged entity. The id is "+
			"resolved inside the caller org's own namespace, so an unknown or foreign id is 404. "+
			"Requires an admin token, or one holding both store read and store write.")

	openapi.Describe("/v1/commerce/store/:storeid", http.MethodPost,
		"Method-override tunnel for clients that cannot send PUT, PATCH or DELETE",
		"Re-dispatches the request into the handler the intended verb would have reached, taking "+
			"that verb from a _method form value or query parameter and then from the "+
			"X-HTTP-Method-Override header, the header winning when both are present. Only PUT, "+
			"PATCH and DELETE are accepted; anything else resolves to 405. The trap is the "+
			"default: naming NO override at all is treated as a partial update, never as a "+
			"create. Authorization is whatever the underlying operation requires, since the real "+
			"handler runs.")

	openapi.Describe("/v1/commerce/store/:storeid", http.MethodDelete,
		"Delete a storefront, keeping a recoverable copy",
		"Removes the addressed store and answers 204 with no body. Before the live row goes, the "+
			"entity is written once more under a tombstone kind, so the deletion leaves a "+
			"recoverable copy rather than destroying the record outright; the store's listing "+
			"overrides live inside that row and go with it. The id is resolved inside the caller "+
			"org's own namespace, so an unknown or foreign id is 404. Requires an admin or "+
			"store-write token.")

	openapi.Describe("/v1/commerce/store/:storeid/trial", http.MethodPost,
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

	openapi.Describe("/v1/commerce/store/:storeid/bundle/:key", http.MethodGet,
		"Fetch a bundle as this storefront sells it",
		"Returns the stored bundle with the store's listing for it laid over the top — every "+
			"non-empty listing field wins, and the currency is forced to the store's own — so the "+
			"caller reads what this storefront actually sells rather than the catalog-wide "+
			"record. The overlay is keyed by the item's ID: a listing filed only under a slug or "+
			"SKU does not reach it, unlike the listing reads, which do fall back to those. An "+
			"unknown store or key is 404. Readable with an admin token or the anonymous published "+
			"storefront key.")

	openapi.Describe("/v1/commerce/store/:storeid/product/:key", http.MethodGet,
		"Fetch a product as this storefront sells it",
		"Returns the stored product with the store's listing for it laid over the top — non-empty "+
			"listing fields replace the catalog values and the currency is forced to the store's "+
			"own — which is what lets two storefronts sell the same catalog product at their own "+
			"price, name and media. The overlay is keyed by the product's ID, so a listing filed "+
			"only under a slug or SKU does not apply here. An unknown store or key is 404. "+
			"Readable with an admin token or the anonymous published storefront key.")

	openapi.Describe("/v1/commerce/store/:storeid/variant/:key", http.MethodGet,
		"Fetch a variant as this storefront sells it",
		"Returns the stored variant with the store's listing for it overlaid — non-empty listing "+
			"fields replace the catalog values and the currency is forced to the store's own — "+
			"which is what makes per-storefront pricing of a shared variant possible. The overlay "+
			"is keyed by the variant's ID, never by its slug or SKU. An unknown store or key is "+
			"404. Readable with an admin token or the anonymous published storefront key.")

	openapi.Describe("/v1/commerce/store/:storeid/listing", http.MethodGet,
		"The storefront's whole listing override map",
		"Returns every override this store applies to catalog items — name, price, list price, "+
			"media, availability and the hidden flag — keyed by product or variant id, in one "+
			"read. A listing is an OVERRIDE, not a product: the catalog item exists independently "+
			"and this map only says how this storefront presents it. Read from the caller org's "+
			"own namespaced database, so a store id belonging to another tenant is 404. Readable "+
			"with an admin token or the anonymous published storefront key.")

	openapi.Describe("/v1/commerce/store/:storeid/listing/:key", http.MethodGet,
		"Fetch one listing override, by item id or by its slug or SKU",
		"Looks the key up in the store's listing map first and, failing that, matches it against "+
			"each listing's slug and then its SKU — so a storefront holding only a product's URL "+
			"slug can still resolve the override. That fallback is unique to the listing reads; "+
			"the item overlay routes match by id alone. A key matching none of the three is 404, "+
			"as is a store id outside the caller org's namespace. Readable with an admin token or "+
			"the anonymous published storefront key.")

	openapi.Describe("/v1/commerce/store/:storeid/listing/:key", http.MethodPost,
		"Add a listing override under a new key",
		"Creates the override and answers the store's ENTIRE listing map at 201 with a Location "+
			"header — not just the entry that was added. A key already present is refused 400: "+
			"creation never silently overwrites, so changing an existing listing has to be an "+
			"explicit replace. The stored listing has its currency stamped from the store's own, "+
			"which the replace path does not do. The key is matched exactly here, with none of "+
			"the slug or SKU fallback the read allows. Admin-gated and resolved inside the caller "+
			"org's namespace.")

	openapi.Describe("/v1/commerce/store/:storeid/listing/:key", http.MethodPut,
		"Upsert a listing override",
		"Decodes the body over the existing listing when the key is present, so fields it omits "+
			"keep their stored values, and builds the listing from the body alone when the key is "+
			"new. Answers 200 when it replaced something and 201 with a Location header when it "+
			"created it; either way the body is the store's entire listing map, not the single "+
			"entry. Unlike creation, this path does NOT restamp the listing's currency from the "+
			"store. Admin-gated, with the store resolved inside the caller org's namespace.")

	openapi.Describe("/v1/commerce/store/:storeid/listing/:key", http.MethodPatch,
		"Confirm a listing override exists and re-save the store",
		"Requires the key to already be present — an absent one is 404 — and answers the store's "+
			"listing map at 200. Read the behaviour before relying on it: the decoded body is "+
			"applied to a COPY taken out of the map and is never assigned back, so the stored "+
			"listing is unchanged and the map returned is exactly the map that was already there. "+
			"An actual edit to an existing listing has to go through the upsert, which does write "+
			"its result back into the store. A body that fails to decode is still 400. "+
			"Admin-gated and namespaced to the caller's org.")

	openapi.Describe("/v1/commerce/store/:storeid/listing/:key", http.MethodDelete,
		"Remove a listing override",
		"Drops the key from the store's listing map and re-saves the store, answering 204 with no "+
			"body. It UN-OVERRIDES rather than deletes: the product, variant or bundle itself is "+
			"untouched and simply reverts to its catalog values on this storefront. A key that is "+
			"not present is 404, and so is a store id outside the caller org's namespace. "+
			"Admin-gated.")
}

// ---- /v1/commerce/store checkout — the two-step and one-step payment flows ----
//
// The /checkout-prefixed addresses bind the SAME handlers as their shorter
// siblings: the prefix is the newer spelling of one operation, not a second
// behaviour. Each is described on its own terms because a reader lands on one
// address at a time, and the alias is named where it is the thing they would
// otherwise get wrong.

func describeCheckout() {
	openapi.Describe("/v1/commerce/store/:storeid/authorize", http.MethodPost,
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

	openapi.Describe("/v1/commerce/store/:storeid/authorize/:orderid", http.MethodPost,
		"Authorize an order that already exists, holding the funds without settling them",
		"Continues the order named in the path rather than minting a new one, holding funds for "+
			"it. The order is loaded from the caller org's own store, so an id belonging to "+
			"another tenant is a 404. The rule most callers get wrong is that the body's order "+
			"object is MERGED onto the loaded order before the tally — this is not a read-only "+
			"reference, and a field sent here overwrites what is stored. The gate, the store "+
			"resolution and the currency override behave exactly as on the bodiless-id sibling, "+
			"and settling is still the capture call's job.")

	openapi.Describe("/v1/commerce/store/:storeid/capture/:orderid", http.MethodPost,
		"Capture a previously authorized order and settle the payment",
		"Settles the order named in the path — the second half of the two-step flow — and answers "+
			"the updated order with a Location header. Dispatch follows the order's STORED payment "+
			"type, and a successful capture is the moment the rest of the system learns about the "+
			"sale: order and payment rows are updated, coupon redemptions, referral, cart and "+
			"stats are written, the confirmation email goes out, and the paid and completed events "+
			"are emitted. A capture failure releases the order's inventory reservations and "+
			"answers 400, so a failed settlement never leaves items held.")

	openapi.Describe("/v1/commerce/store/:storeid/charge", http.MethodPost,
		"Authorize and capture a new order in one call",
		"Runs authorization and capture back to back against a freshly created order — the "+
			"one-step flow for callers with no reason to hold funds. It takes the authorize body "+
			"and inherits every authorize rule: the store's currency wins over the body, the items "+
			"are reserved before the processor is called, and the amount bounds the processor "+
			"enforces still apply. There is no order id on this address, so it can never continue "+
			"an existing order. Either half failing answers 400, and the capture side effects — "+
			"confirmation email, redemptions, stats, the paid and completed events — run only when "+
			"both halves succeed.")

	openapi.Describe("/v1/commerce/store/:storeid/paypal/pay", http.MethodPost,
		"Start a PayPal authorization for a new order",
		"Runs the ordinary store authorize flow — the route binds that very handler, so the body, "+
			"the store resolution, the tally, the reservations and the failure behaviour are the "+
			"authorize address's, unchanged. It reaches PayPal only when the body's payment type "+
			"says so; nothing about this path forces the processor, so a card-typed payment posted "+
			"here authorizes on the card processor instead. A successful PayPal authorization "+
			"stamps a pay key onto the payment, which is the key the confirm and cancel addresses "+
			"filter on. It is the older entry point; the plain authorize address is the one to "+
			"build against.")

	openapi.Describe("/v1/commerce/store/:storeid/paypal/confirm/:payKey", http.MethodPost,
		"PayPal confirm by pay key — refuses, because a pay key alone does not identify the order",
		"Intended to mark every payment carrying the given pay key as paid and flip the order to "+
			"paid, it cannot do that from this address and does not pretend to: the shared "+
			"checkout handler resolves its order from an ORDER ID path parameter that this route "+
			"does not carry, so it always works against a fresh untyped order and the confirm "+
			"dispatch refuses it with 400 before the pay key is ever queried. The token gate, the "+
			"namespace and the store lookup all run ahead of that, so a missing token is still "+
			"401 and an unloadable store still 500. Drive a PayPal return through an address that "+
			"carries the order id.")

	openapi.Describe("/v1/commerce/store/:storeid/paypal/cancel/:payKey", http.MethodPost,
		"PayPal cancel by pay key — refuses, because a pay key alone does not identify the order",
		"Intended to void the payments carrying the given pay key, stamp them cancelled and "+
			"cancel the order, it never reaches that work: the shared checkout handler reads its "+
			"order from an ORDER ID path parameter this route does not carry, leaving an untyped "+
			"order that the cancel dispatch refuses with 400 before the pay key lookup runs. "+
			"Authentication, namespacing and store resolution happen ahead of the refusal, so a "+
			"missing token is 401 and an unloadable store 500. Cancelling a real PayPal "+
			"authorization needs an address that carries the order id.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/authorize", http.MethodPost,
		"Authorize a new order against a storefront, holding the funds — the checkout spelling",
		"Authorizes a new order for the addressed store and holds the funds, answering the saved "+
			"order with a Location header. It binds the identical handler as the shorter authorize "+
			"address, so the two are ONE operation at two spellings and not two behaviours; the "+
			"checkout prefix is the newer one. Every rule carries over: admin or published scope "+
			"on the token, the store loaded first with its currency overriding the body, items "+
			"reserved before the processor call, and reservations released with the order "+
			"persisted cancelled on failure. Nothing is settled here.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/authorize/:orderid", http.MethodPost,
		"Authorize an existing order, holding the funds — the checkout spelling",
		"Continues the order named in the path rather than minting one, and shares its handler "+
			"byte for byte with the unprefixed authorize-by-id address. The order is loaded from "+
			"the caller org's own store, so another tenant's id is a 404, and the body's order "+
			"object is merged onto the loaded row before the tally — a field sent here overwrites "+
			"what is stored. Store resolution, the token gate and the currency override behave as "+
			"on every other authorize address; settle with the capture address and the same order "+
			"id.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/capture/:orderid", http.MethodPost,
		"Capture a previously authorized order and settle it — the checkout spelling",
		"Settles the authorized order named in the path and answers the updated order with a "+
			"Location header, running the same handler as the unprefixed capture address. Dispatch "+
			"follows the order's stored payment type. Success is what triggers the downstream "+
			"work — order and payment updates, redemptions, referral, cart and stats, the "+
			"confirmation email, and the paid and completed events — while a failure releases the "+
			"order's inventory reservations and answers 400.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/charge", http.MethodPost,
		"Authorize and capture a new order in one call — the checkout spelling",
		"Performs authorization and capture back to back against a newly created order for the "+
			"addressed store, on the same handler as the unprefixed charge address. It takes the "+
			"authorize body and inherits every authorize rule, including the store's currency "+
			"winning over the body and the items being reserved before the processor is called. "+
			"There is no order id on this address, so it can never continue an existing order. "+
			"Either half failing answers 400, and the capture side effects run only when both "+
			"succeed.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/paypal/pay", http.MethodPost,
		"Start a PayPal authorization for a new order — the checkout spelling",
		"Begins a PayPal authorization by running the ordinary store authorize flow, since the "+
			"route binds that exact handler — body, store resolution, tally, reservations and "+
			"failure behaviour are the authorize address's, unchanged. The processor is chosen "+
			"from the body's payment type, so this path reaches PayPal only when that type says "+
			"so. A successful PayPal authorization stamps a pay key onto the payment, which is the "+
			"key the confirm and cancel addresses filter on. Build against the plain authorize "+
			"address instead.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/paypal/confirm/:payKey", http.MethodPost,
		"PayPal confirm by pay key — refuses, exactly as the unprefixed address does",
		"Meant to mark the payments carrying the given pay key as paid and set the order to paid, "+
			"it cannot reach that work from this address: the shared checkout handler takes its "+
			"order from an ORDER ID path parameter this route does not carry, so the order is "+
			"always fresh and untyped and the confirm dispatch refuses with 400 before the pay key "+
			"is queried. The token gate, the namespace middleware and the store lookup all run "+
			"ahead of that, so authentication and store failures surface first. Behaviour is "+
			"identical to the unprefixed confirm address; the checkout prefix changes nothing "+
			"here.")

	openapi.Describe("/v1/commerce/store/:storeid/checkout/paypal/cancel/:payKey", http.MethodPost,
		"PayPal cancel by pay key — refuses, exactly as the unprefixed address does",
		"Meant to void the payments carrying the given pay key, stamp them cancelled and cancel "+
			"the order, but the shared checkout handler resolves its order from an ORDER ID path "+
			"parameter this route does not carry. The result is an untyped order and a cancel "+
			"dispatch that refuses with 400 before the pay key lookup ever runs. Token gate, "+
			"namespacing and store resolution happen first, so a missing token is still 401 and an "+
			"unloadable store still 500. It is the same handler as the unprefixed cancel address, "+
			"with the same outcome.")
}

// ---- /v1/commerce/<kind> — the merchant resource table ----
//
// Seventeen kinds, seven operations each, and ONE handler behind all 119.
// commerce's generic REST scaffold (util/rest) binds create, list, get, replace,
// patch, delete and a method-override tunnel for every model handed to it, so
// these are not seventeen behaviours that happen to resemble each other — they
// are one behaviour, and the kind supplies what the row IS and who may touch it.
// The mechanics are therefore written once and composed with the kind, because
// the alternative is seventeen hand-copied paragraphs describing one generator,
// which is the drift DescribeRest already exists to prevent one level down.
//
// Three facts hold for all 119, so they are stated here instead of seventeen
// times below:
//
//   - EVERY ROW IS THE CALLER'S OWN. The scaffold builds each entity against the
//     caller org's own namespaced store, keyed by the namespace resolved from
//     the gateway-validated X-Org-Id. Reads and writes are both inside that
//     store, so another tenant's row is not withheld by a policy check — it is
//     not there at all. That is why every miss below is 404 and never 403.
//   - EVERY JSON FIELD IS CLIENT-WRITABLE. Create, replace and patch decode the
//     request body straight onto the model with no per-field allowlist. Where a
//     field is derived, ignored or overwritten anyway, the kind says so.
//   - THE PER-KIND PERMISSION TABLE IS NOT UNIVERSAL, and this is the one most
//     worth knowing. util/rest carries scopes for collection, product, return,
//     subscriber and variant only. For the other twelve the scaffold finds no
//     entry, logs that it is skipping the check, and ALLOWS — so on those kinds
//     the route's own gate is the entire authorization story. Each kind says
//     which of the two it is.
type merchantKind struct {
	kind      string // URL segment; the id parameter is <kind>id
	noun      string // singular, for the summaries
	plural    string // plural, for the list summary
	what      string // what the row IS — the sentence a caller needs before any verb
	sortBy    string // the field the list orders by when sort is not given
	scope     string // per-kind permission scope; "" when the table has no entry
	admin     bool   // the route demands the ADMIN permission, not merely a valid token
	paywalled bool   // the route also runs the commerce-admin entitlement gate
}

// gate is the authorization sentence shared by all seven of a kind's operations:
// who reaches the route at all, and — for the paywalled kinds — what the org must
// hold to be admitted past it.
func (m merchantKind) gate() string {
	s := "Any valid access token reaches it."
	if m.admin {
		s = "The token must carry the ADMIN permission; an ordinary access token is refused."
	}
	if m.paywalled {
		s += " The org must also be entitled to the commerce admin: the paywall answers 402 " +
			"subscription_required unless the org holds an active or trialing pro subscription, a live " +
			"trial credit or a redeemed invite, and 503 when that entitlement cannot be read rather " +
			"than admitting on an unknown. The internal service token and a platform superadmin pass " +
			"straight through."
	}
	return s
}

// also states the SECOND check — the per-kind scope the scaffold applies on top of
// the route's gate — or says plainly that this kind has none, which is a fact about
// the authorization a caller would otherwise have to assume.
func (m merchantKind) also(need string) string {
	if m.scope == "" {
		return " The per-kind permission table has no entry for " + m.kind + ", so the scaffold skips " +
			"that second check with a warning and the gate above is the whole authorization story."
	}
	return " The token must also carry " + need + "."
}

func (m merchantKind) describe() {
	root := "/v1/commerce/" + m.kind + "/"
	one := root + ":" + m.kind + "id"
	write := "Admin or Write" + m.scope
	edit := "Admin, or Read" + m.scope + " and Write" + m.scope + " together"

	openapi.Describe(root, http.MethodGet,
		"List your org's "+m.plural+", as a page",
		m.what+" Answers a pagination envelope — the page and display echoed back, the rows under "+
			"models, a total count and a facets array — read from the caller org's own namespaced "+
			"store, so one tenant can never list another's. Sorting defaults to "+m.sortBy+" and is "+
			"overridable with sort. display is the page size and page applies only alongside it; "+
			"either one that is not a positive integer is refused with 500 rather than silently "+
			"ignored, and the limit query overrides the reported COUNT only, never the rows returned. "+
			"No search backend is wired, so the datastore is the one and only list path and facets is "+
			"always empty. A request resolving no org namespace is served an EMPTY page rather than "+
			"an unscoped scan: the namespace IS the tenant filter, so without one there is nothing "+
			"safe to return. "+m.gate()+m.also("Admin or the "+m.scope+" list scope"))

	openapi.Describe(root, http.MethodPost,
		"Create a "+m.noun,
		m.what+" Decodes the body into a new row in the caller org's own namespaced store — isolated "+
			"to that tenant from its first write — and answers the stored row at 201 with a Location "+
			"header naming its id. The id is assigned by the store, not taken from the body. A body "+
			"that fails to decode is 400 and a store that refuses the write is 500. "+
			m.gate()+m.also(write))

	openapi.Describe(one, http.MethodGet,
		"Fetch one "+m.noun,
		m.what+" Reads the addressed row from the caller org's own namespaced store. An id that is "+
			"not there is 404 — and another tenant's id is not there by construction, so it reads "+
			"exactly like a typo instead of confirming the row exists somewhere else. "+
			m.gate()+m.also("Admin or Read"+m.scope))

	openapi.Describe(one, http.MethodPut,
		"Replace a "+m.noun+" outright",
		m.what+" This is a true REPLACEMENT, not a merge: the stored row's key is preserved, but the "+
			"body is decoded onto a FRESH entity, so every field the body omits is written back as "+
			"its ZERO value. Patch is the verb for changing part of a row. The id is resolved inside "+
			"the caller org's own namespace and an absent one is 404 before anything is written; a "+
			"body that fails to decode is 400. Answers the stored result. "+m.gate()+m.also(edit))

	openapi.Describe(one, http.MethodPatch,
		"Change part of a "+m.noun,
		m.what+" Loads the stored row and decodes the body OVER it, so only the fields the body names "+
			"change and everything else keeps its stored value — the difference from the full "+
			"replace, which clears what it is not told. Answers the merged row. An id absent from the "+
			"caller org's namespace is 404 and a body that fails to decode is 400. "+
			m.gate()+m.also(edit))

	openapi.Describe(one, http.MethodPost,
		"Method-override tunnel for a "+m.noun+" — for clients that cannot send PUT, PATCH or DELETE",
		m.what+" Re-dispatches the request into the handler the intended verb would have reached, "+
			"taking that verb from a _method form value or query parameter and then from the "+
			"X-HTTP-Method-Override header. PUT replaces the row, PATCH changes part of it, DELETE "+
			"removes it, and anything else is 405. The trap is the DEFAULT: naming no override at all "+
			"leaves the method POST, which this tunnel maps to the PARTIAL UPDATE — it is never a "+
			"create, and creating is the collection root's job. Behaviour and authorization are the "+
			"underlying operation's, since the real handler runs. "+m.gate())

	openapi.Describe(one, http.MethodDelete,
		"Delete a "+m.noun+", keeping a recoverable copy",
		m.what+" Removes the addressed row and answers 204 with no body. Before the live row goes it "+
			"is written once more under a deleted tombstone kind, so a deletion leaves a recoverable "+
			"copy rather than destroying the record outright — and a tombstone that cannot be written "+
			"fails the call with 500 before anything is removed. The id is resolved inside the caller "+
			"org's own namespace, so an absent or foreign id is 404. "+m.gate()+m.also(write))
}

func describeMerchant() {
	for _, m := range merchantKinds {
		m.describe()
	}
}

var merchantKinds = []merchantKind{{
	kind: "collection", noun: "collection", plural: "collections",
	sortBy: "the slug", scope: "Collection", paywalled: true,
	what: "A collection is a merchandising group a storefront renders — a slug and name, copy and " +
		"media, flat lists of the product and variant ids it holds, published, preorder and " +
		"out-of-stock flags, and an availability window. Membership lives on the collection as those " +
		"id lists rather than as a join, so putting a product into a collection is a write here and " +
		"not on the product.",
}, {
	kind: "discount", noun: "discount", plural: "discounts",
	sortBy: "the last-updated time", paywalled: true,
	what: "A discount is a price rule: a type (flat, percent, free-shipping, free-item or bulk), a " +
		"window, a scope naming the store, collection, product or variant it applies to, a target, " +
		"and rules pairing a trigger — a price or quantity threshold — with an action, an amount off " +
		"or a percentage. It is ENABLED BY DEFAULT, so a bare create makes a live discount rather " +
		"than a draft. The rule engine caches per replica for about thirty seconds, so a discount " +
		"switched off here can keep applying briefly on other replicas.",
}, {
	kind: "disclosure", noun: "disclosure", plural: "disclosures",
	sortBy: "the last-updated time",
	what: "A disclosure is a published-document record — a publication body, a content hash, a type " +
		"and a named receiver. The hash LOOKS like a field you set and is in fact derived, but only " +
		"on update: a freshly created disclosure keeps whatever hash the caller sent until the first " +
		"replace or patch recomputes it, so a new row's hash attests to nothing. This kind lives in " +
		"commerce's demo tree — a live writable resource in your tenant's real store that nothing " +
		"else in commerce reads.",
}, {
	kind: "movie", noun: "movie", plural: "movies",
	sortBy: "the slug",
	what: "A movie is a film catalog record — a slug plus EIDR and IMDB ids, all three required, with " +
		"title and synopsis copy, artwork, screenshots, trailers, cast and crew, and available and " +
		"hidden flags. It carries NO price: the money for a film lives on the product that sells it.",
}, {
	kind: "note", noun: "note", plural: "notes",
	sortBy: "the last-updated time",
	what: "A note is a timestamped free-text log line — a caller-supplied time, a source, a message " +
		"and an enabled flag. That time is the caller's own field and is distinct from the row's " +
		"creation stamp; the note search filters on it, so a note written without one is a zero-time " +
		"note the ops log will never surface.",
}, {
	kind: "product", noun: "product", plural: "products",
	sortBy: "the slug", scope: "Product", paywalled: true,
	what: "A product is a sellable catalog item: slug, SKU and UPC, name and copy, media, " +
		"availability and preorder flags, a reservation block, and its money — currency, price, " +
		"MSRP, list price and inventory cost in minor units, inventory count, taxability, and the " +
		"subscription interval when it is subscribeable. Its variants and options are carried as a " +
		"denormalized JSON snapshot inside the product, separate from the standalone variant rows, " +
		"and nothing keeps the two in step for you.",
}, {
	kind: "return", noun: "return", plural: "returns",
	sortBy: "the last-updated time", scope: "Return",
	what: "A return is an RMA — the store, user and order it belongs to, the line items coming back, " +
		"a fulfillment block carrying its own type, status and pricing, a summary, and eight " +
		"lifecycle timestamps from submitted through delivered and processed. Its status is a FREE " +
		"STRING with no enumeration behind it, and there is no refund amount on the return itself: " +
		"the money sits inside the line items and the fulfillment pricing.",
}, {
	kind: "saleschannel", noun: "sales channel", plural: "sales channels",
	sortBy: "the last-updated time", paywalled: true,
	what: "A sales channel is a named selling surface — a name, a description, a disabled flag and " +
		"metadata. The flag is NEGATIVE, so a channel created from an empty body is enabled. Nothing " +
		"on this row links products, prices or stock to the channel; here it is a label other " +
		"surfaces scope themselves by.",
}, {
	kind: "stocklocation", noun: "stock location", plural: "stock locations",
	sortBy: "the last-updated time", paywalled: true,
	what: "A stock location is a physical address inventory can be held at — a name, street lines, " +
		"city, province, country, postal code and a phone. None of it is validated, there are no " +
		"coordinates, and the row carries no enabled flag and no inventory link, so deleting it is " +
		"the only way to retire one.",
}, {
	kind: "submission", noun: "submission", plural: "submissions",
	sortBy: "the last-updated time",
	what: "A submission is one filled-in form from a site visitor — an email, an optional user id, the " +
		"client details the server observed (user agent, referer, geography) and the form's own " +
		"fields as free metadata. It carries no form id, so the link back to the form that produced " +
		"it is not stored on the row.",
}, {
	kind: "subscriber", noun: "subscriber", plural: "subscribers",
	sortBy: "the last-updated time", scope: "Subscriber",
	what: "A subscriber is a mailing-list member — name, email, the form id that captured them, " +
		"unsubscribed state and date, client details, tags and metadata. Writing one FIRES A " +
		"WEBHOOK: subscriber.created on create and subscriber.updated on replace or patch, emitted " +
		"BEFORE the write is known to have succeeded and carrying the row as sent, so the payload " +
		"holds the raw email rather than the normalized one that gets stored.",
}, {
	kind: "tokentransaction", noun: "token transaction", plural: "token transactions",
	sortBy: "the last-updated time",
	what: "A token transaction records a transfer between two identified parties — amount and fees, a " +
		"timestamp, sending and receiving addresses, names, user ids, states and countries, a flag " +
		"per side, a protocol name and a transaction hash. Nothing here touches a chain: the hash is " +
		"an unvalidated string and the flags are plain writable booleans with no screening behind " +
		"them. Amounts are floating-point rather than the exact minor units every real money field " +
		"in commerce uses, and there is no currency field at all — this kind lives in commerce's " +
		"demo tree, so it is a live writable resource in your tenant's store that nothing else in " +
		"commerce reads, and it must never carry real money.",
}, {
	kind: "transfer", noun: "transfer", plural: "transfers",
	sortBy: "the last-updated time", admin: true,
	what: "A transfer records that a payable WAS PAID — the annotation a human writes after paying " +
		"out of band. Commerce executes no payout: creating one moves no money, and it marks the " +
		"referenced payable settled. It carries the payable and payee ids, the amount it settles and " +
		"the amount actually sent (which may be a different asset), a type of eth, wire or other, " +
		"the transaction hash or wire reference, when it was paid and who recorded it; amounts are " +
		"exact decimal strings with an asset, not cents. It is admin-gated because writing one " +
		"settles money we owe, and nothing enforces uniqueness on the reference — so posting the " +
		"same transfer twice settles the payable twice.",
}, {
	kind: "variant", noun: "variant", plural: "variants",
	sortBy: "the SKU", scope: "Variant", paywalled: true,
	what: "A variant is one purchasable SKU of a product — its product id, SKU and UPC, name, media, " +
		"availability, the option name and value pairs that distinguish it, a sold counter, and its " +
		"own money and stock: currency, price, MSRP, inventory cost, inventory count and taxability. " +
		"Inventory and sold are plain writable numbers with no decrement logic behind them here. The " +
		"same variant also exists as a JSON copy inside its product, and writing one does not update " +
		"the other.",
}, {
	kind: "wallet", noun: "wallet", plural: "wallets",
	sortBy: "the last-updated time", admin: true,
	what: "A wallet is a container of custodial blockchain accounts, and its only field is that " +
		"account list — each account carrying a name, an address, a chain type, and the ENCRYPTED " +
		"private key with its salt. Creating a wallet through this table generates NO KEYS: key " +
		"generation lives on the account routes, so a wallet made here is an empty shell and an " +
		"account posted into one is stored exactly as sent, with no key generation and no validation " +
		"behind it. Know what a read renders: the plaintext private key is never marshalled and " +
		"never stored, but the encrypted blob and its salt ARE returned, so whoever can read a " +
		"wallet can attack it offline down to the strength of the owner's passphrase. That is why " +
		"this kind is admin-gated.",
}, {
	kind: "watchlist", noun: "watchlist", plural: "watchlists",
	sortBy: "the last-updated time",
	what: "A watchlist is a viewer's saved list of movies — a user id, an email, and the movies " +
		"themselves. It stores WHOLE MOVIE SNAPSHOTS rather than movie ids, so a list goes stale the " +
		"moment a film record changes and grows without bound as it fills.",
}, {
	kind: "webhook", noun: "webhook", plural: "webhooks",
	sortBy: "the last-updated time", admin: true,
	what: "A webhook is a merchant-registered endpoint that receives commerce event callbacks — a " +
		"name, a URL, live and all flags, a per-event map, an enabled flag, and the shared access " +
		"token each delivery posts IN THE BODY. Two things to know before registering one: that " +
		"token is a plainly readable field, so anyone who may read webhooks reads every endpoint's " +
		"secret, and delivery consults only the all flag and the event map — it does NOT consult " +
		"enabled or live, so setting enabled false does not stop delivery and deleting the row is " +
		"the only thing that does. Delivery is a single POST with a twenty-second timeout and no " +
		"retry.",
}}
