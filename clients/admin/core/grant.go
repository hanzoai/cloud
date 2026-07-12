package core

// The ONE credit-write path + the ONE tamper-evident audit emit. ApplyGrant is the
// single core shared by POST /v1/admin/customers/:org/credit (org from the path) and
// POST /v1/admin/grants (org from the body): validate the amount + target org, deposit
// into the org's commerce ledger (trial vs prepaid by source), and record the audit
// row. One path, one way to grant.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/money"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/types"
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

// grantIdempotencyKey derives the DETERMINISTIC commerce idempotency key for a grant from
// its (org, amount, currency, source) BOUND to the operator-supplied Idempotency-Key nonce
// — so a retried grant (a commit-then-timeout re-submit carrying the SAME nonce) dedupes at
// commerce (X-Idempotency-Key, at-most-once), while two DISTINCT grants — even same org +
// amount — never collide. Binding the amount/currency/source into the hash means a nonce
// accidentally reused for a DIFFERENT grant still lands (a different key), so dedup can
// never silently DROP a legitimate distinct grant.
//
// Empty when the operator supplied no nonce: without a stable per-attempt id there is no
// value that is both retry-stable AND grant-unique, so we do NOT fabricate one (a content
// -only hash would wrongly dedupe two legitimate identical comps). The deposit is then
// additive — the pre-existing behavior. Effective end-to-end once the operator console
// sends an Idempotency-Key per grant attempt (reused verbatim on retry); commerce already
// enforces the dedup (api/billing/deposit.go).
func grantIdempotencyKey(c *zip.Ctx, org, currency, source string, amountCents int64) string {
	nonce := strings.TrimSpace(c.Header("Idempotency-Key"))
	if nonce == "" {
		nonce = strings.TrimSpace(c.Header("X-Idempotency-Key"))
	}
	if nonce == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		org, strconv.FormatInt(amountCents, 10), currency, source, nonce,
	}, "|")))
	return "grant-" + hex.EncodeToString(sum[:])
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

	// FAIL-CLOSED durability (SOC2 AU-2/AU-5): a credit grant moves REAL money and MUST
	// leave a durable, tamper-evident record. If this deployment has no audit store to
	// record into, REFUSE the grant BEFORE any money moves — never an unaudited money
	// move. A nil store is not a production state: cloud requires a persistent data dir
	// for the trail (audit_serve.go), and nil arises only from the explicit
	// CLOUD_AUDIT_DISABLED dev opt-out, on which moving money is not a supported op.
	if s.State.AuditStore == nil {
		return c.JSON(503, map[string]any{"status": "error", "msg": "grant refused: no durable audit store is configured on this deployment; a credit grant must be recorded before money moves", "data": nil})
	}

	tag, source := grantTag(req.Source)
	notes := grantNote(c, req.Reason)

	// ONE credit money-move: the co-resident native finance wallet is preferred (the ai
	// prepaid gate + the edge meter read/debit THAT wallet, so a grant MUST land there),
	// with the commerce HTTP deposit as the split-deploy fallback. Both return the
	// pre-balance (recorded even on failure), the entry/transaction id, and the post-balance,
	// so the audit + response below are one shape regardless of which path moved the money.
	before, txID, after, derr := grantDeposit(s, c, org, currency, notes, tag, source, req.AmountCents)
	if derr != nil {
		// The grant did not land — record the FAILED attempt (accountability), then
		// surface the error. Never report a grant that failed as success.
		EmitAudit(s, c, "admin.customer.credit", "credit", org,
			map[string]any{"balanceCents": before},
			map[string]any{"amountCents": req.AmountCents, "currency": currency, "reason": req.Reason, "source": source, "error": derr.Error()},
			audit.Outcome{Result: "error", Status: 200, Reason: "grant failed"})
		return Fail(c, "grant failed: "+derr.Error())
	}

	EmitAudit(s, c, "admin.customer.credit", "credit", org,
		map[string]any{"balanceCents": before},
		map[string]any{"balanceCents": after, "grantedCents": req.AmountCents, "currency": currency, "reason": req.Reason, "source": source, "transactionId": txID},
		audit.Outcome{Result: "success", Status: 200})

	return OK(c, map[string]any{
		"org":           org,
		"grantedCents":  req.AmountCents,
		"currency":      currency,
		"source":        source,
		"balanceCents":  after,
		"transactionId": txID,
	})
}

// grantDeposit performs the ONE credit money-move for a grant. It prefers the co-resident
// native finance wallet — the ai prepaid gate and the edge meter read/debit THAT wallet,
// so an admin grant must credit it (subject == the org slug, the org-pool wallet) — and
// falls back to the commerce HTTP deposit only when no finance ledger is co-resident (a
// split deploy). It returns the pre-balance (so ApplyGrant can audit even a FAILED
// attempt), the entry/transaction id, and the post-balance, so the audit + response are
// one shape regardless of which path moved the money.
func grantDeposit(s *cloud.Service[State], c *zip.Ctx, org, currency, notes, tag, source string, amountCents int64) (before int64, txID string, after int64, err error) {
	ctx := c.Context()
	if fin := finance.Current(); fin != nil {
		before, _ = fin.BalanceCents(ctx, org, org, currency, false)
		id, derr := fin.Deposit(ctx, types.DepositInput{
			Org: org, Subject: org, Cents: amountCents, Currency: currency, Notes: notes, Tags: tag,
		})
		if derr != nil {
			return before, "", before, derr
		}
		after, _ = fin.BalanceCents(ctx, org, org, currency, false)
		return before, id, after, nil
	}
	// Split deploy: no co-resident finance ledger → the commerce billing HTTP deposit, with
	// its operator-nonce idempotency key so a retried grant dedupes at commerce.
	beforeC, _ := s.State.Commerce.Credits(ctx, org)
	idem := grantIdempotencyKey(c, org, currency, source, amountCents)
	res, derr := s.State.Commerce.Deposit(ctx, org, money.Cents(amountCents), currency, notes, tag, idem)
	if derr != nil {
		return int64(beforeC), "", int64(beforeC), derr
	}
	afterC, _ := s.State.Commerce.Credits(ctx, org)
	return int64(beforeC), res.TxID, int64(afterC), nil
}

// EmitAudit writes ONE compliance record for a management action to cloud's
// tamper-evident trail: who (the validated SuperAdmin from the sanitized identity —
// the gate already proved it), what (action + resource), the redacted before/after, and
// the outcome. This is the "before/after on a config-affecting change" the request-level
// middleware record cannot carry (it never reads bodies). Best-effort: a failure here is
// logged loud, never silent, and never double-fails the response. A nil store
// (unconfigured deployment) is a no-op, like the middleware.
func EmitAudit(s *cloud.Service[State], c *zip.Ctx, action, resType, resID string, before, after any, outcome audit.Outcome) {
	if s.State.AuditStore == nil {
		return
	}
	org, _ := principal.Org(c)
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: strings.TrimSpace(c.User()), Email: strings.TrimSpace(c.UserEmail())},
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
