package admin

import (
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// createCreditGrant is the admin mint surface: POST /v1/admin/credit-grants.
//
// SuperAdmin ONLY (wired through core.Guard). It does NOT mint in-process — it
// forwards the request VERBATIM to commerce's already-mint-gated
// POST /v1/billing/credit-grants (middleware.Mint → PlatformOnly), authenticated
// by COMMERCE_SERVICE_TOKEN and scoped to the target org, and writes ONE
// tamper-evident compliance record. Commerce stays the single credit-grant ledger;
// this is a thin, audited relay so there is exactly one place credit is minted.
//
// The body is commerce's own CreateCreditGrant contract; the only field this layer
// reads is the target org (`org`, or `user` as the org-pool alias) to select the
// per-org namespace commerce's EdgeAuth trusts after verifying the service token.
func createCreditGrant(s *cloud.Service[core.State], c *zip.Ctx) error {
	if !s.State.Commerce.Ready() {
		return core.Fail(c, "commerce is not configured on this deployment")
	}

	var req map[string]any
	if err := c.Bind(&req); err != nil {
		return core.Fail(c, "invalid request body")
	}

	org, _ := req["org"].(string)
	if strings.TrimSpace(org) == "" {
		org, _ = req["user"].(string)
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return core.Fail(c, "org is required")
	}
	idempotencyKey, _ := req["idempotencyKey"].(string)

	body, err := json.Marshal(req)
	if err != nil {
		return core.Fail(c, "invalid request body")
	}

	raw, err := s.State.Commerce.CreateCreditGrant(c.Context(), org, body, idempotencyKey)
	if err != nil {
		core.EmitAudit(s, c, "admin.customer.credit-grant", "credit-grant", org,
			req, map[string]any{"error": err.Error()},
			audit.Outcome{Result: "error", Status: 502, Reason: "credit-grant failed"})
		return core.Fail(c, "credit-grant failed: "+err.Error())
	}

	core.EmitAudit(s, c, "admin.customer.credit-grant", "credit-grant", org,
		nil, json.RawMessage(raw),
		audit.Outcome{Result: "success", Status: 200})
	return core.OK(c, json.RawMessage(raw))
}
