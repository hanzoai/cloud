package guide

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/datastore"
)

// gtm.go is the Business AI Guide's ANALYTICS LENS: it reads the org's real funnel
// from the shared analytics warehouse (hanzo.events — the SAME table + tenancy column
// clients/analytics serves and the "analytics" detector probes) and turns it into
// GTM recommendations. The Guide is the AI-GTM agent; this is the data it reasons
// over so its guidance is grounded in what the funnel is actually doing, not generic
// advice.
//
// The read is org-scoped POSITIONALLY (tenant_id = ?, nothing user-derived
// interpolated) and honest-degrading: an unreachable/empty warehouse yields
// available=false, never a fabricated number.

// Funnel is the org's top-of-funnel over the trailing window: traffic → signups →
// orders, with revenue. Available is false when the warehouse is unreachable or the
// org has emitted nothing (honest-empty), so the caller renders "turn on analytics"
// rather than a misleading row of zeros.
type Funnel struct {
	Available  bool    `json:"available"`
	WindowDays int     `json:"windowDays"`
	Pageviews  int64   `json:"pageviews"`
	Visitors   int64   `json:"visitors"`
	Signups    int64   `json:"signups"`
	Orders     int64   `json:"orders"`
	Revenue    float64 `json:"revenue"`
}

// funnelWindowDays is the trailing window the lens summarizes.
const funnelWindowDays = 30

// analyticsFunnel reads the org's funnel from hanzo.events. It binds the org
// positionally and returns available=false on any warehouse error (best-effort — the
// GTM lens never fails the request over an unreachable warehouse) or when the org has
// emitted nothing.
func analyticsFunnel(ctx context.Context, org string) Funnel {
	f := Funnel{WindowDays: funnelWindowDays}
	const q = `SELECT
		countIf(event = '$pageview') AS pageviews,
		uniqExact(distinct_id) AS visitors,
		countIf(event = 'signup_completed') AS signups,
		countIf(event = 'order_completed') AS orders,
		toFloat64(sum(revenue)) AS revenue
	FROM ` + eventsTable + `
	WHERE tenant_id = ? AND timestamp >= now() - INTERVAL 30 DAY`
	rows, err := datastore.Query(ctx, q, org)
	if err != nil || len(rows) == 0 {
		return f
	}
	r := rows[0]
	f.Pageviews = numOf(r, "pageviews")
	f.Visitors = numOf(r, "visitors")
	f.Signups = numOf(r, "signups")
	f.Orders = numOf(r, "orders")
	f.Revenue = floatOf(r, "revenue")
	f.Available = f.Pageviews > 0 || f.Visitors > 0 || f.Signups > 0 || f.Orders > 0
	return f
}

// recommend derives the next-best GTM actions from the funnel — deterministic
// heuristics over real numbers (never fabricated advice). It reads the shape of the
// funnel (traffic present? converting to signups? to orders?) and points at the
// concrete Hanzo surface that moves the weakest stage.
func (f Funnel) recommend() []string {
	if !f.Available {
		return []string{
			"No analytics yet. Instrument your site with @hanzo/event so conversions flow to /v1/event — the Guide needs the funnel to advise you.",
		}
	}
	var out []string
	switch {
	case f.Visitors == 0:
		out = append(out, "Events are arriving but no visitors resolved — check that distinctId is set on your @hanzo/event client.")
	case f.Signups == 0:
		out = append(out, fmt.Sprintf("%d visitors, 0 signups. Sharpen the landing page and make the signup CTA unmissable.", f.Visitors))
	case f.Orders == 0:
		out = append(out, fmt.Sprintf("%d signups, 0 orders. Add a pricing page and a checkout — turn signups into revenue.", f.Signups))
	default:
		out = append(out, fmt.Sprintf("%d orders / $%.2f revenue. You are converting — connect an ad destination (/v1/destinations) so Meta and Google optimize on real purchases, not clicks.", f.Orders, f.Revenue))
	}
	if f.Orders > 0 || f.Signups > 0 {
		out = append(out, "Forward these conversions to your ad platforms via Destinations so their algorithms bid on the people who actually convert.")
	}
	return out
}

// numOf reads an integer column across the numeric types a datastore aggregate can
// surface (mirrors detect.go's firstCount, generalized to a named column).
func numOf(row map[string]any, key string) int64 {
	switch v := row[key].(type) {
	case int64:
		return v
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	default:
		return 0
	}
}

// floatOf reads a float column across the numeric types a datastore sum() surfaces.
func floatOf(row map[string]any, key string) float64 {
	switch v := row[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}
