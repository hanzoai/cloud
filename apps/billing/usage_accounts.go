package billing

// usage_accounts.go answers GET /v1/billing/usage/accounts — the per-account
// SERVER-ROUTED usage breakdown the unified dashboard reads beside /v1/billing/usage.
//
// TENANT ISOLATION (the whole point, same as usage/balance). The org is the VALIDATED
// IAM owner claim (never a client header), and the subject is the validated user —
// which the org having resolved guarantees non-empty. The breakdown is read from the
// linked-account plane scoped to exactly that (org, subject), so a caller sees ONLY
// their OWN linked accounts — no client-supplied subject/account/org param is ever
// consulted.
//
// The data lives in clients/link (RoutedBreakdown); this is a thin, org-scoped read
// twin of usage(), mounted in the billing namespace so the dashboard's usage panels
// find it where they already read /v1/billing/usage. It is the one read under
// /v1/billing that owns its whole shape, so it is a TYPED op where its passthrough
// siblings stay raw.

import (
	"context"
	"github.com/hanzoai/cloud"
	"net/http"

	"github.com/hanzoai/cloud/apps/link"
	"github.com/zap-proto/zip"
)

// accounts is the GET /v1/billing/usage/accounts answer — link's breakdown
// re-declared here so a per-tenant money answer can state its cache directive
// (zip.HeaderCoder); the JSON is link.AccountsUsage's, byte for byte.
type accounts link.AccountsUsage

func (accounts) ResponseHeaders() map[string]string { return noStore() }

// usageAccounts answers per-account totals for the linked provider accounts the
// gateway ROUTED this caller's traffic through — requests, prompt and completion
// tokens, recorded cost — plus their honest sum.
//
// This is the one read in the billing namespace scoped to the PERSON, not the
// org. Rows are keyed on (validated org, validated user), so a caller sees the
// accounts THEY linked and never a colleague's, even inside one org — everything
// else under /v1/billing is org-wide. Neither key is ever read from the request
// body or the query.
//
// It is a ROUTING counter, not the money ledger. `costCents` is 0 for an account
// billed by its own subscription, where the plan pays the provider directly, so
// these totals do not reconcile against what the org was charged.
// /v1/billing/usage is the charged ledger.
//
// 401 without a validated principal. Where the linked-account plane is not
// resident the answer is an honest 501 — never an empty breakdown, which would
// read as no usage.
func (o ops) usageAccounts(ctx context.Context, _ *cloud.Unit) (*accounts, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	view, ok := link.RoutedBreakdown(ctx, org, user)
	if !ok {
		// The linked-account plane is not co-resident (a split deploy). Honest
		// "unavailable" — never a fabricated empty breakdown that reads as "no usage".
		return nil, zip.Errorf(http.StatusNotImplemented, "per-account usage is not available on this deployment")
	}
	return (*accounts)(&view), nil
}
