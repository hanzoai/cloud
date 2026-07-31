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
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/link"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// The prose, beside the handler it describes, for the same reason the rest of this
// surface declares its own: a raw *zip.Ctx handler is not a typed op, so zipdoc has no
// doc comment to lift and the document would publish an operationId and nothing else.
func init() {
	openapi.Describe("/v1/billing/usage/accounts", http.MethodGet,
		"Traffic through the accounts the CALLER linked, per account",
		"Answers per-account totals for the linked provider accounts the gateway ROUTED this "+
			"caller's traffic through — requests, prompt and completion tokens, recorded cost — "+
			"plus their honest sum.\n\n"+
			"This is the one read in the billing namespace scoped to the PERSON, not the org. "+
			"Rows are keyed on (validated org, validated user), so a caller sees the accounts THEY "+
			"linked and never a colleague's, even inside one org — everything else under "+
			"/v1/billing is org-wide. Neither key is ever read from the request.\n\n"+
			"It is a ROUTING counter, not the money ledger. `costCents` is 0 for an account billed "+
			"by its own subscription (`billing: \"plan\"`), where the plan pays the provider "+
			"directly, so these totals do not reconcile against what the org was charged. "+
			"/v1/billing/usage is the charged ledger.\n\n"+
			"401 without a validated principal. Where the linked-account plane is not resident the "+
			"answer is an honest 501 — never an empty breakdown, which would read as no usage.")
}

// usageAccounts serves the caller's per-account routed-usage breakdown.
func usageAccounts(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view billing")
	}
	// c.User() is guaranteed non-empty once principal.Org returned ok (Org composes
	// Validated, which is c.User() != ""). It is the account-scope subject, taken from
	// the validated principal — never a request field.
	view, ok := link.RoutedBreakdown(c.Context(), org, c.User())
	if !ok {
		// The linked-account plane is not co-resident (a split deploy). Honest
		// "unavailable" — never a fabricated empty breakdown that reads as "no usage".
		return zip.Errorf(http.StatusNotImplemented, "per-account usage is not available on this deployment")
	}
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, view)
}
