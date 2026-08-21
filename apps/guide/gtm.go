package guide

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/apps/datastore"
)

// gtm.go is the Business AI Guide's ANALYTICS LENS: it reads the org's real funnel
// from the event plane (event.fact — the SAME table + tenancy column
// clients/analytics serves and the "analytics" detector probes) and turns it into
// GTM recommendations. The Guide is the AI-GTM agent; this is the data it reasons
// over so its guidance is grounded in what the funnel is actually doing, not generic
// advice.
//
// The read is org-scoped POSITIONALLY (org = ?, nothing user-derived interpolated)
// and honest-degrading: an unreachable/empty warehouse yields available=false,
// never a fabricated number.

// Funnel is the org's top-of-funnel over the trailing window: traffic → signups →
// orders, with revenue. Available is false when the warehouse is unreachable or the
// org has emitted nothing (honest-empty), so the caller renders "turn on analytics"
// rather than a misleading row of zeros.
type Funnel struct {
	// Available separates "this org has no traffic" from "we could not ask". False
	// means the warehouse was unreachable or the org has emitted nothing at all, and
	// every count below is then a placeholder zero rather than a measurement — a
	// caller must read this before reading any of them.
	Available bool `json:"available"`
	// WindowDays is the length of the trailing window every count covers, so a
	// reader knows whether 40 signups is a month or a day.
	WindowDays int `json:"windowDays"`
	// Pageviews counts page events in the window, one per view rather than per
	// person, so a single visitor reading ten pages counts ten.
	Pageviews int64 `json:"pageviews"`
	// Visitors is the number of DISTINCT people seen in the window, counted by the
	// beacon's distinct id — so it is unique visitors, not sessions and not views.
	Visitors int64 `json:"visitors"`
	// Signups counts completed signups in the window, the step where an anonymous
	// visitor becomes somebody with an account.
	Signups int64 `json:"signups"`
	// Orders counts completed orders in the window — purchases, not carts started.
	Orders int64 `json:"orders"`
	// Revenue is the sum of the amounts those orders reported, in whatever currency
	// the beacon stamped on them (major units, e.g. 49.5 for $49.50) — NOT cents,
	// and not converted to a single currency. Contrast revenueCents on the profile,
	// which is the money of record.
	Revenue float64 `json:"revenue"`
}

// funnelWindowDays is the trailing window the lens summarizes.
const funnelWindowDays = 30

// analyticsFunnel reads the org's funnel from event.fact. It binds the org
// positionally and returns available=false on any warehouse error (best-effort — the
// GTM lens never fails the request over an unreachable warehouse) or when the org has
// emitted nothing. A pageview is kind='page' (the plane's discriminator) and revenue
// reads back the attributes entry the plane normalizer stamped.
func analyticsFunnel(ctx context.Context, org string) Funnel {
	f := Funnel{WindowDays: funnelWindowDays}
	const q = `SELECT
		countIf(kind = 'page') AS pageviews,
		uniqExact(distinct_id) AS visitors,
		countIf(name = 'signup_completed') AS signups,
		countIf(name = 'order_completed') AS orders,
		sum(toFloat64OrZero(attributes['revenue'])) AS revenue
	FROM ` + eventsTable + `
	WHERE org = ? AND signal = 'act' AND time >= now() - INTERVAL 30 DAY`
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
		out = append(out, fmt.Sprintf("%d orders / $%.2f revenue. You are converting — connect an ad destination (/v1/destination) so Meta and Google optimize on real purchases, not clicks.", f.Orders, f.Revenue))
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
