# LLM.md — apps/campaign

Guidance for apps/campaign. Moved from the root LLM.md; cross-cutting rules stay in the root.

## GTM: `/v1/campaign` orchestration → channels → connectors → analytics

The go-to-market stack decomplects a campaign from its execution. A **Campaign is a
VALUE** (`apps/campaign`: `{name, audience, content[], schedule, budget, channels[],
status}`) that SPANS channels; a **Channel is an EXECUTOR** (`channel.go`, the
`Channel` interface) it fans out to. The three channels are orthogonal and each
CONSUMES the connector plane via `integrations.TokenFor` — the campaign object never
touches a credential:

- **paid → `/v1/ads`** — `ads.LaunchPaid/PaidSpend/PausePaid` (`apps/ad/provider.go`)
  resolve the org's ad token (`meta_ads`/`google_ads`/… via `TokenFor(org, <id>,
  "access_token")`) and run the campaign on the provider. Meta is executed for real;
  fail-closed when the org has not connected (424). This is the ONLY place `/v1/ads`
  touches the connector plane.
- **organic → `/v1/publish`** (rename of `apps/social`) and **email → `/v1/marketing`**
  are DESIGNED follow-ons: register their executors the same way in `plugin/campaign/clients.go`
  (`campaign.RegisterChannel(campaign.NewChannel(kind, launch, spend, pause))`). Until
  wired, a fan-out records that channel "unavailable" (honest), never fabricated.

Channels are injected at the composition root (`plugin/campaign/clients.go`), the SAME
injected-function decoupling the coding dispatcher uses — `campaign` never imports
`ads`, `ads` never imports `campaign`. Fan-out (`launch.go` `fanOut`) is best-effort
per channel; the org (the ONLY tenant key) is passed verbatim to every executor, so a
campaign can only ever resolve its OWN org's token.

**Metrics = the ONE analytics plane, not a second store.** `GET /v1/campaign/:id/metrics`
reads the funnel from `analytics.CampaignMetrics(org, campaignID, variant, start, end)`
(`apps/event/campaign.go`) — an org+`utm_campaign`(+`utm_content`)-scoped query
over `hanzo.events`, org and campaign bound POSITIONALLY (same tenancy invariant as
every analytics query) — joined with each channel connector's reported spend
(`Channel.Spend`). Derived KPIs: CTR/CVR/CAC/ROAS. Honest-empty when the warehouse is
absent.

**Creative A/B composes the experiment primitive** (`experiment.go`), it does not
reinvent it: a creative A/B is an experiment whose variant = a creative (tagged
`utm_content`) and whose metric = the analytics read. The `AssignFunc`/`EvidenceFunc`
clients are wired at the root to the flags-assignment + evidence primitive; nil-safe
until it lands (single-creative honest default).
