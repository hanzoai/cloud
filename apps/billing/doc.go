// Package billing is your org's balance, what it has spent, and the cards it pays with.
//
// It is the customer's own money surface, serving the org-scoped
// /v1/billing/{balance,usage,usage/accounts,ledger} reads. It does not own the
// prefix whole: commerce serves the merchant half of /v1/billing/* (invoices,
// subscriptions, alerts, methods, webhooks) from the store it keeps.
//
// It answered a second prefix once. /v1/finance was six more reads of this same
// wallet under another name — balance and usage were these reads respelled,
// credits, invoices and payment-methods were addresses commerce already serves —
// so HIP-0139 §7 closed it, and closing a shared address by fold means the
// duplicate is deleted rather than moved. What survived is the ledger read
// (ledger.go), at /v1/billing/ledger, where nobody else answers.
//
// WHY THIS EXISTS. On the console host (console.hanzo.ai) the ingress routes
// /v1/* straight to cloud-api:8000 — the console's Next BFF is reached only at
// "/". So the console's /v1/billing/usage + /v1/billing/balance calls land HERE,
// on cloud-api, NOT on the console's per-tenant commerce proxy. cloud-api
// previously wired commerce billing ONLY under the admin-gated aggregate
// (apps/admin, /v1/admin/*), so a normal org owner (e.g. davelorenzini /
// maxpower) hitting /v1/billing/usage had NO customer route and was denied — the
// "Access required" wall on every product overview + o11y usage panel. This adds
// exactly the customer surface those calls need.
//
// TENANT ISOLATION (the whole point). The org is the VALIDATED IAM owner claim
// (principal.Org — the trusted X-Org-Id the identity middleware minted from the
// caller's verified session/bearer, HIP-0026; NEVER a client-supplied header). A
// customer therefore reads ONLY their OWN org's ledger. The commerce billing
// subject is pinned server-side to that org and NO client-supplied subject/org
// query param is ever forwarded, so the browser cannot widen scope. This is the
// per-org READ twin of the admin god-view (apps/admin) — the SAME commerce S2S
// machinery, but scoped to the caller instead of all-orgs (which stays admin-only).
//
// SUBJECT. Prepaid balance is per-ORG: commerce keys the wallet under the BARE org
// slug as the `user` subject + the trusted `X-Org-Id` (admin.orgSubject /
// metering identityFromCtx — verified live: user=<org> + X-Org-Id=<org> returns
// the real wallet, "org/user" reads an empty one). The gateway debits this SAME
// key, so a read here shows exactly what the org is charged.
//
// TWO BACKENDS, ONE WIRE. Balance and usage read the co-resident native ledger
// DIRECTLY when finance is published (balance.go, usage_coresident.go) — which it always
// is in the unified binary — and fall back to the commerce S2S proxy only on a split
// deploy. Either way the wire is commerce's: the console's normalizeUsageRecords parses
// the RAW per-request ledger ({usage:[{transactionId,amount,metadata,createdAt}]}) and
// balance is the raw {balance,holds,available} cents object, so the proxy path forwards
// commerce's body + status VERBATIM and never rolls up (the rollup is the admin
// aggregate's job).
package billing
