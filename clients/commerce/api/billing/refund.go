package billing

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type refundRequest struct {
	User                  string `json:"user"`
	Currency              string `json:"currency"`
	Amount                int64  `json:"amount"` // cents
	OriginalTransactionID string `json:"originalTransactionId"`
	Notes                 string `json:"notes"`
}

// Refund creates a deposit tagged "refund" to REVERSE a prior charge, correcting
// an overcharge. The metadata links back to the original transaction.
//
// H1 (money-correctness). Previously this minted an arbitrary, uncapped credit
// against an UNVALIDATED originalTransactionId — a refund could exceed the
// original, name a non-existent or foreign transaction, refund a credit (doubling
// a deposit), or be replayed to double-refund. It is now fully validated and
// idempotent: the original MUST exist in THIS org's ledger, be a charge
// (Withdraw) for the SAME subject, and bound the amount (refund ≤ original); and
// there is AT MOST ONE refund per original transaction (keyed idempotency), so a
// retry replays and any second refund of the same charge is refused. Fail-closed
// throughout.
//
//	POST /v1/billing/refund
func Refund(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req refundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	req.User = strings.ToLower(strings.TrimSpace(req.User))
	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}

	if req.Amount <= 0 {
		http.Fail(c, 400, "amount must be positive", nil)
		return
	}
	// Server-authoritative ceiling (shared with Deposit) — defense in depth.
	if maxCents := depositMaxCents(); req.Amount > maxCents {
		http.Fail(c, 400, fmt.Sprintf("amount %d exceeds maximum refund of %d cents", req.Amount, maxCents), nil)
		return
	}

	req.OriginalTransactionID = strings.TrimSpace(req.OriginalTransactionID)
	if req.OriginalTransactionID == "" {
		http.Fail(c, 400, "originalTransactionId is required", nil)
		return
	}

	// The original MUST exist in this org's namespaced ledger. GetById returns
	// datastore.ErrNoSuchEntity for an unknown id → 404 (never a silent mint).
	orig := transaction.New(db)
	if err := orig.GetById(req.OriginalTransactionID); err != nil {
		http.Fail(c, 404, "original transaction not found", err)
		return
	}
	// Only a CHARGE is refundable. Refunding a Deposit/credit would double it.
	if orig.Type != transaction.Withdraw {
		http.Fail(c, 400, "original transaction is not a refundable charge", nil)
		return
	}
	// The refund must return money to the party that was charged — never route
	// one subject's charge to another subject's balance.
	if !strings.EqualFold(strings.TrimSpace(orig.DestinationId), req.User) {
		http.Fail(c, 400, "refund subject does not match the original transaction", nil)
		return
	}
	// Bound: never refund more than was charged.
	if req.Amount > int64(orig.Amount) {
		http.Fail(c, 400, fmt.Sprintf("refund %d exceeds original charge of %d cents", req.Amount, int64(orig.Amount)), nil)
		return
	}

	// Currency defaults to (and must match) the original charge.
	cur := currency.Type(strings.ToLower(strings.TrimSpace(req.Currency)))
	if cur == "" {
		cur = orig.Currency
	}
	if cur == "" {
		cur = "usd"
	}
	if orig.Currency != "" && cur != orig.Currency {
		http.Fail(c, 400, "refund currency does not match the original transaction", nil)
		return
	}

	// Idempotency (H1): AT MOST ONE refund per original transaction. Keyed on the
	// original tx id (NOT a client-chosen key — the invariant is per-charge), so a
	// retry replays the stored result and a second refund of the same charge is
	// refused. Scoped to the subject; the datastore is already org-namespaced.
	// Fail CLOSED (503) if the guard store is unreachable — a refund we cannot
	// dedupe must not run.
	rec, replay, gerr := idempotencykey.Begin(db, "billing-refund:"+req.User, req.OriginalTransactionID)
	if gerr != nil {
		log.Error("refund idempotency Begin failed (user=%s, orig=%s): %v", req.User, req.OriginalTransactionID, gerr, c)
		http.Fail(c, 503, "refund temporarily unavailable; retry", gerr)
		return
	}
	if replay {
		if rec.Status == idempotencykey.StatusCompleted && rec.Response != "" {
			c.Data(200, "application/json", []byte(rec.Response))
			return
		}
		http.Fail(c, 409, "a refund for this transaction is already in progress", nil)
		return
	}

	notes := req.Notes
	if notes == "" {
		notes = fmt.Sprintf("Refund: %d cents %s (original tx: %s)", req.Amount, cur, req.OriginalTransactionID)
	}

	trans := transaction.New(db)
	trans.Type = transaction.Deposit
	trans.DestinationId = req.User
	trans.DestinationKind = "iam-user"
	trans.Currency = cur
	trans.Amount = currency.Cents(req.Amount)
	trans.Notes = notes
	trans.Tags = "refund"
	trans.Metadata = Map{
		"refundType":            "billing-correction",
		"originalTransactionId": req.OriginalTransactionID,
	}

	if org.TestMode() {
		trans.Test = true
	}

	if err := trans.Create(); err != nil {
		// No money moved — release the guard so a later retry is not wedged.
		_ = rec.Delete()
		log.Error("Failed to create refund transaction: %v", err, c)
		http.Fail(c, 500, "failed to create refund", err)
		return
	}

	resp := gin.H{
		"transactionId":         trans.Id(),
		"user":                  req.User,
		"amount":                req.Amount,
		"currency":              cur,
		"type":                  "deposit",
		"tags":                  "refund",
		"originalTransactionId": req.OriginalTransactionID,
	}
	// Seal the guard with the exact success body so a same-tx retry replays it.
	if body, mErr := json.Marshal(resp); mErr == nil {
		_ = idempotencykey.Complete(rec, string(body))
	}

	c.JSON(201, resp)
}
