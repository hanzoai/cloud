// Package payout is what the referral, affiliate and author programs ask of the
// money plane, and it is a QUESTION: what has this org spent? That read is the
// qualify signal / accrual base. It was three byte-identical commerce.go copies;
// extracted here so the commerce binding lives exactly ONCE.
//
// IT NO LONGER DEPOSITS. It carried a Deposit — the ONE money-in primitive all three
// programs shared — which is how a GET on three surfaces came to mint platform
// credit. Earnings are PAYABLES now: each program ACCRUES and RECORDS what is owed,
// and a human settles it out of band. Platform credit is issued only by an admin
// grant (apps/admin/core.ApplyGrant). TestClientIsReadOnly fails if a write returns.
//
// # It used to ask over HTTP, and it never once got an answer
//
// This was an *http.Client aimed at GET /v1/billing/usage/rollup, sent through the
// commerce transport. That transport does not reach a
// network when commerce is co-resident: it dispatches the request back into this
// binary's own router BY PATH, and /v1/billing/usage/rollup is registered nowhere
// here — commerce's own api.Route() bundle is behind //go:build cloud and is never
// compiled in. So the read was a 404 wearing an upstream failure's clothes. Split
// into per-app binaries it failed differently and worse: the base URL is empty in
// every process but commerce's, so the client reported itself "not configured" and
// answered ZERO. Referrals qualified nobody, affiliates accrued nothing, authors
// were paid nothing — silently, for as long as that shape shipped.
//
// It asks the ledger BY NAME now (client.FinanceSpend). There is no URL, no service
// token and nothing for a deployment to configure, so there is no configuration
// that can be wrong.
//
// Commerce is an INTERFACE so each program's store/handler logic stays testable
// with a fake ledger; Client is the ONE production binding. A program keeps its
// own narrow (unexported-method) client and a thin adapter delegating to Client —
// Go package-scoped interface methods can't cross packages, and the adapter is
// where a program still names its own grant tag.
package payout
