// Package ads is your paid ad campaigns, launched and paused from one place.
//
// A campaign carries an objective, a budget and reported spend, and runs on
// Meta, Google, TikTok, Reddit, LinkedIn or Microsoft with the org's own
// connector token.
//
// It is also the PAID executor of the go-to-market plane: apps/campaigns fans its
// paid channel out to LaunchPaid/PaidSpend/PausePaid (provider.go), and this
// surface runs the same campaigns standalone.
//
// The AdCampaign entity is the root of the ad hierarchy (campaign → ad sets → ads):
// a named campaign on an ad Platform (meta/google/tiktok/x), a lifecycle Status
// (draft/active/paused/completed), an Objective, and Budget/Spend in minor units
// (cents). The ad-set and ad legs of the hierarchy are follow-ups that hang off
// this client; this is the reviewable domain + campaign CRUD they attach to.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/ad/summary            per-org roll-up (total/active/budget/spend)
//	GET    /v1/ad/campaigns          list campaigns (?status=)      -> {data:[…]}
//	POST   /v1/ad/campaigns          create a campaign              -> AdCampaign (201)
//	GET    /v1/ad/campaigns/:id      campaign detail                -> AdCampaign
//	PUT    /v1/ad/campaigns/:id      update a campaign              -> AdCampaign
//	DELETE /v1/ad/campaigns/:id      delete a campaign
//	POST   /v1/ad/campaigns/:id/launch  run it on the provider      -> AdCampaign
//
// serve.go auto-registers GET /v1/ad/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package ad
