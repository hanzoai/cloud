// Package leaderboard is the ranking of who uses AI most, in your org and globally.
//
// It ranks who leads inside an org, which orgs lead globally, and draws a
// GitHub-style per-day contribution graph for one subject — all opt-in for
// public listing.
//
// It is a DERIVED, read-only lens over the ONE usage ledger (hanzo.cloud_usage)
// through the datastore OLAP rollup — it adds no metering path and double-counts
// nothing.
//
// Surface (all /v1, NO /api/ prefix; org-scoped, fail-closed):
//
//	GET  /v1/leaderboard              ranked top users (personal|org) or orgs (global)
//	GET  /v1/leaderboard/standing     the PUBLIC board: a masked window around the reader (standing.go)
//	GET  /v1/leaderboard/pulse        the PUBLIC pulse: activity and spread as counts over time (pulse.go)
//	GET  /v1/leaderboard/galaxy       the PUBLIC galaxy: every ranked org as a star, opaque and masked (galaxy.go)
//	GET  /v1/leaderboard/feed         the PUBLIC live feed, Server-Sent Events, masked (feed.go)
//	GET  /v1/leaderboard/badge/:handle the PUBLIC rank SVG badge for a listed org, hourly-cached (badge.go)
//	GET  /v1/leaderboard/activity     per-day series for a heatmap + timeline (authorized subject)
//	GET  /v1/leaderboard/beta         the hanzo.ai/beta waitlist ranked by one score (beta.go)
//	GET  /v1/leaderboard/optin        the caller's opt-in + their org's opt-in
//	PUT  /v1/leaderboard/optin        set the caller's OWN public-listing opt-in
//	PUT  /v1/leaderboard/optin/org    set the ORG's public-board opt-in (org admin)
//	POST /v1/admin/leaderboard/rollup seed the rollup from ledger history (SuperAdmin, once)
//
// It answers under its OWN name. It used to sit inside apps/usage's /v1/usage/*
// prefix — a distinct concern (who leads + your activity graph) at paths another
// subsystem's name was on, which apps/usage's own doc read as "owns ALL usage".
// Two packages under one prefix is one prefix with no owner, and the two ARE two:
// this one keeps a store (the opt-in preferences, store.go) and usage keeps none,
// so the boundary was already there and only the address disagreed. Now the
// address agrees, and usage keeps what it actually owns (/v1/usage/summary,
// /v1/usage/analytics). Its auto health route is /v1/leaderboard/health, which
// the name now matches. The SuperAdmin backfill is the operator's view of this
// capability and lands where those live, /v1/admin/leaderboard/rollup.
//
// TENANT ISOLATION (the bar). The org is the VALIDATED IAM owner claim (principal.Org
// — the trusted X-Org-Id the identity middleware minted from the verified bearer,
// HIP-0026; NEVER a client header) AND a validated principal is required. Every
// datastore read binds the org POSITIONALLY (never interpolated). A user board only
// ever contains the caller's own org's rows; an org board carries org-level aggregates
// only; cross-org detail is structurally impossible. Fail closed: no principal → 401;
// datastore not connected → honest-empty (available:false), never fabricated ranks.
//
// PRIVACY (opt-in). Public listing is OPT-IN and PRIVATE by default: a user sees their
// OWN rank always, but is shown to others only after opting in with a chosen handle;
// an org appears on the cross-org global board only after an org admin opts it in.
// See view.go for the naming/anonymization policy.
package leaderboard
