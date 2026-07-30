package campaign

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// metrics.go reads a campaign's RESULTS. Per the GTM architecture there is exactly
// ONE metrics plane: a campaign's impressions/clicks/conversions/revenue are an
// analytics query scoped to the campaign (analytics.CampaignMetrics over the
// utm_campaign-tagged events in hanzo.events), and its spend is each channel
// connector's reported number (Channel.Spend, which the executor reads from the
// provider via the org's connector token). Nothing is stored here and nothing is
// fabricated — an unprovisioned events warehouse degrades to honest-empty, exactly
// as /v1/analytics does.
//
// The derived rates (CTR/CVR/CAC/ROAS) are the growth KPIs the CTO growth loop
// reads; they compose the same two honest inputs.

// campaignWindowEnv-free: campaigns run over weeks, so the default lookback is 30
// days. ?range trims it; ?start/?end are honored as RFC3339 for a custom window.
const defaultRangeDays = 30

// ChannelMetric is one channel's spend contribution to a campaign's metrics.
type ChannelMetric struct {
	Kind       string `json:"kind"`
	Platform   string `json:"platform"`
	Status     string `json:"status"`
	ExternalID string `json:"externalId,omitempty"`
	SpendCents int64  `json:"spendCents"`
	SpendError string `json:"spendError,omitempty"` // honest: connector spend read failed
}

// Metrics is a campaign's results view: the analytics-sourced funnel + the
// connector-sourced spend + the derived growth KPIs. Available reflects the
// analytics events lens (false = warehouse not yet emitting, honest-empty).
type Metrics struct {
	CampaignID  string          `json:"campaignId"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Range       string          `json:"range"`
	Start       string          `json:"start"`
	End         string          `json:"end"`
	Available   bool            `json:"available"`
	Impressions int64           `json:"impressions"`
	Clicks      int64           `json:"clicks"`
	Conversions int64           `json:"conversions"`
	Revenue     float64         `json:"revenue"`
	Visitors    int64           `json:"visitors"`
	SpendCents  int64           `json:"spendCents"`
	CTR         float64         `json:"ctr"`  // clicks / impressions
	CVR         float64         `json:"cvr"`  // conversions / clicks
	CAC         float64         `json:"cac"`  // spend $ per conversion
	ROAS        float64         `json:"roas"` // revenue per spend $
	Channels    []ChannelMetric `json:"channels"`
	Source      string          `json:"source"`
	// ABTest is the creative A/B analysis from the experiments primitive
	// (experiments.Analyze, pull-model), present only when the campaign runs
	// more than one creative and an experiment is wired. Opaque JSON — campaign
	// stays decoupled from the experiments analysis type.
	ABTest json.RawMessage `json:"abTest,omitempty"`
}

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
