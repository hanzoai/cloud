package cloudflare

// spend.go — the account's spending limits, read against the warning threshold.
//
// Cloudflare stops Workers and Durable Objects at an account spending limit
// (Billing > Spend Management; the API calls them spend policies). It emails at
// 80% and 100% only to a policy's notification_email, which a dashboard-created
// limit does not have — so the first sign of a limit was usually the stop itself.
// This read is the warning before it: each limit with this period's spend, where
// it stands against CLOUD_PROVIDER_BUDGET_WARN_PCT, and a hard limit at or past
// that threshold said on the platform's alert path (sdk/supply) when it is read.
//
// The plane owns no clock, so a limit is measured when somebody reads it — a
// console, or ops tooling polling the platform's own account.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/supply"
)

// spendPolicy is one Cloudflare spend policy, the fields this read uses.
type spendPolicy struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ProductID      string `json:"product_id"`
	Action         string `json:"action"`
	Enabled        bool   `json:"enabled"`
	ThresholdCents int64  `json:"threshold_cents"`
	// CurrentState is absent when Cloudflare does not report the period's spend.
	CurrentState *struct {
		BalanceCents *int64 `json:"balance_cents"`
		Status       string `json:"status"`
	} `json:"current_state"`
}

// actionBlock is the policy action that STOPS the product at the limit; the other,
// "notification", only emails.
const actionBlock = "usage_block"

// spendLimitsOut is the account's spending limits.
type spendLimitsOut struct {
	// WarnPct is the share of a limit, in percent, at which a limit is reported near
	// it (CLOUD_PROVIDER_BUDGET_WARN_PCT; 80 unless the deployment sets another).
	WarnPct int `json:"warnPct"`
	// Limits is every spending limit on the account, in Cloudflare's order.
	Limits []spendLimit `json:"limits"`
}

// spendLimit is one spending limit and where this period's spend stands against it.
type spendLimit struct {
	// ID is Cloudflare's id for the limit (its spend policy).
	ID string `json:"id"`
	// Name is the limit's name on Cloudflare, when it has one.
	Name string `json:"name,omitempty"`
	// Layer is the product the limit holds, in Cloudflare's own words: workers or
	// durable_objects.
	Layer string `json:"layer"`
	// Hard is true for a limit that STOPS the product when reached (Cloudflare's
	// usage_block), false for one that only notifies.
	Hard bool `json:"hard"`
	// Enabled is whether Cloudflare enforces the limit.
	Enabled bool `json:"enabled"`
	// LimitCents is the limit for the current billing period, in US cents.
	LimitCents int64 `json:"limitCents"`
	// SpentCents is Cloudflare's estimate of this period's spend, in US cents.
	// Absent, never zero, when Cloudflare did not report it: "nothing spent" and
	// "spend unknown" are different answers.
	SpentCents *int64 `json:"spentCents,omitempty"`
	// UsedPct is SpentCents as a whole percentage of LimitCents; absent with it.
	UsedPct int `json:"usedPct,omitempty"`
	// Status is Cloudflare's own reading of the limit: below_limit, limit_reached,
	// no_current_period, unavailable or disabled.
	Status string `json:"status,omitempty"`
	// Level is where the spend stands: below, warn (at or over warnPct of the
	// limit), at_cap, or unknown when the spend was not reported.
	Level string `json:"level"`
}

// SpendLimits lists the spending limits on the org's Cloudflare account — the
// Workers and Durable Objects ceilings Cloudflare stops work at — each with this
// billing period's spend and where it stands against the warning threshold below
// the cap (`warnPct`).
//
// A hard limit (one that stops the product) at or past the threshold is also
// raised on the platform's alert path when it is read, so a read is the warning
// BEFORE Cloudflare stops the layer, not after. Needs a token that can read the
// account's billing; without it Cloudflare's own 403 is relayed. Any org member
// may read.
func (o ops) spendLimits(ctx context.Context, _ *cloud.Unit) (*spendLimitsOut, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	var policies []spendPolicy
	if err := cl.cfDo(ctx, http.MethodGet, "/accounts/"+acct+"/billing/spend-policies", nil, &policies); err != nil {
		return nil, cfErr(err)
	}
	out := &spendLimitsOut{WarnPct: supply.WarnPct(), Limits: make([]spendLimit, 0, len(policies))}
	for _, p := range policies {
		out.Limits = append(out.Limits, measure(ctx, cl.org, p, out.WarnPct))
	}
	return out, nil
}

// measure is one policy as a spendLimit. An enabled hard limit with a reported
// spend is also observed (sdk/supply), which is what raises the warning.
func measure(ctx context.Context, org string, p spendPolicy, warnPct int) spendLimit {
	l := spendLimit{ID: p.ID, Name: p.Name, Layer: p.ProductID, Hard: p.Action == actionBlock,
		Enabled: p.Enabled, LimitCents: p.ThresholdCents, Level: "unknown"}
	if p.CurrentState != nil {
		l.Status = p.CurrentState.Status
	}
	if p.CurrentState == nil || p.CurrentState.BalanceCents == nil || p.ThresholdCents <= 0 {
		return l
	}
	spent := *p.CurrentState.BalanceCents
	l.SpentCents = &spent
	l.UsedPct = int(spent * 100 / p.ThresholdCents)
	switch {
	case spent >= p.ThresholdCents || l.Status == "limit_reached":
		l.Level = supply.AtCap.String()
	case spent*100 >= int64(warnPct)*p.ThresholdCents:
		l.Level = supply.Warn.String()
	default:
		l.Level = supply.Below.String()
	}
	if layer, ok := supply.ParseLayer(p.ProductID); ok && l.Hard && p.Enabled {
		supply.Observe(ctx, supply.Reading{Provider: "cloudflare", Layer: layer, Org: org, Used: spent, Limit: p.ThresholdCents})
	}
	return l
}
