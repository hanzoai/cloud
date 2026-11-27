// Package marketplace is the one market between orgs: agents and personas to
// hire, apps, skills, MCP servers and tools to buy, each sold by the org that owns
// it, and the two-sided work one org buys from another.
//
// It serves at /v1/marketplace the public shop (shop.go), an org's listings and
// the proof that it owns what each sells (listings.go), install per org/project,
// jobs cleared by /v1/principal and paid once over x402 (jobs.go), feedback only
// from a settled job's parties (feedback.go), a seller's standing in one read and
// its proved payout wallet (seller.go), and the Lux agent contracts' events
// (chain.go). It never moves money itself: x402 does, from the price table this
// package owns (payments.go), and Hanzo never holds a job's funds.
//
// Install is the tool plane's activation write (marketplace install == tool
// activation — one store, one truth), asked of the tool plane's own process over
// the plane (tools_list, tools_activate, tools_owned in apps/tools/market.go): the
// registry is never in this process, and reading it here was an empty registry —
// nothing to discover, every publish an unknown tool, every install a failure.
//
// TWO TRANSPORTS, ONE POLICY — because the shipped topology has no co-residency.
// The three clients this package binds are process-globals (x402.reg, tools.std, and
// wallets' mounted singleton), so they bind within ONE process, and the fleet runs
// ONE PROCESS PER APP. That is not a possible future deployment, it is the only one:
// manifest/apps.go declares marketplace, tools, x402 and wallets as four ordinary
// prefix-routed rows (only `zen` is Coresident, manifest/apps.go:194); the Dockerfile
// builds a plugin binary per row and cmd/cloud loads each as its own CHILD PROCESS
// (cmd/cloud/main.go:257, zip.Load on a binary path); and the fused monolith that
// linked every subsystem was deleted (cmd/cloud/main.go:1-9).
//
// So the wiring above bound nothing in production, and a listed tool was not merely
// unbuyable — it was FREE. The tools process refuses a dispatch whose registry ROW
// declares a price, but a marketplace price lives in the listing store, so a listing
// on a tool that declares none was dispatched for nothing.
//
// The fix is the internal plane (resource_billing_peer.go is the precedent: the same
// split turned every priced create free, and the answer was to ASK the owning
// process). Each op is served by the process that owns the answer:
//
//	tools → x402         x402_settle                  settle this tool call   (apps/x402/rpc.go)
//	x402  → marketplace  market_price                 what it costs, who is paid (rpc.go here)
//	x402  → wallets      wallets_payee                resolve the payee wallet (apps/wallet/rpc.go)
//	x402  → commerce     finance_draw, finance_credit  the payer's half and the payee's (apps/commerce/credit_rpc.go)
//	market → x402        x402_authorize, x402_void    hold a job's payment, or give it up (jobs.go)
//	x402  → commerce     finance_hold, finance_return  set the amount aside, or give it back
//
// The in-process client stays the FAST PATH where the owner is co-resident; the plane
// answers where it is not. Both are the same policy and both fail closed, which is
// what payments_test.go (one process) and split_test.go (five real processes) assert
// against each other.
//
// Surface (/v1 only). The shop is public; everything else is the caller's org's.
//
//	GET    /v1/marketplace                  discovery: the caller's tools, with any public listing's overlay
//	GET    /v1/marketplace/shop             the one catalog: every public listing, searched, faceted, paged
//	GET    /v1/marketplace/shop/:id         one public listing, with its seller and reputation
//	GET    /v1/marketplace/listings         the caller org's own listings
//	POST   /v1/marketplace/listings         publish a listing of something the org owns
//	PATCH  /v1/marketplace/listings/:id     edit one
//	DELETE /v1/marketplace/listings/:id     unpublish
//	POST   /v1/marketplace/install          install (activate) a tool for the caller's (org,project)
//	POST   /v1/marketplace/uninstall        uninstall (deactivate) a tool
//	POST   /v1/marketplace/jobs             hire: clear, then open a job on a signed x402 payment (jobs.go)
//	GET    /v1/marketplace/jobs[/:id]       the caller's jobs, as buyer or seller
//	POST   /v1/marketplace/listings/:id/verify  give an expert listing staff's mark, SuperAdmin only (expert.go)
//	POST   /v1/marketplace/jobs/:id/{accept,decline,cancel,deliver,release,dispute,refund,resolve,feedback}
//	GET    /v1/marketplace/seller           where the caller's org stands as a seller (seller.go)
//	POST   /v1/marketplace/seller/payout[/verify]  bind a payout wallet by a signed challenge
package marketplace
