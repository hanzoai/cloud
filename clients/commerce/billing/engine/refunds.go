package engine

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/credit"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentintent"
	"github.com/hanzoai/cloud/clients/commerce/models/refund"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// CreateRefundParams holds the parameters for creating a refund.
type CreateRefundParams struct {
	PaymentIntentId string
	InvoiceId       string
	Amount          int64  // 0 = full refund
	Reason          string // "duplicate" | "fraudulent" | "requested_by_customer"
}

// CreateRefund creates a full or partial refund for a payment intent or invoice.
// If Amount is 0, the full amount is refunded.
func CreateRefund(ctx context.Context, db *datastore.Datastore, params CreateRefundParams, proc processor.PaymentProcessor) (*refund.Refund, error) {
	var amount int64
	var cur currency.Type

	if params.PaymentIntentId != "" {
		pi := paymentintent.New(db)
		if err := pi.GetById(params.PaymentIntentId); err != nil {
			return nil, fmt.Errorf("payment intent not found: %w", err)
		}
		if pi.Status != paymentintent.Succeeded {
			return nil, fmt.Errorf("can only refund succeeded payment intents, current: %s", pi.Status)
		}
		if params.Amount > 0 {
			amount = params.Amount
		} else {
			amount = pi.AmountReceived
		}
		if amount > pi.AmountReceived {
			return nil, fmt.Errorf("refund amount %d exceeds received amount %d", amount, pi.AmountReceived)
		}
		cur = pi.Currency
	} else if params.InvoiceId != "" {
		inv := billinginvoice.New(db)
		if err := inv.GetById(params.InvoiceId); err != nil {
			return nil, fmt.Errorf("invoice not found: %w", err)
		}
		if inv.Status != billinginvoice.Paid {
			return nil, fmt.Errorf("can only refund paid invoices, current: %s", inv.Status)
		}
		if params.Amount > 0 {
			amount = params.Amount
		} else {
			amount = inv.AmountPaid
		}
		if amount > inv.AmountPaid {
			return nil, fmt.Errorf("refund amount %d exceeds paid amount %d", amount, inv.AmountPaid)
		}
		cur = inv.Currency
	} else {
		return nil, fmt.Errorf("either paymentIntentId or invoiceId is required")
	}

	r := refund.New(db)
	r.PaymentIntentId = params.PaymentIntentId
	r.InvoiceId = params.InvoiceId
	r.Amount = amount
	r.Currency = cur
	r.Reason = params.Reason

	// If external processor, attempt refund
	if proc != nil && params.PaymentIntentId != "" {
		pi := paymentintent.New(db)
		_ = pi.GetById(params.PaymentIntentId)
		if pi.ProviderRef != "" {
			result, err := proc.Refund(ctx, processor.RefundRequest{
				TransactionID: pi.ProviderRef,
				Amount:        currency.Cents(amount),
				Reason:        params.Reason,
			})
			if err != nil {
				_ = r.MarkFailed(err.Error())
				if createErr := r.Create(); createErr != nil {
					return nil, createErr
				}
				return r, err
			}
			r.ProviderRef = result.RefundID
		}
	}

	// Mark as succeeded for internal refunds
	_ = r.MarkSucceeded()

	if err := r.Create(); err != nil {
		return nil, fmt.Errorf("failed to create refund: %w", err)
	}

	return r, nil
}

// CreateCreditNoteParams holds the parameters for creating a credit note.
type CreateCreditNoteParams struct {
	InvoiceId       string
	CustomerId      string
	Amount          int64
	Reason          string
	LineItems       []credit.CreditNoteLineItem
	OutOfBandAmount int64
	Memo            string
}

// CreateCreditNote creates a credit note against an invoice.
func CreateCreditNote(db *datastore.Datastore, params CreateCreditNoteParams) (*credit.CreditNote, error) {
	if params.InvoiceId == "" {
		return nil, fmt.Errorf("invoiceId is required")
	}

	inv := billinginvoice.New(db)
	if err := inv.GetById(params.InvoiceId); err != nil {
		return nil, fmt.Errorf("invoice not found: %w", err)
	}

	cn := credit.New(db)
	cn.InvoiceId = params.InvoiceId
	cn.CustomerId = params.CustomerId
	if cn.CustomerId == "" {
		cn.CustomerId = inv.UserId
	}
	cn.Currency = inv.Currency
	cn.Reason = params.Reason
	cn.Memo = params.Memo
	cn.LineItems = params.LineItems
	cn.OutOfBandAmount = params.OutOfBandAmount

	// Calculate total amount from line items
	if params.Amount > 0 {
		cn.Amount = params.Amount
	} else {
		var total int64
		for _, li := range params.LineItems {
			total += li.Amount
		}
		total += params.OutOfBandAmount
		cn.Amount = total
	}

	// Auto-number (simple increment based on query count)
	rootKey := db.NewKey("synckey", "", 1, nil)
	existing := make([]*credit.CreditNote, 0)
	if _, err := credit.Query(db).Ancestor(rootKey).GetAll(&existing); err == nil {
		cn.SetNumber(len(existing) + 1)
	} else {
		cn.SetNumber(1)
	}

	if err := cn.Create(); err != nil {
		return nil, fmt.Errorf("failed to create credit note: %w", err)
	}

	return cn, nil
}
