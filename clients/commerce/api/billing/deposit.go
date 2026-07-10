package billing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/credit"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// depositMaxCents is the server-authoritative ceiling on a SINGLE Deposit/Refund,
// defense-in-depth BEHIND the PlatformOnly gate: even a leaked service token or a
// fat-fingered internal call cannot mint an absurd balance in one shot. The
// default ($1,000,000) is generous so legitimate settlement/promo deposits are
// untouched; tune per-deploy with COMMERCE_DEPOSIT_MAX_CENTS (envCents fails safe
// to the default on unset/non-positive). Reuses the topup env helper — one way to
// read a cents bound.
func depositMaxCents() int64 {
	return envCents("COMMERCE_DEPOSIT_MAX_CENTS", 100_000_000)
}

type depositRequest struct {
	User      string `json:"user"`
	Currency  string `json:"currency"`
	Amount    int64  `json:"amount"` // cents
	Notes     string `json:"notes"`
	Tags      string `json:"tags"`
	ExpiresIn int    `json:"expiresIn"` // days until expiry (0 = no expiry)
}

// Deposit creates a deposit (credit) transaction for an IAM user.
//
//	POST /v1/billing/deposit
//
// Used by internal services to add funds to a user's account (payment
// processor settlement, manual credit, promotional grants, etc.).
func Deposit(c *gin.Context) {
	org := middleware.GetOrganization(c)
	// ONE central commerce ledger, org-scoped by the namespace column (New binds
	// the namespace from org.Namespaced(c) — the same store+scoping the LLM
	// gateway prepaid gate and every balance read use: tier.go/zap.go/usage.go and
	// models/transaction/util). NOT NewNamespaced: that split deposits into
	// per-org SQLite FILES (/app/data/orgs/<org>/data.db) while the gate read the
	// central store → every balance read $0 → 402 on all AI. Tenancy is a value
	// (the row's org namespace + destinationId), not a place (a physical file).
	db := datastore.New(org.Namespaced(c))

	var req depositRequest
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

	// Server-authoritative ceiling (H1). Bounds a single mint even for a trusted
	// caller — no unbounded credit in one request.
	if maxCents := depositMaxCents(); req.Amount > maxCents {
		http.Fail(c, 400, fmt.Sprintf("amount %d exceeds maximum deposit of %d cents", req.Amount, maxCents), nil)
		return
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	// Step 4: when the chain-backed ledger is enabled, a deposit MINTS on-chain
	// HUSD to the org's derived address (bucketed by its tags) instead of writing
	// a DB credit row — the value is created by the treasury key, not this process.
	// The X-Idempotency-Key (if any) makes the mint exactly-once; without one,
	// distinct deposits are legitimately additive (a fresh key each call).
	if chainCreditEnabled() {
		mintKey := randomIdemKey("deposit:" + req.User + ":")
		if hdr := strings.TrimSpace(c.GetHeader("X-Idempotency-Key")); hdr != "" {
			mintKey = "deposit:" + req.User + ":" + hdr
		}
		reason := req.Notes
		if reason == "" {
			reason = "deposit"
		}
		rc, err := chainMintCredit(c, org, req.User, req.Amount, bucketForTags(req.Tags), reason, mintKey)
		if err != nil {
			log.Error("chain deposit mint failed for %s: %v", req.User, err, c)
			http.Fail(c, 502, "on-chain credit mint failed", err)
			return
		}
		c.JSON(201, gin.H{
			"transactionId": rc.TxHash,
			"user":          req.User,
			"amount":        req.Amount,
			"currency":      cur,
			"type":          "deposit",
			"tags":          req.Tags,
			"onChain":       true,
			"txHash":        rc.TxHash,
			"replayed":      rc.Replayed,
		})
		return
	}

	// Optional idempotency (H1). A caller-supplied X-Idempotency-Key makes a
	// retry / double-submit credit AT MOST ONCE: a completed key replays the
	// stored body, an in-flight key 409s. Absent a key there is no guard —
	// distinct deposits to the same user (repeated settlements) are legitimately
	// additive, so we never dedupe by amount. Scoped to the subject so keys never
	// collide across users; the datastore is already org-namespaced.
	idemKey := strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	var idemRec *idempotencykey.IdempotencyKey
	if idemKey != "" {
		rec, replay, gerr := idempotencykey.Begin(db, "billing-deposit:"+req.User, idemKey)
		if gerr != nil {
			log.Error("deposit idempotency Begin failed (user=%s): %v", req.User, gerr, c)
		} else if replay {
			if rec.Status == idempotencykey.StatusCompleted && rec.Response != "" {
				c.Data(200, "application/json", []byte(rec.Response))
				return
			}
			http.Fail(c, 409, "deposit already in progress", nil)
			return
		} else {
			idemRec = rec
		}
	}

	notes := req.Notes
	if notes == "" {
		notes = fmt.Sprintf("Deposit: %d cents %s", req.Amount, cur)
	}

	trans := transaction.New(db)
	trans.Type = transaction.Deposit
	trans.DestinationId = req.User
	trans.DestinationKind = "iam-user"
	trans.Currency = cur
	trans.Amount = currency.Cents(req.Amount)
	trans.Notes = notes
	trans.Tags = req.Tags

	if req.ExpiresIn > 0 {
		trans.ExpiresAt = time.Now().AddDate(0, 0, req.ExpiresIn)
	}

	if org.TestMode() {
		trans.Test = true
	}

	if err := trans.Create(); err != nil {
		// No balance moved — release the guard so a later retry is not wedged.
		if idemRec != nil {
			_ = idemRec.Delete()
		}
		log.Error("Failed to create deposit transaction: %v", err, c)
		http.Fail(c, 500, "failed to create deposit", err)
		return
	}

	resp := gin.H{
		"transactionId": trans.Id(),
		"user":          req.User,
		"amount":        req.Amount,
		"currency":      cur,
		"type":          "deposit",
		"tags":          req.Tags,
	}
	if !trans.ExpiresAt.IsZero() {
		resp["expiresAt"] = trans.ExpiresAt
	}

	// Seal the guard with the exact success body so a same-key retry replays it
	// verbatim (no second credit).
	if idemRec != nil {
		if body, mErr := json.Marshal(resp); mErr == nil {
			_ = idempotencykey.Complete(idemRec, string(body))
		}
	}

	c.JSON(201, resp)
}

// GrantStarterCredit creates a $100 USD starter credit for a new org.
// The credit expires after 365 days if unused. Tagged "starter-credit"
// so it can be identified in transaction history.
//
// No payment method is required — the starter credit is the on-signup grant
// that lets a new org evaluate the platform before adding a card. A verified
// payment method is required only to top up BEYOND the starter credit.
// Idempotent: deduped by the starter-credit tag.
//
//	POST /v1/billing/credit
func GrantStarterCredit(c *gin.Context) {
	org := middleware.GetOrganization(c)
	// Central ledger, org-scoped by namespace (see Deposit above).
	db := datastore.New(org.Namespaced(c))

	var req struct {
		User string `json:"user"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	req.User = strings.ToLower(strings.TrimSpace(req.User))
	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}

	// Step 4: chain-backed path mints the welcome credit on-chain (idempotent by
	// the deterministic welcome key → one grant per subject, ever), replacing the
	// tag-dedupe DB write. The SAME key is shared by /me/welcome and /grant-starter
	// so no subject can be double-granted across the three welcome endpoints.
	if chainCreditEnabled() {
		rc, err := chainMintCredit(c, org, req.User, int64(credit.StarterCreditCents), treasury.BucketCredit,
			"welcome-credit", welcomeMintKey(org, req.User))
		if err != nil {
			log.Error("chain welcome credit mint failed for %s: %v", req.User, err, c)
			http.Fail(c, 502, "on-chain welcome credit mint failed", err)
			return
		}
		status := 201
		if rc.Replayed {
			status = 200 // already granted — matches the DB path's idempotent shape
		}
		c.JSON(status, gin.H{
			"user":     req.User,
			"granted":  !rc.Replayed,
			"amount":   credit.StarterCreditCents,
			"currency": "usd",
			"onChain":  true,
			"txHash":   rc.TxHash,
		})
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)

	// No payment-method gate: the starter credit is grantable WITHOUT a card.
	// A verified payment method is required only for top-up beyond the starter
	// credit (see TopupWithToken/Topup). The tag dedupe below still prevents
	// double-dipping.

	// Check if starter credit was already granted (prevent double-dipping).
	existingTrans := make([]*transaction.Transaction, 0)
	tq := transaction.Query(db).Ancestor(rootKey).
		Filter("DestinationId=", req.User).
		Filter("Tags=", credit.StarterCreditTag)
	if _, err := tq.Limit(1).GetAll(&existingTrans); err == nil && len(existingTrans) > 0 {
		// Idempotent no-op: the starter credit is one-per-user, so a repeat
		// claim is an expected outcome, not a failure. Return 200 (matching
		// PostMyWelcome's already-granted shape) instead of 409 so the billing
		// UI's on-load claim doesn't surface a red error in the browser console
		// for users who already have their credit.
		c.JSON(200, gin.H{
			"user":    req.User,
			"granted": false,
			"reason":  "already_granted",
		})
		return
	}

	trans := transaction.New(db)
	trans.Type = transaction.Deposit
	trans.DestinationId = req.User
	trans.DestinationKind = "iam-user"
	trans.Currency = "usd"
	trans.Amount = currency.Cents(credit.StarterCreditCents)
	trans.Notes = "Welcome credit: $100.00 USD (expires in 365 days)"
	trans.Tags = credit.StarterCreditTag
	trans.ExpiresAt = time.Now().AddDate(0, 0, credit.StarterCreditDays)
	trans.Metadata = Map{
		"creditType": "starter",
		"expiryDays": credit.StarterCreditDays,
	}

	if org.TestMode() {
		trans.Test = true
	}

	// Server-FIXED (StarterCreditCents), tag-idempotent welcome credit to the
	// caller's OWN subject — amount not attacker-controlled — so authorize this
	// write at the ledger sink.
	trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	if err := trans.Create(); err != nil {
		log.Error("Failed to grant starter credit: %v", err, c)
		http.Fail(c, 500, "failed to grant starter credit", err)
		return
	}

	c.JSON(201, gin.H{
		"transactionId": trans.Id(),
		"user":          req.User,
		"amount":        credit.StarterCreditCents,
		"currency":      "usd",
		"type":          "deposit",
		"tags":          credit.StarterCreditTag,
		"expiresAt":     trans.ExpiresAt,
	})
}
