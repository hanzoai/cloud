package billing

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// GPU billing — the ONE server-enforced money rule that makes GPUs draw ONLY
// prepaid real money, NEVER credit grants.
//
// The rule cannot be expressed as "spend order" alone: a GPU debit is recorded
// as a Withdraw tagged "gpu", which the bucket projection (billing/bucket)
// attributes to PREPAID directly (never credits-first). Two server-side gates
// make that fail-closed:
//
//   - a card on file is REQUIRED (a GPU is real money, so a chargeable card must
//     back it), and
//   - the charge is refused unless PREPAID (real money) alone can cover it —
//     the check reads split.PrepaidAvailable, never the combined balance, so a
//     customer sitting on $100 of free credit and $0 prepaid is refused a GPU,
//     exactly as intended.
//
// The cloud GPU launch gate calls GET /gpu-eligibility before provisioning and
// POST /gpu-charge to debit; neither path ever calls BurnCredits.

// gpuEligibilityRequest is read from query params on the read gate.
type gpuChargeRequest struct {
	User        string `json:"user"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency,omitempty"`
	Notes       string `json:"notes,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	// Tag lets the caller record a GPU sub-kind (e.g. "gpu-h100"); it is FORCED
	// into the gpu bucket (a non-gpu tag is rejected) so this endpoint can never
	// be used to record a credit-eligible withdrawal.
	Tag string `json:"tag,omitempty"`
}

// GPUChargeEligibility is the read-only launch gate: may a GPU of this size be
// launched/charged from prepaid right now?
//
//	GET /v1/billing/gpu-eligibility?user=<subj>&amountCents=<n>&minPrepaidCents=<m>
//
// Returns 200 with {eligible,reason,...} in ALL cases (the caller decides how to
// render) — it never 402s, so the launch UI can show the exact remedy (add a
// card / add prepaid). amountCents is the immediate charge; minPrepaidCents is
// the 24h-minimum prepaid the GPU policy requires before launch (the gate needs
// prepaidAvailable >= max(amountCents, minPrepaidCents)).
func GPUChargeEligibility(c *gin.Context) {
	org := middleware.GetOrganization(c)
	if org == nil {
		http.Fail(c, 401, "missing organization", nil)
		return
	}
	ctx := org.Namespaced(c)

	user := strings.ToLower(strings.TrimSpace(c.Query("user")))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}
	cur := currency.Type(strings.ToLower(c.DefaultQuery("currency", "usd")))
	amount := currency.Cents(queryInt64(c, "amountCents", 0))
	minPrepaid := currency.Cents(queryInt64(c, "minPrepaidCents", 0))

	split, err := bucketedSplit(ctx, user, cur, org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to query balance", err)
		return
	}
	card := getCardOnFile(datastore.New(ctx), user)

	need := amount
	if minPrepaid > need {
		need = minPrepaid
	}

	eligible := true
	reason := "ok"
	switch {
	case !card.OnFile:
		eligible, reason = false, "card_required"
	case split.PrepaidAvailable < need:
		eligible, reason = false, "insufficient_prepaid"
	}

	c.JSON(200, gin.H{
		"user":             user,
		"currency":         cur,
		"eligible":         eligible,
		"reason":           reason,
		"prepaidAvailable": int64(split.PrepaidAvailable),
		"prepaidBalance":   int64(split.PrepaidBalance),
		"creditsRemaining": int64(split.CreditsRemaining), // shown, but NOT usable for GPU
		"requiredCents":    int64(need),
		"cardOnFile":       card.OnFile,
	})
}

// ChargeGPU debits a GPU charge from PREPAID real money only. It is the ONLY
// commerce write that records a "gpu"-tagged withdrawal, and it is fail-closed:
//
//		POST /v1/billing/gpu-charge  { user, amountCents, currency?, requestId?, tag? }
//
//	  - 402 {code: card_required}         — no chargeable card on file.
//	  - 402 {code: insufficient_prepaid}  — prepaid real money can't cover it
//	    (credits are NEVER consulted or consumed).
//	  - 201 {transactionId, prepaidBalance, ...} on success.
//
// Admin/service token (mounted on the admin group) — called by the cloud GPU
// launch/meter path, never the browser.
func ChargeGPU(c *gin.Context) {
	org := middleware.GetOrganization(c)
	if org == nil {
		http.Fail(c, 401, "missing organization", nil)
		return
	}
	ctx := org.Namespaced(c)
	db := datastore.New(ctx)

	var req gpuChargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}
	req.User = strings.ToLower(strings.TrimSpace(req.User))
	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}
	if req.AmountCents <= 0 {
		http.Fail(c, 400, "amountCents must be positive", nil)
		return
	}
	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	// Gate 1: a chargeable card MUST be on file — a GPU is real money.
	card := getCardOnFile(db, req.User)
	if !card.OnFile {
		c.JSON(402, gin.H{"error": gin.H{
			"code":    "card_required",
			"message": "Add a card on file before launching a GPU",
		}})
		return
	}

	// Gate 2: PREPAID real money alone must cover the charge — credits are never
	// consulted, so a GPU can never draw on a grant.
	split, err := bucketedSplit(ctx, req.User, cur, org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to query balance", err)
		return
	}
	if split.PrepaidAvailable < currency.Cents(req.AmountCents) {
		c.JSON(402, gin.H{"error": gin.H{
			"code":             "insufficient_prepaid",
			"message":          "Add prepaid funds (GPUs are billed from prepaid real money, not credits)",
			"prepaidAvailable": int64(split.PrepaidAvailable),
			"requiredCents":    req.AmountCents,
		}})
		return
	}

	// Record the debit as a "gpu"-tagged Withdraw. bucket.Compute attributes a
	// gpu withdrawal to prepaid, never credits — so this can never consume a grant.
	tag := gpuTag(req.Tag)
	notes := req.Notes
	if notes == "" {
		notes = fmt.Sprintf("GPU charge: %d cents %s", req.AmountCents, cur)
	}
	trans := transaction.New(db)
	trans.Type = transaction.Withdraw
	trans.SourceId = req.User
	trans.SourceKind = "iam-user"
	trans.Currency = cur
	trans.Amount = currency.Cents(req.AmountCents)
	trans.Notes = notes
	trans.Tags = tag
	trans.Metadata = Map{
		"bucket":    "prepaid",
		"kind":      "gpu",
		"requestId": req.RequestID,
	}
	if org.TestMode() {
		trans.Test = true
	}
	if err := trans.Create(); err != nil {
		log.Error("Failed to record GPU charge for %s: %v", req.User, err, c)
		http.Fail(c, 500, "failed to record GPU charge", err)
		return
	}

	// New prepaid balance after the debit.
	after, _ := bucketedSplit(ctx, req.User, cur, org.TestMode())
	c.JSON(201, gin.H{
		"transactionId":  trans.Id(),
		"user":           req.User,
		"amount":         req.AmountCents,
		"currency":       cur,
		"tags":           tag,
		"prepaidBalance": int64(after.PrepaidBalance),
		"status":         "ok",
	})
}

// gpuTag forces a caller-supplied tag into the gpu bucket. A blank or non-gpu tag
// becomes "gpu"; an explicit "gpu-*"/"gpu:*"/"gpu" is preserved. This guarantees
// the recorded withdrawal is always gpu-classified (prepaid-only), so this
// endpoint can never mint a credit-eligible withdrawal.
func gpuTag(tag string) string {
	t := strings.TrimSpace(strings.ToLower(tag))
	if t == "gpu" || strings.HasPrefix(t, "gpu-") || strings.HasPrefix(t, "gpu:") {
		return t
	}
	if t == "" {
		return "gpu"
	}
	return "gpu-" + t
}

func queryInt64(c *gin.Context, key string, def int64) int64 {
	s := strings.TrimSpace(c.Query(key))
	if s == "" {
		return def
	}
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		return def
	}
	return n
}
