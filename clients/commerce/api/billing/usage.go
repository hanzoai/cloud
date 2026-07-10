package billing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/meter"
	"github.com/hanzoai/cloud/clients/commerce/models/spendalert"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type usageRequest struct {
	User     string `json:"user"`
	Currency string `json:"currency"`
	Amount   int64  `json:"amount"` // cents (back-compat; prefer amountMicros)
	// AmountMicros is the debit in micro-USD (1e6 = $1). Preferred over Amount:
	// it carries sub-cent precision so tiny spends aren't lost to cent rounding.
	// Chat's tokenValue is already micro-USD (1e6 tokenCredits = $1), so it maps
	// 1:1. Zero/absent => fall back to Amount*10000.
	AmountMicros int64  `json:"amountMicros"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	// Project / Service attribute this spend to a scope for per-scope caps
	// (issue #70). Empty = the org-wide default scope.
	Project          string `json:"project"`
	Service          string `json:"service"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	TotalTokens      int    `json:"totalTokens"`
	RequestID        string `json:"requestId"`
	Premium          bool   `json:"premium"`
	Stream           bool   `json:"stream"`
	Status           string `json:"status"`
	ClientIP         string `json:"clientIp"`
}

// GetUsage returns usage transactions for an IAM user, filtered by tag "api-usage".
//
//	GET /v1/billing/usage?user=hanzo/alice&currency=usd
func GetUsage(c *gin.Context) {
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)
	db := datastore.New(ctx)

	user := strings.TrimSpace(c.Query("user"))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)

	transs := make([]*transaction.Transaction, 0)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("Test=", org.TestMode()).
		Filter("SourceKind=", "iam-user").
		Filter("SourceId=", user).
		Filter("Tags=", "api-usage")

	cur := currency.Type(strings.ToLower(c.DefaultQuery("currency", "")))
	if cur != "" {
		q = q.Filter("Currency=", cur)
	}

	if _, err := q.GetAll(&transs); err != nil {
		log.Error("Failed to query usage transactions: %v", err, c)
		http.Fail(c, 500, "failed to query usage", err)
		return
	}

	items := make([]gin.H, 0, len(transs))
	for _, t := range transs {
		items = append(items, gin.H{
			"transactionId": t.Id(),
			"amount":        t.Amount,
			"currency":      t.Currency,
			"notes":         t.Notes,
			"metadata":      t.Metadata,
			"createdAt":     t.CreatedAt,
		})
	}

	c.JSON(200, gin.H{
		"user":  user,
		"count": len(items),
		"usage": items,
	})
}

// roundMicrosToCents converts a micro-USD debit (1e6 = $1, so 1 cent = 10_000
// micros) to whole cents, rounding to the NEAREST cent (round-half-up). Chosen
// over floor: floor drops every sub-cent fraction and thus systematically
// undercharges, whereas round-to-nearest has expected undercharge 0 over many
// spends. Stateless (no shared remainder) so it is safe on the append-only,
// no-atomic-RMW commerce ledger. The exact micros are preserved in the
// transaction metadata for audit / future true-up.
func roundMicrosToCents(micros int64) int64 {
	if micros <= 0 {
		return 0
	}
	return (micros + 5000) / 10000
}

