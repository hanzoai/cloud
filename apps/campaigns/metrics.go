package campaigns

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// metrics.go reads a campaign's RESULTS. Per the GTM architecture there is exactly
// ONE metrics plane: a campaign's impressions/clicks/conversions/revenue are an
// analytics query scoped to the campaign (event.CampaignMetrics over the
// utm_campaign-tagged events in event.event), and its spend is each channel
// connector's reported number (Channel.Spend, which the executor reads from the
// provider via the org's connector token). Nothing is stored here and nothing is
// fabricated — an unprovisioned events warehouse degrades to honest-empty, exactly
// as /v1/event does.
//
// The derived rates (CTR/CVR/CAC/ROAS) are the growth KPIs the CTO growth loop
// reads; they compose the same two honest inputs.

// campaignWindowEnv-free: campaigns run over weeks, so the default lookback is 30
// days. ?range trims it; ?start/?end are honored as RFC3339 for a custom window.
const defaultRangeDays = 30

// ChannelMetric is one channel's spend contribution to a campaign's metrics.
type ChannelMetric struct {
	// Kind is which channel this row is: paid, organic or email. It is also the
	// row's identity — a campaign carries at most one channel per kind.
	Kind string `json:"kind"`
	// Platform is the provider the spend was read from: meta, google, x, instagram,
	// or the email provider.
	Platform string `json:"platform"`
	// Status is the channel's launch state on the campaign — pending, live, paused,
	// failed or unavailable. Only a live channel is asked for its spend at all.
	Status string `json:"status"`
	// ExternalID is the provider-side id of the execution the spend belongs to.
	// Absent until the channel has launched.
	ExternalID string `json:"externalId,omitempty"`
	// SpendCents is what the provider itself reports this channel spent, in CENTS.
	// 0 when the channel never launched, when no executor is wired for it, or when
	// the read failed — SpendError tells the last case apart from a genuine zero.
	SpendCents int64 `json:"spendCents"`
	// SpendError is why this channel's spend could not be read (connector not
	// connected, provider error), as one secret-free line. Present only on failure;
	// the campaign total then simply omits this channel rather than failing.
	SpendError string `json:"spendError,omitempty"`
}

// campaignResults is a campaign's results view: the analytics-sourced funnel + the
// connector-sourced spend + the derived growth KPIs. Available reflects the
// analytics events lens (false = warehouse not yet emitting, honest-empty).
//
// Declared under its published name for the reason campaignRecord is (store.go): a
// field's prose reaches the document only under the name its struct literal is
// declared with.
type campaignResults struct {
	// CampaignID is the campaign these results are for, echoed from the request.
	CampaignID string `json:"campaignId"`
	// Name is the campaign's display name at read time, so a result can be labelled
	// without a second fetch.
	Name string `json:"name"`
	// Status is the campaign's lifecycle state at read time — draft, live, paused,
	// completed or failed. A draft has never run, so its funnel is legitimately zero.
	Status string `json:"status"`
	// Range is the window actually used: 24h, 7d, 30d, 90d, or "custom" when an
	// explicit start/end pair was honored. An unparseable or absent range reads 30d,
	// so this is the value to trust, not the one that was sent.
	Range string `json:"range"`
	// Start is the window's inclusive start, RFC3339 UTC.
	Start string `json:"start"`
	// End is the window's end, RFC3339 UTC — the read's own clock unless an explicit
	// pair was given. The window is a LOOKBACK, not the campaign's own lifetime.
	End string `json:"end"`
	// Available is false when the analytics warehouse is not connected or the query
	// failed: the funnel below is then zero because nothing could be read, not
	// because nothing happened. Spend and Channels are still real — they come from
	// the connectors, not the warehouse.
	Available bool `json:"available"`
	// Impressions is how many times the campaign's creatives were shown, counted
	// from its utm_campaign-tagged impression events.
	Impressions int64 `json:"impressions"`
	// Clicks is the campaign's click events over the window.
	Clicks int64 `json:"clicks"`
	// Conversions is the terminal funnel events attributed to the campaign — orders
	// completed, signups completed, explicit conversion events.
	Conversions int64 `json:"conversions"`
	// Revenue is the summed revenue attribute of the campaign's events, in whole
	// CURRENCY UNITS (dollars) — the one money value here that is not in cents.
	Revenue float64 `json:"revenue"`
	// Visitors is how many distinct people the campaign reached, counted by event
	// identity across ALL its events in the window — not a subset of Impressions, so
	// it can exceed them for a campaign whose provider reports clicks but not views.
	Visitors int64 `json:"visitors"`
	// SpendCents is the campaign's total spend in CENTS: the sum of what each live
	// channel's provider reports. A channel whose spend could not be read
	// contributes 0 and says so on its own row.
	SpendCents int64 `json:"spendCents"`
	// CTR is clicks per impression, a fraction rounded to 4 places (0.0123 = 1.23%),
	// not a percentage. 0 when there were no impressions to divide by.
	CTR float64 `json:"ctr"`
	// CVR is conversions per click, a fraction rounded to 4 places. 0 when there
	// were no clicks.
	CVR float64 `json:"cvr"`
	// CAC is customer acquisition cost: spend DOLLARS per conversion, rounded to
	// cents. 0 when nothing converted — that is "not yet computable", not "free".
	CAC float64 `json:"cac"`
	// ROAS is return on ad spend: revenue per spend DOLLAR, rounded to 2 places
	// (2.5 = $2.50 back per $1). 0 when nothing was spent.
	ROAS float64 `json:"roas"`
	// Channels is the per-channel spend breakdown that SpendCents sums, one row per
	// channel on the campaign including the ones that never launched.
	Channels []ChannelMetric `json:"channels"`
	// Source names the analytics table the funnel was read from, so an operator can
	// see exactly what was counted. Set even when Available is false.
	Source string `json:"source"`
	// ABTest is the creative A/B analysis from the experiments primitive
	// (experiments.Analyze, pull-model), present only when the campaign runs
	// more than one creative and an experiment is wired. Opaque JSON — campaign
	// stays decoupled from the experiments analysis type.
	ABTest json.RawMessage `json:"abTest,omitempty"`
}

