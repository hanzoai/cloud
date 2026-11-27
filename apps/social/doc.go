// Package social is posting to every social account you own, now or on a schedule.
//
// An org's connected channels (X, Facebook, Instagram, LinkedIn, TikTok,
// YouTube, Threads) and the posts it publishes or schedules to them.
//
// Two entities. An account is a connected channel (the hanzoai/social stack's
// "integration": GET /public/v1/provider), and a post is content published or
// scheduled to a channel (POST /public/v1/posts {type:now|schedule, date, …}).
// Scheduling is not a third entity — it is a post with Status=="scheduled"
// carrying a future ScheduleAt.
//
// This is the in-process fold of the standalone social pods onto the cloud
// framework. apps/content still reaches the SAME upstream over HTTP
// (channels.go → api.social.hanzo.ai), so social publishing has two paths today
// and this one — which owns the accounts, the scheduler and the publish edge — is
// the one. apps/marketing's content calendar is a third scheduled-post store; it
// has no publisher wired and answers 501.
//
// The publish edge (publish.go) and the scheduler (scheduler.go) ARE folded: a post
// fans out to its channel's connected accounts through the Publisher client, on an
// explicit publish, on create (when scheduled for now-or-earlier), and on the scheduler
// tick (scheduled → published when the time arrives). The provider push itself is the
// swappable Publisher edge; its fail-closed default is honest — no Hanzo deployment
// carries the provider OAuth-app credentials (providerCreds) the live orchestrator needs,
// so a publish reports exactly which credentials are missing (503) and NEVER fakes
// success. The per-account OAuth connect flow + native per-provider push are the honest
// remaining gap (see the fold report + GET /v1/social/providers).
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/social/summary            per-org roll-up (posts/scheduled/published/accounts)
//	GET    /v1/social/providers          publish-readiness per network (+ missing creds)
//	GET    /v1/social/accounts           list accounts (?provider=)      -> {data:[…]}
//	POST   /v1/social/accounts           connect an account             -> socialAccount (201)
//	GET    /v1/social/accounts/:id       account detail                 -> socialAccount
//	PUT    /v1/social/accounts/:id       update an account              -> socialAccount
//	DELETE /v1/social/accounts/:id       disconnect an account
//	GET    /v1/social/posts              list posts (?status=)           -> {data:[…]}
//	POST   /v1/social/posts              create/schedule a post          -> socialPost (201)
//	GET    /v1/social/posts/:id          post detail                    -> socialPost
//	PUT    /v1/social/posts/:id          update a post                  -> socialPost
//	DELETE /v1/social/posts/:id          delete a post
//	POST   /v1/social/posts/:id/publish  publish a post now              -> socialPost
//
// serve.go auto-registers GET /v1/social/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package social
