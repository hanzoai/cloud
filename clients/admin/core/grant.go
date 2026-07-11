package core

// The ONE credit-write path + the ONE tamper-evident audit emit. ApplyGrant is the
// single core shared by POST /v1/admin/customers/:org/credit (org from the path) and
// POST /v1/admin/grants (org from the body): validate the amount + target org, deposit
// into the org's commerce ledger (trial vs prepaid by source), and record the audit
// row. One path, one way to grant.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/money"
	"github.com/zap-proto/zip"
)

// CreditRequest is the grant body. AmountCents is the credit to add (positive only — a
// grant, never a silent debit). Reason is the operator's justification, recorded in the
// audit trail's before/after (refund / comp / support).
type CreditRequest struct {
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Reason      string `json:"reason"`
	// Source splits the grant into the commerce ledger's two money buckets:
	//   - "trial"   (default) — a non-cash promo/comp credit: spendable on non-premium
	//                metered usage only, NEVER refundable cash and NEVER paid out.
	//   - "prepaid" — real money added to the customer's cash balance. Refundable,
	//                GPU-eligible.
	// Unknown/empty → trial (fail-closed to non-cash).
	Source string `json:"source"`
}

// maxGrantCents caps a single grant at $100,000 — a guardrail against a fat-finger
// operator credit, not a policy limit. The cap keeps a typo from minting a fortune.
const maxGrantCents int64 = 100 * 100 * 1000

// grantTag maps a grant source to the commerce deposit Tags that billing/bucket
// DepositKind classifies into Credit (trial) vs Prepaid (real money). Default
// (empty/unknown/"trial") is the non-cash Credit bucket — a staff comp is never
// silently minted as payout-able real money.
func grantTag(source string) (tag, normalized string) {
	if strings.ToLower(strings.TrimSpace(source)) == "prepaid" {
		return "admin-grant", "prepaid" // DepositKind: bare → Prepaid (real money)
	}
	return "grant:admin", "trial" // DepositKind: grant:* → Credit (non-cash trial)
}

// grantNote composes the deposit note from the operator's reason (bounded), so the
// commerce ledger row itself carries the justification alongside the audit trail.
func grantNote(c *zip.Ctx, reason string) string {
	r := strings.TrimSpace(reason)
	if len(r) > 200 {
		r = r[:200]
	}
	by := strings.TrimSpace(c.UserEmail())
	if by == "" {
		by = strings.TrimSpace(c.User())
	}
	if r == "" {
		r = "operator credit"
	}
	if by != "" {
		return fmt.Sprintf("Admin grant by %s: %s", by, r)
	}
	return "Admin grant: " + r
}

// ApplyGrant validates the amount + target org, deposits into the org's commerce ledger
// (trial vs prepaid by source), and records the tamper-evident audit row. One path, one
// way to grant.
func ApplyGrant(s *cloud.Service[State], c *zip.Ctx, org string, req CreditRequest) error {
	ctx := c.Context()
	cr := CallerCreds(c)
	if req.AmountCents <= 0 {
		return Fail(c, "amountCents must be positive")
	}
	if req.AmountCents > maxGrantCents {
		return Fail(c, fmt.Sprintf("amountCents exceeds the %d-cent per-grant cap", maxGrantCents))
	}
	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "usd"
	}

	// Validate the target is a REAL org (never mint an orphan wallet on a typo).
	o, err := FindOrg(s, ctx, cr, org)
	if err != nil {
		return Fail(c, err.Error())
	}
	if o == nil {
		return c.JSON(404, map[string]any{"status": "error", "msg": "customer not found", "data": nil})
	}

	before, _ := s.State.Commerce.Credits(ctx, org)

	tag, source := grantTag(req.Source)
	notes := grantNote(c, req.Reason)
	res, derr := s.State.Commerce.Deposit(ctx, org, money.Cents(req.AmountCents), currency, notes, tag)
	if derr != nil {
		// The grant did not land — record the FAILED attempt (accountability), then
		// surface the error. Never report a grant that failed as success.
		EmitAudit(s, c, "admin.customer.credit", "credit", org,
			map[string]any{"balanceCents": before},
			map[string]any{"amountCents": req.AmountCents, "currency": currency, "reason": req.Reason, "source": source, "error": derr.Error()},
			audit.Outcome{Result: "error", Status: 200, Reason: "grant failed"})
		return Fail(c, "grant failed: "+derr.Error())
	}

	after, _ := s.State.Commerce.Credits(ctx, org)
	EmitAudit(s, c, "admin.customer.credit", "credit", org,
		map[string]any{"balanceCents": before},
		map[string]any{"balanceCents": after, "grantedCents": req.AmountCents, "currency": currency, "reason": req.Reason, "source": source, "transactionId": res.TxID},
		audit.Outcome{Result: "success", Status: 200})

	return OK(c, map[string]any{
		"org":           org,
		"grantedCents":  req.AmountCents,
		"currency":      currency,
		"source":        source,
		"balanceCents":  after,
		"transactionId": res.TxID,
	})
}

// EmitAudit writes ONE compliance record for a management action to cloud's
// tamper-evident trail: who (the validated global admin from the sanitized identity —
// the gate already proved it), what (action + resource), the redacted before/after, and
// the outcome. This is the "before/after on a config-affecting change" the request-level
// middleware record cannot carry (it never reads bodies). Best-effort: a failure here is
// logged loud, never silent, and never double-fails the response. A nil store
// (unconfigured deployment) is a no-op, like the middleware.
func EmitAudit(s *cloud.Service[State], c *zip.Ctx, action, resType, resID string, before, after any, outcome audit.Outcome) {
	if s.State.AuditStore == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: strings.TrimSpace(c.Org()), Sub: strings.TrimSpace(c.User()), Email: strings.TrimSpace(c.UserEmail())},
		Action:    action,
		Resource:  audit.Resource{Type: resType, ID: resID},
		Auth:      audit.AuthContext{Method: "jwt", IsAdmin: c.IsAdmin()},
		Outcome:   outcome,
		UserAgent: c.Header("User-Agent"),
		RequestID: c.RequestID(),
		Method:    c.Method(),
		Path:      c.Path(),
		Before:    audit.Redact(mustJSON(before)),
		After:     audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.AuditStore.Append(c.Context(), rec); err != nil {
		c.Log().Error("admin: audit emit failed (request-level record still applies)",
			"action", action, "resource", resType, "id", resID, "err", err)
	}
}

// mustJSON marshals v to raw JSON for the audit before/after, returning an empty object
// on the (unexpected) marshal error rather than panicking — a metadata diff must never
// crash a money/access action.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
