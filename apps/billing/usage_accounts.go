package billing

// usage_accounts.go answers GET /v1/billing/usage/accounts — the per-account
// SERVER-ROUTED usage breakdown the unified dashboard reads beside /v1/billing/usage.
//
// TENANT ISOLATION (the whole point, same as usage/balance). The org is the VALIDATED
// IAM owner claim (principal.Org — the trusted X-Org-Id the identity middleware
// minted, HIP-0026; NEVER a client header), and the subject is the validated user
// (c.User(), which principal.Org having returned ok guarantees non-empty). The
// breakdown is read from the linked-account plane scoped to exactly that (org,
// subject), so a caller sees ONLY their OWN linked accounts — no client-supplied
// subject/account/org param is ever consulted.
//
// The data lives in clients/link (RoutedBreakdown); this is a thin, org-scoped read
// twin of usage(), mounted in the billing namespace so the dashboard's usage panels
// find it where they already read /v1/billing/usage.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/apps/link"
	"github.com/zap-proto/zip"
)

// usageAccounts breaks the caller's own routed spend down per linked account —
// requests, tokens and cost cents for each, plus their total. It reports only the
// caller's own accounts; a deployment without the linked-account plane answers 501
// rather than a fabricated empty breakdown.
//
// Response: {"scope": "user", "source": "routed", "total": {"accounts": 1, "requests": 42, "costCents": 130}, "accounts": [{"account": "anthropic", "requests": 42, "costCents": 130}]}
func (o ops) usageAccounts(ctx context.Context, _ *noArgs) (*link.AccountsUsage, error) {
	c, org, err := caller(ctx, "billing")
	if err != nil {
		return nil, err
	}
	// c.User() is guaranteed non-empty once principal.Org returned ok (Org composes
	// Validated, which is c.User() != ""). It is the account-scope subject, taken from
	// the validated principal — never a request field.
	view, ok := link.RoutedBreakdown(ctx, org, c.User())
	if !ok {
		// The linked-account plane is not co-resident (a split deploy). Honest
		// "unavailable" — never a fabricated empty breakdown that reads as "no usage".
		return nil, zip.Errorf(http.StatusNotImplemented, "per-account usage is not available on this deployment")
	}
	c.SetHeader("Cache-Control", "no-store")
	return &view, nil
}
