// Package destinations is your events forwarded to the ad and analytics tools
// you use.
//
// Server-side conversion forwarding: connect Google Analytics 4, Meta, X,
// LinkedIn, TikTok, Reddit, Insights or Analytics, and every event the org captures
// is translated into that platform's own conversion schema and sent from the
// server, with PII hashed on the way out.
//
// It is a CONSUMER of the canonical /v1/event stream (apps/event), installed
// as a sink at Mount — never a second collector and never a second ingest endpoint.
//
// The plane is four decomplected concerns, one per file group:
//
//   - Destination interface + per-platform adapters (this file + ga4/meta/… .go):
//     an adapter renders the normalized Conversion into its platform's wire shape
//     and delivers it. Adapters self-register from init() — a new platform is a new
//     file, never a change to the fan-out.
//   - translator (translate.go): maps the canonical EVENTS vocabulary once onto the
//     normalized StandardEvent taxonomy + lifts match keys — the ONE interlingua
//     every adapter renders from.
//   - per-org registry (store.go + KMS custody in destinations.go): the connected
//     destinations + their non-secret ids (measurement/pixel), with the API secrets
//     KMS-sealed per org.
//   - fan-out consumer (fanout.go): the apps/event sink — for each of an
//     org's enabled destinations, translate + Send, bounded and fail-soft.
//
// SECRET CUSTODY mirrors apps/integrations: a destination's API secret lives ONLY
// in KMS (sealed, per org, at /orgs/{org}/destinations/{platform}); the store holds
// only the non-secret ids. A destination may instead ride an existing integrations
// connection's token (Meta CAPI reuses the meta_ads OAuth token) — Spec.Fallback.
package destination
