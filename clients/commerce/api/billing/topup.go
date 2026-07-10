package billing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/kms"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// Sentinel errors so callers (Topup HTTP handler, auto-recharge cron) can map a
// charge outcome to the right HTTP status / log without re-deriving it.
var (
	errNoProcessor            = errors.New("no payment processor available")
	errChargedButCreditFailed = errors.New("charge succeeded but balance credit failed")
)

type topupRequest struct {
	UserID          string `json:"userId"`
	PaymentMethodID string `json:"paymentMethodId"`
	AmountCents     int64  `json:"amountCents"`
	Currency        string `json:"currency,omitempty"`
}

// chargeAndCredit charges a saved payment method via the org's KMS-hydrated
// processor registry and, on success, credits the user's prepaid balance with a
// Deposit transaction. The org MUST already be KMS-hydrated by the caller (so
// payment.ProcessorsForOrg sees real Square credentials). Returns the deposit
// transaction id and the new balance.
//
// This is the single charge primitive reused by both the on-session top-up
// (Topup) and the off-session auto-recharge cron. For a Square card-on-file the
// SourceID is the saved card id (pm.ProviderRef) and CustomerID must be the
// Square customer id — a card-on-file is only chargeable in its customer's
// context (fall back to the org slug for legacy methods saved before card-on-file).
func chargeAndCredit(c *gin.Context, org *organization.Organization, db *datastore.Datastore, pm *paymentmethod.PaymentMethod, amountCents int64, cur currency.Type, userId, description string) (string, currency.Cents, error) {
	squareCustomerID := pm.CustomerId
	if pm.Metadata != nil {
		if v, ok := pm.Metadata["squareCustomerId"].(string); ok && v != "" {
			squareCustomerID = v
		}
	}

	ctx := middleware.GetContext(c)
	chargeReq := processor.PaymentRequest{
		Token:       pm.ProviderRef,
		Amount:      currency.Cents(amountCents),
		Currency:    cur,
		CustomerID:  squareCustomerID,
		Description: description,
	}

	// The global registry holds empty singletons (Square is registered
	// unconfigured at init), so charges must go through the org-scoped registry.
	reg := payment.ProcessorsForOrg(org)
	proc, err := reg.SelectProcessor(ctx, chargeReq)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", errNoProcessor, err)
	}

	result, err := proc.Charge(ctx, chargeReq)
	if err != nil {
		return "", 0, fmt.Errorf("charge failed: %w", err)
	}
	if !result.Success {
		msg := result.ErrorMessage
		if msg == "" {
			msg = "charge declined"
		}
		return "", 0, errors.New(msg)
	}

	// Step 4: the card was charged (money in), so mint the credit ON CHAIN as
	// prepaid HUSD to the org's derived address instead of a DB deposit row —
	// idempotent by the processor reference so a retried charge credits once.
	if chainCreditEnabled() {
		rc, mErr := chainMintCredit(c, org, userId, amountCents, treasury.BucketPrepaid,
			fmt.Sprintf("topup via %s (ref: %s)", proc.Type(), result.ProcessorRef),
			"topup:"+result.ProcessorRef)
		if mErr != nil {
			log.Error("RECONCILE: charge succeeded (ref=%s) but chain mint failed for user %s: %v",
				result.ProcessorRef, userId, mErr, c)
			return "", 0, fmt.Errorf("%w: ref=%s: %v", errChargedButCreditFailed, result.ProcessorRef, mErr)
		}
		var balanceCents currency.Cents
		if datas, bErr := util.GetTransactionsByCurrency(org.Namespaced(c), userId, "iam-user", cur, org.TestMode()); bErr == nil {
			if data, ok := datas.Data[cur]; ok {
				balanceCents = data.Balance
			}
		}
		return rc.TxHash, balanceCents, nil
	}

	trans := transaction.New(db)
	trans.Type = transaction.Deposit
	trans.DestinationId = userId
	trans.DestinationKind = "iam-user"
	trans.Currency = cur
	trans.Amount = currency.Cents(amountCents)
	trans.Notes = fmt.Sprintf("Top-up via %s (ref: %s)", proc.Type(), result.ProcessorRef)
	trans.Tags = "topup"
	// Ledger test-ness MUST follow the charge environment (org.TestMode), not
	// org.Live alone — otherwise a Square sandbox charge could credit the live
	// (spendable) balance. test==credit-bucket==read-bucket==charge-env.
	test := org.TestMode()
	trans.Test = test
	// The card was charged (result.Success): a settled payment IS the mint
	// authority, so authorize THIS write at the ledger sink (money-in == credit).
	trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	if err := trans.Create(); err != nil {
		// Charge succeeded but credit failed — log with full context for manual reconciliation.
		log.Error("RECONCILE: charge succeeded (ref=%s) but deposit failed for user %s: %v",
			result.ProcessorRef, userId, err, c)
		return "", 0, fmt.Errorf("%w: ref=%s: %v", errChargedButCreditFailed, result.ProcessorRef, err)
	}

	// Read back the new balance so the caller doesn't need a separate request.
	var balanceCents currency.Cents
	if datas, err := util.GetTransactionsByCurrency(org.Namespaced(c), userId, "iam-user", cur, test); err == nil {
		if data, ok := datas.Data[cur]; ok {
			balanceCents = data.Balance
		}
	}
	return trans.Id(), balanceCents, nil
}

// Topup charges a saved payment method and credits the user's balance.
//
//	POST /v1/billing/topup
//
// Body: { userId, paymentMethodId, amountCents, currency? }
// Returns: { transactionId, balanceCents, status }
func Topup(c *gin.Context) {
	org := middleware.GetOrganization(c)

	// Hydrate payment credentials from KMS (same pattern as checkout/subscription).
	if v, ok := c.Get("kms"); ok {
		if kmsClient, ok := v.(*kms.CachedClient); ok {
			if err := kms.Hydrate(kmsClient, org); err != nil {
				log.Error("KMS hydration failed for org %q: %v", org.Name, err, c)
			}
		}
	}

	db := datastore.New(org.Namespaced(c))

	var req topupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonhttp.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.UserID == "" {
		jsonhttp.Fail(c, 400, "userId is required", nil)
		return
	}
	if req.PaymentMethodID == "" {
		jsonhttp.Fail(c, 400, "paymentMethodId is required", nil)
		return
	}
	if req.AmountCents <= 0 {
		jsonhttp.Fail(c, 400, "amountCents must be positive", nil)
		return
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	// Load the payment method.
	pm := paymentmethod.New(db)
	if err := pm.GetById(req.PaymentMethodID); err != nil {
		jsonhttp.Fail(c, 404, "payment method not found", err)
		return
	}

	desc := fmt.Sprintf("Top-up %d %s for user %s", req.AmountCents, cur, req.UserID)
	txID, balanceCents, err := chargeAndCredit(c, org, db, pm, req.AmountCents, cur, req.UserID, desc)
	if err != nil {
		switch {
		case errors.Is(err, errNoProcessor):
			log.Error("No processor available for topup: %v", err, c)
			jsonhttp.Fail(c, 422, "no payment processor available", err)
		case errors.Is(err, errChargedButCreditFailed):
			jsonhttp.Fail(c, 500, "charge succeeded but balance credit failed; contact support", err)
		default:
			log.Error("Charge failed for topup (user=%s pm=%s): %v", req.UserID, req.PaymentMethodID, err, c)
			jsonhttp.Fail(c, 402, err.Error(), nil)
		}
		return
	}

	c.JSON(200, gin.H{
		"transactionId": txID,
		"balanceCents":  balanceCents,
		"status":        "ok",
	})
}
