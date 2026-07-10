package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
)

// CollectionResult describes the outcome of a payment collection attempt.
type CollectionResult struct {
	Success        bool   `json:"success"`
	CreditUsed     int64  `json:"creditUsed"`
	BalanceUsed    int64  `json:"balanceUsed"`
	ProviderUsed   int64  `json:"providerUsed"`
	ProviderRef    string `json:"providerRef,omitempty"`
	AmountCharged  int64  `json:"amountCharged"`
	Error          string `json:"error,omitempty"`
}

// CreditBurner is the function signature for burning credits.
// This matches the existing BurnCredits function in api/billing/credit_grants.go.
type CreditBurner func(db *datastore.Datastore, userId string, amount int64, meterId string) (int64, error)

// CollectInvoice attempts to collect payment for an invoice using the
// provider-agnostic waterfall: credits -> balance -> external provider.
// The burnCredits parameter is injected to avoid circular imports.
func CollectInvoice(ctx context.Context, db *datastore.Datastore, inv *billinginvoice.BillingInvoice, burnCredits CreditBurner) (*CollectionResult, error) {
	if inv.Status != billinginvoice.Open {
		return nil, fmt.Errorf("invoice must be open to collect, current status: %s", inv.Status)
	}

	result := &CollectionResult{}
	remaining := inv.AmountDue

	// Step 1: Burn credits
	if remaining > 0 && burnCredits != nil {
		afterCredits, err := burnCredits(db, inv.UserId, remaining, "")
		if err != nil {
			// Non-fatal: continue without credits
			_ = err
		} else {
			result.CreditUsed = remaining - afterCredits
			remaining = afterCredits
		}
	}

	// Step 2: Deduct from transaction balance
	if remaining > 0 {
		balanceUsed, err := deductFromBalance(ctx, db, inv.UserId, inv.Currency, remaining)
		if err != nil {
			// Non-fatal: continue to external provider
			_ = err
		} else {
			result.BalanceUsed = balanceUsed
			remaining -= balanceUsed
		}
	}

	// Step 3: External provider (Square, etc.)
	// For now, balance-only. External provider integration can be added
	// by passing a ProviderCharger callback similar to CreditBurner.
	if remaining > 0 {
		// If there's still remaining amount after credits + balance,
		// mark as partial failure. External provider charging will be
		// added in Phase 5 (webhook integration).
		result.Success = false
		result.Error = fmt.Sprintf("insufficient funds: %d cents remaining after credits and balance", remaining)
	} else {
		result.Success = true
	}

	result.AmountCharged = inv.AmountDue - remaining

	// Update invoice
	inv.CreditApplied = result.CreditUsed
	inv.AmountPaid = result.AmountCharged
	inv.AttemptCount++
	inv.LastAttemptAt = time.Now()

	if result.Success {
		method := "balance"
		if result.CreditUsed > 0 && result.BalanceUsed == 0 {
			method = "credit"
		}
		if err := inv.MarkPaid(method, result.ProviderRef); err != nil {
			return result, err
		}
	}

	return result, nil
}

// deductFromBalance withdraws the specified amount from the user's
// transaction balance if sufficient funds exist.
func deductFromBalance(ctx context.Context, db *datastore.Datastore, userId string, cur currency.Type, amount int64) (int64, error) {
	if cur == "" {
		cur = "usd"
	}

	// Check available balance
	data, err := txutil.GetTransactionsByCurrency(ctx, userId, "user", cur, false)
	if err != nil {
		return 0, err
	}

	balData, ok := data.Data[cur]
	if !ok || int64(balData.Balance) < amount {
		// Use whatever is available
		available := int64(0)
		if ok {
			available = int64(balData.Balance)
		}
		if available <= 0 {
			return 0, nil
		}
		amount = available
	}

	// Create withdrawal transaction
	tx := transaction.New(db)
	tx.Parent = db.NewKey("synckey", "", 1, nil)
	tx.Type = transaction.Withdraw
	tx.Amount = currency.Cents(amount)
	tx.Currency = cur
	tx.SourceKind = "user"
	tx.SourceId = userId
	tx.DestinationKind = "billing"
	tx.DestinationId = "invoice"
	tx.Notes = "Invoice payment"

	if err := tx.Create(); err != nil {
		return 0, fmt.Errorf("failed to create withdrawal: %w", err)
	}

	return amount, nil
}
