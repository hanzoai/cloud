package admin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
)

// createCreditGrant mints credit for one org. It is the ONE admin mint surface, and it
// does NOT mint in-process: it forwards the request to commerce's already-mint-gated
// POST /v1/billing/credit-grants, authenticated by the service token and scoped to the
// target org, then writes one tamper-evident compliance record. Commerce stays the sole
// credit ledger; this is a thin, audited relay so there is exactly one place credit is
// created.
//
// The body is commerce's OWN CreateCreditGrant contract, forwarded whole — every field
// it carries reaches commerce. The only two this layer reads are the target org (`org`,
// or `user` as the org-pool alias), which selects the namespace commerce's EdgeAuth
// trusts, and `idempotencyKey`, which makes a double-clicked grant credit once.
//
// A FAILED grant is audited too, with the request body attached: an attempted mint is
// exactly as interesting to a compliance auditor as a successful one.
//
// Example: {"org":"acme","amountCents":50000,"reason":"design partner credit",
// "idempotencyKey":"grant-2026-07-27-acme"}
// Response: {"status":"ok","msg":"","data":{"id":"cg_01J","org":"acme","amountCents":50000,
// "remainingCents":50000}}
func (o ops) createCreditGrant(ctx context.Context, in *creditGrantIn) (*rawOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	if !s.State.Commerce.Ready() {
		return &rawOut{Status: core.Err, Msg: "commerce is not configured on this deployment"}, nil
	}

	req := map[string]any(*in)
	org, _ := req["org"].(string)
	if strings.TrimSpace(org) == "" {
		org, _ = req["user"].(string)
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return &rawOut{Status: core.Err, Msg: "org is required"}, nil
	}
	idempotencyKey, _ := req["idempotencyKey"].(string)

	body, err := json.Marshal(req)
	if err != nil {
		return &rawOut{Status: core.Err, Msg: "invalid request body"}, nil
	}

	raw, err := s.State.Commerce.CreateCreditGrant(ctx, org, body, idempotencyKey)
	if err != nil {
		core.EmitAudit(s, c, "admin.customer.credit-grant", "credit-grant", org,
			req, map[string]any{"error": err.Error()},
			audit.Outcome{Result: "error", Status: 502, Reason: "credit-grant failed"})
		return &rawOut{Status: core.Err, Msg: "credit-grant failed: " + err.Error()}, nil
	}

	core.EmitAudit(s, c, "admin.customer.credit-grant", "credit-grant", org,
		nil, json.RawMessage(raw),
		audit.Outcome{Result: "success", Status: 200})
	return &rawOut{Status: core.OK, Data: json.RawMessage(raw)}, nil
}

// creditGrantIn is commerce's CreateCreditGrant body, held open rather than modelled: a
// Go struct here would silently DROP any field commerce adds, and commerce — not this
// relay — owns that contract. See the handler for the two keys admin itself reads.
type creditGrantIn map[string]any