// Metrics is the domain spelling of campaignResults — an ALIAS, the same type, for
// the same reason Campaign is one for campaignRecord (store.go): apps/agents
// already publishes a "Metrics" into the fleet's flat schema namespace.
type Metrics = campaignResults

// channelSpend fans the spend read across a campaign's live channels. Each read
// is best-effort: a connector-disabled or provider-error channel contributes 0
// with an honest SpendError, never failing the whole metrics read. The org is
// passed verbatim so each executor resolves ITS OWN org's connector token.
func channelSpend(ctx context.Context, org string, camp Campaign) (int64, []ChannelMetric) {
	out := make([]ChannelMetric, 0, len(camp.Channels))
	var total int64
	for _, spec := range camp.Channels {
		cm := ChannelMetric{Kind: spec.Kind, Platform: spec.Platform, Status: spec.Status, ExternalID: spec.ExternalID}
		if spec.Status == chanLive && spec.ExternalID != "" {
			if ch, ok := resolveChannel(spec.Kind); ok {
				cents, err := ch.Spend(ctx, org, refOf(spec))
				if err != nil {
					cm.SpendError = shortErr(err)
				} else {
					cm.SpendCents = nonNeg(cents)
					total += cm.SpendCents
				}
			}
		}
		out = append(out, cm)
	}
	return total, out
}

// window resolves [start,end) + a label from a range (24h|7d|30d|90d) or an
// explicit start/end pair (RFC3339). Bad or absent input falls back to the
// 30-day default. It takes the three VALUES rather than the request, so the typed
// op and any caller reach the same rule through the same function.
func window(startStr, endStr, rangeLabel string) (time.Time, time.Time, string) {
	now := time.Now().UTC()
	if s, e := strings.TrimSpace(startStr), strings.TrimSpace(endStr); s != "" && e != "" {
		st, err1 := time.Parse(time.RFC3339, s)
		en, err2 := time.Parse(time.RFC3339, e)
		if err1 == nil && err2 == nil && en.After(st) {
			return st.UTC(), en.UTC(), "custom"
		}
	}
	label := strings.ToLower(strings.TrimSpace(rangeLabel))
	var d time.Duration
	switch label {
	case "24h":
		d = 24 * time.Hour
	case "7d":
		d = 7 * 24 * time.Hour
	case "90d":
		d = 90 * 24 * time.Hour
	default:
		d = defaultRangeDays * 24 * time.Hour
		label = "30d"
	}
	return now.Add(-d), now, label
}

// ── derived KPIs (pure, div-by-zero-safe) ────────────────────────────────────

func ratio(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return round4(float64(part) / float64(whole))
}

// perConversion is CAC: spend DOLLARS per conversion (cents/100/conversions).
func perConversion(spendCents, conversions int64) float64 {
	if conversions <= 0 {
		return 0
	}
	return round2(float64(spendCents) / 100 / float64(conversions))
}

// roas is revenue per spend dollar (revenue / (cents/100)).
func roas(revenue float64, spendCents int64) float64 {
	if spendCents <= 0 {
		return 0
	}
	return round2(revenue / (float64(spendCents) / 100))
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }
