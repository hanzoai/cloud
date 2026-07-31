package campaign

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/analytics"
)

// metrics.go reads a campaign's RESULTS. Per the GTM architecture there is exactly
// ONE metrics plane: a campaign's impressions/clicks/conversions/revenue are an
// analytics query scoped to the campaign (analytics.CampaignMetrics over the
// utm_campaign-tagged events in event.event), and its spend is each channel
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

// metricsCampaign reports a campaign's results: the analytics funnel (impressions,
// clicks, conversions, revenue, visitors) over the chosen window, each channel's
// connector-reported spend, and the derived CTR, CVR, CAC and ROAS. Nothing is
// stored or fabricated — an events warehouse that is not emitting reports
// available:false rather than failing the read.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "range": "7d"}
func (o ops) metricsCampaign(ctx context.Context, in *MetricsQuery) (*Metrics, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	start, end, rangeLabel := metricsWindow(in.Range, in.Start, in.End)

	// Funnel — the ONE analytics query for a campaign's results (org + campaign
	// bound POSITIONALLY inside analytics; honest-empty when the events table is
	// absent). "" = whole-campaign (all creatives).
	ev, aerr := analytics.CampaignMetrics(ctx, org, camp.ID, "", start, end)
	if aerr != nil {
		// A datastore outage is honest-empty here too — the campaign still reports
		// its spend + channels; the funnel is simply unavailable this read.
		s.Log.Debug("campaign analytics unavailable (honest-empty)", "campaign", camp.ID, "err", aerr)
	}

	// Spend — each live channel's connector-reported spend, fanned in fail-soft.
	spendCents, chMetrics := channelSpend(ctx, org, camp)

	m := Metrics{
		CampaignID:  camp.ID,
		Name:        camp.Name,
		Status:      camp.Status,
		Range:       rangeLabel,
		Start:       start.UTC().Format(time.RFC3339),
		End:         end.UTC().Format(time.RFC3339),
		Available:   ev.Available,
		Impressions: ev.Impressions,
		Clicks:      ev.Clicks,
		Conversions: ev.Conversions,
		Revenue:     ev.Revenue,
		Visitors:    ev.Visitors,
		SpendCents:  spendCents,
		Channels:    chMetrics,
		Source:      ev.Source,
	}
	m.CTR = ratio(ev.Clicks, ev.Impressions)
	m.CVR = ratio(ev.Conversions, ev.Clicks)
	m.CAC = perConversion(spendCents, ev.Conversions)
	m.ROAS = roas(ev.Revenue, spendCents)
	// A/B lens: the experiments primitive's pull-model analysis (nil when the
	// campaign runs a single creative or no experiment is wired).
	m.ABTest = analyzeExperiment(ctx, org, camp, start, end)
	return &m, nil
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

// metricsWindow resolves [start,end) + a label from range (24h|7d|30d|90d) or an
// explicit start/end (RFC3339). Bad/absent input falls back to the 30-day default.
func metricsWindow(rangeIn, startIn, endIn string) (time.Time, time.Time, string) {
	now := time.Now().UTC()
	if s, e := strings.TrimSpace(startIn), strings.TrimSpace(endIn); s != "" && e != "" {
		st, err1 := time.Parse(time.RFC3339, s)
		en, err2 := time.Parse(time.RFC3339, e)
		if err1 == nil && err2 == nil && en.After(st) {
			return st.UTC(), en.UTC(), "custom"
		}
	}
	label := strings.ToLower(strings.TrimSpace(rangeIn))
	var d time.Duration
	switch label {
	case "24h":
		d = 24 * time.Hour
	case "7d":
		d = 7 * 24 * time.Hour
	case "90d":
		d = 90 * 24 * time.Hour
	case "30d", "":
		d = defaultRangeDays * 24 * time.Hour
		label = "30d"
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
