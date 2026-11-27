// Package campaign is one go-to-market push across paid, organic and email at once.
//
// A campaign — audience, creatives, schedule, budget — launches to every
// channel and reads back as one funnel with each channel's spend.
//
// A Campaign is a VALUE — {name, audience, content[], schedule, budget,
// channels[], status} — that SPANS channels and fans out to orthogonal executors.
// It is the capability layer that CONSUMES the connector plane: the campaign
// object never touches a credential; each channel executor resolves the org's
// connector token itself through the provider.TokenFor custody client.
//
// THE DECOMPLECT (HIP-0126 — Integrations, Connectors & the Extension Runtime): a
// Connector is a connection (credential custody + auth); a capability is what you
// DO with it. /v1/campaign is a CONSUMER of connectors — the role HIP-0126 gives
// Flows — never a second credential path. "campaigns" used to be braided across
// three packages — an ad campaign (apps/ads), an email campaign
// (apps/marketing), social posts (apps/social). This plane lifts the GTM
// campaign to the ONE value it is and makes the channels orthogonal EXECUTORS it
// fans out to (channel.go):
//
//	paid    → apps/ads       (meta_ads/google_ads/tiktok_ads/… — REGISTERED)
//	organic → apps/social    (the social connectors — NO executor registered yet)
//	email   → apps/marketing (sendgrid/mailchimp/… — NO executor registered yet)
//
// Only the paid executor is wired today (plugin/campaign/clients.go). A campaign
// carrying an organic or email channel launches its paid channels and records the
// others "unavailable" — honest, never a faked launch. Until those two executors
// exist, /v1/social and /v1/marketing are the ONLY way to run those channels, and
// each is ALSO usable standalone once they are; this plane composes them. Metrics are NOT stored here — a campaign's results are read at query time
// from the ONE analytics plane (metrics.go: event.CampaignMetrics over the
// utm_campaign-tagged events) plus each channel connector's reported spend. A
// creative A/B is an experiment whose variant = creative and whose metric = the
// campaign result from analytics; it composes the experiment client (experiment.go),
// never a second assignment or evidence store.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, and the org is the value passed to every channel executor,
// so a campaign can only ever resolve its OWN org's connector token.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/campaign/summary               per-org roll-up + wired channels
//	GET    /v1/campaign                        list campaigns (?status=)   -> {data:[…]}
//	POST   /v1/campaign                        create a campaign (draft)    -> Campaign (201)
//	GET    /v1/campaign/:id                    campaign detail              -> Campaign
//	PUT    /v1/campaign/:id                    update a draft campaign      -> Campaign
//	DELETE /v1/campaign/:id                    delete a campaign
//	POST   /v1/campaign/:id/launch             fan out to channels          -> Campaign
//	POST   /v1/campaign/:id/pause              pause every live channel     -> Campaign
//	GET    /v1/campaign/:id/metrics            analytics results + spend    -> Metrics
//	POST   /v1/campaign/:id/channels           add a channel                -> Campaign
//	DELETE /v1/campaign/:id/channels/:kind     remove a channel             -> Campaign
//
// serve.go auto-registers GET /v1/campaign/health (no OwnsHealth here).
package campaign
