// Package account is your own account: API keys you mint and revoke, and org onboarding.
//
// It mounts the signed-in caller's OWN self-service surface natively in the unified
// cloud binary — the Go port of the console's two NON-proxy Next server routes
// (app/keys + app/onboard) plus the server-side money work the statically-exported
// console needs (task #41, "True 1-binary FE"). It replaces the retired /v1/console/*
// namespace: "console" is just the cloud FE name, so there is NO /v1/console API
// domain — every route lives on its REAL domain.
//
// WHY THESE ROUTES (and not the pure passthrough proxies). The console's PURE BFF
// reverse-proxies — app/cloud, app/ai — vanish in the one-binary model: the SPA calls
// the canonical /v1/* on its own origin and the already-mounted subsystems answer. The
// routes ported HERE do REAL server work a static SPA cannot: keys/onboard run
// privileged IAM logic as the confidential `hanzo-console` client, and embed does
// server-side verification. Each has no pure-proxy equivalent, so it must be ported.
//
// The billing and store DATA are not among them, and the difference is the whole
// lesson. They were ported as two catch-all forwarders — GET|POST /v1/billing/* and
// full-CRUD /v1/commerce/*, mounted last (order 122), re-serving families other apps
// already own by re-dialing them over HTTP as the platform. That
// authority satisfies commerce's MayMintMoney, so forwarding WAS authorization and the only
// thing standing between a signed-in member and the mint routes was a hand-maintained
// allowlist. They are gone. Every endpoint either forwarded is served natively — by
// billing (order 121) or by the co-resident commerce embed (order 100) — at a prefix the
// manifest names DEEPER than the bare stem, so each already won the route and the
// forwarder saw none of them. What survives here is the part that was never the proxy:
// the subject-pinning those native routes apply themselves (billing_coresident.go's
// PinBillingSubject).
//
// SURFACE — one prefix, /v1/account, and the segment after it names the thing (every
// route requires a VALIDATED principal — a gateway-minted, IAM-verified X-User-Id; a
// client-forged X-Org-Id on the bearer-less path is refused):
//
//	GET    /v1/account/keys          — the keys the caller may see: their own, or all the org's for an org admin.
//	POST   /v1/account/keys          — create a NEW key { name, type, limit, expires }; returns its secret ONCE.
//	PATCH  /v1/account/keys/{id}     — edit one key's name, limit or expiry; the secret is unchanged.
//	DELETE /v1/account/keys/{id}     — revoke exactly that key; it stays listed as revoked.
//	POST   /v1/account/orgs          — create an org the caller owns by membership; they are never moved.
//	GET    /v1/account/csrf          — mint the anti-CSRF token the SPA echoes on money writes (csrf.go).
//	GET    /v1/account/embed         — brand-app embed entitlement + reachability probe (embed.go).
//
// ONE SUBSYSTEM REGISTRATION, at order 48. The order is a convention, not the
// protection: the fiber fork inserts endpoint routes MOST-SPECIFIC-FIRST regardless of
// when they were registered (zap-proto/fiber router_precedence.go — ServeMux semantics,
// a static literal beats a param beats a greedy wildcard), so /v1/account/* wins over
// clients/iam's /v1/iam/* and the commerce embed because it is DEEPER, not because it
// mounts earlier. Specificity is also why the retired bridge's two bare stems could
// never have shadowed anything — and why nothing needed to replace them when they went.
//
// TYPED OPS. Every ADDRESSABLE route here is a typed op (zip.Get/Post/Delete with
// real In/Out types), so each is ONE registry entry the REST route, the OpenAPI
// operation's schema and prose, the MCP tool, the CLI command and every generated
// SDK method all derive from. Two are not, and cannot be: the profile-photo pair
// takes a multipart form in and answers raw image bytes out (avatar.go). The
// bridge's seven wildcard forwarders were the other exceptions and they went with
// it. typed_wire_test.go holds the list as a CLOSED set and fails on any account
// operation that is neither a typed op nor named there — so the next route added
// here is typed by default, and dropping one out of the registry takes a
// deliberate edit with a reason.
//
// TENANCY. The caller is resolved from the VALIDATED identity headers ONLY
// (principal.Validated / c.Org() / c.User()), the same trust boundary every mutating
// subsystem uses. The IAM id targeted is DERIVED as `<owner>/<name>` from those
// validated claims — never taken from the request body/query — so a caller can only ever
// mint/revoke their OWN key and onboard THEMSELVES; there is no path to name a
// third-party subject. When the confidential client is unwired the surface is honestly
// "not configured" (501), never a fabricated key or org.
package account
