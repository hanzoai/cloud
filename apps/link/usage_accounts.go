package link

// usage_accounts.go is the per-account SERVER-ROUTED usage breakdown the unified
// dashboard reads: the total plus one row per linked account the gateway routed
// through, for the caller's OWN accounts.
//
// It is the READ twin of routed.go's counter. The scope is the caller (org+subject):
// a user sees the accounts THEY linked and routed, never another user's or org's —
// the same fail-closed tenancy the rest of the plane uses (principal.Org + c.User()).
//
// TWO MOUNTS, ONE VIEW. The SAME shaped view answers at:
//   - GET /v1/links/usage/accounts   — canonical, where the data lives (this file),
//   - GET /v1/billing/usage/accounts — the billing namespace the dashboard's usage
//     panels read (clients/billing, which calls RoutedBreakdown below).
// One shaping function (routedAccountsView) feeds both, so the two can never drift.

import (
	"context"
	"net/http"

	"github.com/zap-proto/zip"
)

// AccountsTotal is the summed usage across a caller's linked accounts.
type AccountsTotal struct {
	Accounts         int   `json:"accounts"`
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	TotalTokens      int64 `json:"totalTokens"`
	CostCents        int64 `json:"costCents"`
}

// AccountsUsage is the per-account breakdown response. Source is always "routed" —
// this is the gateway's own routed ledger, distinct from the device collector's plan
// snapshots (/v1/links/usage/summary) and from the org money ledger
// (/v1/billing/usage). Scope is always "user": the caller's own linked accounts.
//
// The fields carry NO per-field doc comments on purpose. clients/billing serves this
// same Go type from its own surface, and a field description generated here but not
// there makes ONE schema name describe two shapes, which the fleet weave refuses.
// The prose lives on the type, where both projections read the same thing: nothing.
type AccountsUsage struct {
	Scope    string        `json:"scope"`
	Source   string        `json:"source"`
	Total    AccountsTotal `json:"total"`
	Accounts []RoutedUsage `json:"accounts"`
}

// routedAccountsView shapes the counter rows into the response: the per-account rows
// as-is plus their honest total. Pure, so the shape is unit-testable without a store.
func routedAccountsView(rows []RoutedUsage) AccountsUsage {
	out := AccountsUsage{Scope: ScopeUser, Source: "routed", Accounts: rows}
	if out.Accounts == nil {
		out.Accounts = []RoutedUsage{}
	}
	out.Total.Accounts = len(rows)
	for _, r := range rows {
		out.Total.Requests += r.Requests
		out.Total.PromptTokens += r.PromptTokens
		out.Total.CompletionTokens += r.CompletionTokens
		out.Total.TotalTokens += r.TotalTokens
		out.Total.CostCents += r.CostCents
	}
	return out
}

// usageAccounts breaks the caller's Hanzo-ROUTED usage down per linked account.
// This is what Hanzo itself served and charged for, not what a provider's own plan
// metered; it answers only for the calling subject.
//
// Response: {"scope": "user", "source": "routed", "total": {"accounts": 1, "requests": 128, "promptTokens": 41200, "completionTokens": 9800, "totalTokens": 51000, "costCents": 316}, "accounts": []}
func (o ops) usageAccounts(ctx context.Context, _ *struct{}) (*AccountsUsage, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.RoutedTotals(ctx, org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "routed usage: %v", err)
	}
	out := routedAccountsView(rows)
	return &out, nil
}

// RoutedBreakdown is the package-level read the billing surface (clients/billing)
// calls to answer GET /v1/billing/usage/accounts without reaching into link's store.
// It resolves through the mounted link subsystem; (zero, false) when link is not
// mounted (a split deploy), so the caller can answer an honest "unavailable" rather
// than a fabricated empty breakdown. org+subject are the caller's validated
// principal — the billing handler passes principal.Org(c) + c.User(), never a client
// value — so this can only ever read the caller's OWN accounts.
func RoutedBreakdown(ctx context.Context, org, subject string) (AccountsUsage, bool) {
	if mounted == nil {
		return AccountsUsage{}, false
	}
	rows, err := mounted.State.store.RoutedTotals(ctx, org, subject)
	if err != nil {
		return AccountsUsage{}, false
	}
	return routedAccountsView(rows), true
}
