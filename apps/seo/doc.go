// Package seo is search visibility as data: what a phrase is worth, what a site
// already places for, who places beside it, who links to it, and what one page
// gets wrong.
//
// Six questions, six calls, one address:
//
//	seoKeyword     what named phrases are searched, and what a click on one costs
//	seoIdea        the phrases nobody named yet, grown from a seed
//	seoRank        every phrase a domain already places for, with its position
//	seoCompetitor  the domains that place for the same phrases
//	seoBacklink    who links to a target, and how much of that is broken or spam
//	seoAudit       one page, fetched and checked
//
// and a seventh that prices the other six (seoRate).
//
// # This is a resale, and the price is not ours to invent
//
// The measurements come from DataForSEO. A resale has exactly one number in it —
// theirs — and the failure mode of a reseller is a copy of that number going stale
// in a table somebody has to remember to edit. So no table is kept. The vendor
// publishes two numbers about every call and BOTH are read from them at run time:
//
//	the QUOTE   their price list, served free at /v3/appendix/user_data and
//	            cached for an hour. It is what a call is expected to cost, so it
//	            is what the caller's balance is authorized against BEFORE the
//	            call, and it is what seoRate publishes.
//	the CHARGE  the `cost` field on the answer itself. It is what they actually
//	            billed — per-row scaling already applied — so it is what the
//	            ledger debits AFTER the call.
//
// One rule, stated twice because there are two moments: the number is the
// vendor's. A price change on their side moves ours within the hour with no
// redeploy, no migration, and nothing to notice. A margin would be a second
// number, and a second number is a table again; margin belongs in the plan a
// customer buys, not in the proxy that spends.
//
// The charge is exact to 18 decimals (money.Amount). Their cheapest call is
// $0.00012 and cents cannot hold it — rendered in cents it rounds to zero, and a
// zero charge is a call that was free and a spend cap that never saw it.
//
// # The credential is the deployment's, and it is read, never held
//
// One DataForSEO account serves every tenant, so the credential is the
// deployment's own, sealed in KMS at orgs/hanzo/seo/DATAFORSEO_{LOGIN,PASSWORD}
// in env prod, and resolved through deps.KMS at call time — never an environment
// variable, never a literal, never logged. It is cached for five minutes so a
// request is not a store read, which is short enough that a rotation takes effect
// without a restart. Every error names the REF and never the value: a ref is a
// path and is safe to say out loud.
//
// # Why six typed ops and not one passthrough
//
// A single op taking the vendor's body verbatim would be one MCP tool no model
// can call and one SDK method whose argument is `any` — the operation id IS the
// tool name, so collapsing six questions into one destroys the only thing that
// makes them findable. The six are questions, not endpoints: each has its own
// small typed request and its own small typed answer, and several vendor
// endpoints fold into each.
//
// What is NOT typed is the vendor's full row, which is sixty fields wide and
// grows. Each answer carries the handful that ARE the product — the phrase, the
// volume, the position, the link count — normalised where the vendor is
// inconsistent about itself: competition arrives as an index out of 100 from one
// endpoint and a fraction from another, and is published here as the fraction
// both times. A caller who needs the sixtieth field wants a different product.
//
// # What this package does not do
//
// It runs no crawl and stores nothing. Every op is one live vendor call answered
// in the same request — no task ids, no polling, no per-tenant state, no store to
// open. The full site audit (a crawl with a job lifecycle) is deliberately absent:
// it is a job, jobs need a plane, and seoAudit answers the same questions about
// the one page a caller is actually looking at.
package seo