// RecordUsage records an API usage event as a Withdraw transaction.
//
//	POST /v1/billing/usage
//
// Creates a withdraw transaction deducting the cost from the user's balance.
func RecordUsage(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req usageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}

	// Lossless sub-cent handling. `amountMicros` (micro-USD, 1e6=$1) carries full
	// precision over the wire; `amount` (cents) is the back-compat fallback.
	effMicros := req.AmountMicros
	if effMicros <= 0 {
		effMicros = req.Amount * 10000 // 1 cent = 10_000 micro-USD
	}
	if effMicros <= 0 {
		// Genuinely zero-cost — acknowledge, no transaction.
		c.JSON(200, gin.H{"user": req.User, "amount": 0, "status": "skipped"})
		return
	}

	// Round to NEAREST cent (round-half-up), NOT floor. Floor systematically
	// undercharges every sub-cent spend; round-to-nearest has expected
	// undercharge 0 across many spends. A per-subject remainder accumulator would
	// be exact, but the commerce datastore has NO atomic RMW / CAS / transactions
	// (RunInTransaction is a stub; balance is append-only, summed from the
	// ledger), so a mutable remainder would be a racy money-state — unsafe on a
	// live ledger. Round-to-nearest is the stateless, race-free equivalent; the
	// EXACT micros are recorded in metadata below for audit / future true-up.
	amountCents := roundMicrosToCents(effMicros)
	if amountCents <= 0 {
		// Sub-half-cent spend rounds to $0.00 (statistically fair, E[loss]=0).
		// Acknowledge without debiting; echo the exact micros for audit.
		c.JSON(200, gin.H{"user": req.User, "amount": 0, "amountMicros": effMicros, "status": "rounded-to-zero"})
		return
	}

	// Idempotency guard (money-critical). A retry (client lost the response) or a
	// double-submit MUST create AT MOST ONE withdraw. Key on the caller's
	// X-Idempotency-Key when supplied, else the requestId the caller sends per
	// spend. The datastore is already org-namespaced, so the key is per-tenant.
	// No key => caller opted out of the guard (behavior unchanged).
	var idemRec *idempotencykey.IdempotencyKey
	idemKey := strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	if idemKey == "" {
		idemKey = strings.TrimSpace(req.RequestID)
	}
	if idemKey != "" {
		rec, replay, gerr := idempotencykey.Begin(db, "billing-usage", idemKey)
		if gerr != nil {
			// Guard store unavailable — log and proceed WITHOUT the replay guard
			// rather than drop a legitimate usage record (matches topup posture).
			log.Error("usage idempotency Begin failed (org=%s key=%s): %v", org.Name, idemKey, gerr, c)
		} else if replay {
			if rec.Status == idempotencykey.StatusCompleted && rec.Response != "" {
				// Retry of a completed debit — replay the stored body, no 2nd debit.
				c.Data(200, "application/json", []byte(rec.Response))
				return
			}
			// Concurrent in-flight debit for this key — never run a second one.
			http.Fail(c, 409, "usage recording already in progress", nil)
			return
		} else {
			idemRec = rec
		}
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	notes := fmt.Sprintf("API usage: %s (%d tokens)", req.Model, req.TotalTokens)

	trans := transaction.New(db)
	trans.Type = transaction.Withdraw
	trans.SourceId = req.User
	trans.SourceKind = "iam-user"
	trans.Currency = cur
	trans.Amount = currency.Cents(amountCents)
	trans.Notes = notes
	trans.Tags = "api-usage"
	// Scope attribution (issue #70): the indexed dimensions the per-scope spend
	// cap sums over. Normalized so the default project ("default") and "no
	// project" collapse to the same "" scope the org-wide cap counts.
	trans.Project = spendalert.NormalizeProject(req.Project)
	trans.Service = strings.TrimSpace(req.Service)
	// Attribute the debit to the BillingAccount funding this project (GCP-style):
	// resolved SERVER-SIDE from the org's ProjectBinding / default account, never a
	// client field. "" = the org-wide default pool (byte-preserving the pre-account
	// behavior). This is what makes an account's balance + period spend summable and
	// keeps a shared account's projects drawing one funding source.
	trans.AccountId = resolveAccountId(db, trans.Project)
	trans.Metadata = Map{
		"model":            req.Model,
		"provider":         req.Provider,
		"promptTokens":     req.PromptTokens,
		"completionTokens": req.CompletionTokens,
		"totalTokens":      req.TotalTokens,
		// Exact debit in micro-USD, before cent rounding — the audit trail that
		// makes any systematic rounding bias detectable and enables a future
		// true-up. sum(amountMicros) is the exact spend; sum(amount)*10000 is
		// what was charged; their delta is the (E=0) rounding drift.
		"amountMicros": effMicros,
		"requestId":    req.RequestID,
		"premium":      req.Premium,
		"stream":       req.Stream,
		"status":       req.Status,
		"clientIp":     req.ClientIP,
	}

	if org.TestMode() {
		trans.Test = true
	}

	// Create the transaction. For usage recording we do NOT enforce
	// balance checks — the API call already happened and must be recorded.
	// Balance gating happens at request time in Cloud-API.
	if err := trans.Create(); err != nil {
		log.Error("Failed to record usage transaction: %v", err, c)
		http.Fail(c, 500, "failed to record usage", err)
		return
	}

	// Also create a MeterEvent for backward compatibility with the new
	// meter-based billing system. Look for a meter with eventName "api-usage".
	go func() {
		rootKey := db.NewKey("synckey", "", 1, nil)
		meters := make([]*meter.Meter, 0, 1)
		q := meter.Query(db).Ancestor(rootKey).
			Filter("EventName=", "api-usage").
			Limit(1)
		if _, err := q.GetAll(&meters); err == nil && len(meters) > 0 {
			evt := meter.NewEvent(db)
			evt.MeterId = meters[0].Id()
			evt.UserId = req.User
			evt.Value = req.Amount
			evt.Timestamp = time.Now()
			evt.Idempotency = req.RequestID
			evt.Dimensions = Map{
				"model":    req.Model,
				"provider": req.Provider,
			}
			if err := evt.Create(); err != nil {
				log.Error("Failed to create backward-compat meter event: %v", err)
			}
		}
	}()

	// Track referral revenue share: if this user was referred, create an
	// affiliate Fee for the referrer's commission. Fire-and-forget — usage
	// recording must not fail because of referral tracking.
	go engine.TrackRevenueShare(db, req.User, currency.Cents(amountCents), cur, trans.Id(), org.TestMode())

	// Accrue the OSS-developer payout: attribute up to 25% of this charge
	// across the upstream OSS packages this org's deployment depends on (from
	// the SBOMs emitted on deploy) into the per-package accrual ledger.
	// Fire-and-forget, same posture as revenue share.
	go engine.AccrueOSSPayout(db, org.Name, req.User, currency.Cents(amountCents), cur, trans.Id(), !org.Live)

	resp := gin.H{
		"transactionId": trans.Id(),
		"user":          req.User,
		"amount":        amountCents,
		"amountMicros":  effMicros,
		"currency":      cur,
		"type":          "withdraw",
	}
	c.JSON(201, resp)

	// Seal the idempotency guard with the exact success body so a retry replays
	// it verbatim (no second withdraw, identical response).
	if idemRec != nil {
		if body, mErr := json.Marshal(resp); mErr == nil {
			_ = idempotencykey.Complete(idemRec, string(body))
		}
	}
}
