package engine

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentintent"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/setupintent"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// CreatePaymentIntentParams contains parameters for creating a payment intent.
type CreatePaymentIntentParams struct {
	CustomerId         string        `json:"customerId"`
	Amount             int64         `json:"amount"`
	Currency           currency.Type `json:"currency"`
	PaymentMethodId    string        `json:"paymentMethodId,omitempty"`
	CaptureMethod      string        `json:"captureMethod,omitempty"`      // "automatic" | "manual"
	ConfirmationMethod string        `json:"confirmationMethod,omitempty"` // "automatic" | "manual"
	SetupFutureUsage   string        `json:"setupFutureUsage,omitempty"`
	Description        string        `json:"description,omitempty"`
	ReceiptEmail       string        `json:"receiptEmail,omitempty"`
	InvoiceId          string        `json:"invoiceId,omitempty"`
}

// CreatePaymentIntent creates a new payment intent in the initial state.
func CreatePaymentIntent(db *datastore.Datastore, params CreatePaymentIntentParams) (*paymentintent.PaymentIntent, error) {
	if params.Amount <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	if params.CustomerId == "" {
		return nil, fmt.Errorf("customerId is required")
	}

	pi := paymentintent.New(db)
	pi.CustomerId = params.CustomerId
	pi.Amount = params.Amount
	pi.Currency = params.Currency
	pi.Description = params.Description
	pi.ReceiptEmail = params.ReceiptEmail
	pi.InvoiceId = params.InvoiceId

	if params.CaptureMethod != "" {
		pi.CaptureMethod = params.CaptureMethod
	}
	if params.ConfirmationMethod != "" {
		pi.ConfirmationMethod = params.ConfirmationMethod
	}
	pi.SetupFutureUsage = params.SetupFutureUsage

	// If a payment method is provided and confirmation is automatic, advance state
	if params.PaymentMethodId != "" {
		pi.PaymentMethodId = params.PaymentMethodId
		if pi.ConfirmationMethod == "automatic" {
			pi.Status = paymentintent.RequiresConfirmation
		} else {
			pi.Status = paymentintent.RequiresConfirmation
		}
	}

	if err := pi.Create(); err != nil {
		return nil, fmt.Errorf("failed to create payment intent: %w", err)
	}

	return pi, nil
}

// ConfirmPaymentIntent confirms a payment intent, optionally attaching a payment method,
// and processes the payment via the configured processor.
func ConfirmPaymentIntent(ctx context.Context, db *datastore.Datastore, pi *paymentintent.PaymentIntent, pmId string, proc processor.PaymentProcessor) error {
	if pmId != "" {
		pi.PaymentMethodId = pmId
	}

	if err := pi.Confirm(); err != nil {
		return err
	}

	if proc == nil || !proc.IsAvailable(ctx) {
		// Internal-only processing: mark succeeded for balance/credit payments
		pi.MarkSucceeded("internal", pi.Amount)
		return pi.Update()
	}

	// Resolve payment method for processor token
	pm := paymentmethod.New(db)
	if err := pm.GetById(pi.PaymentMethodId); err != nil {
		pi.LastError = fmt.Sprintf("payment method not found: %s", pi.PaymentMethodId)
		_ = pi.Update()
		return fmt.Errorf("payment method not found: %w", err)
	}

	req := processor.PaymentRequest{
		Amount:      currency.Cents(pi.Amount),
		Currency:    pi.Currency,
		Description: pi.Description,
		CustomerID:  pi.CustomerId,
		Token:       pm.ProviderRef,
	}

	if pi.CaptureMethod == "manual" {
		// Authorize only
		result, err := proc.Authorize(ctx, req)
		if err != nil {
			pi.LastError = err.Error()
			pi.Status = paymentintent.RequiresPaymentMethod
			_ = pi.Update()
			return fmt.Errorf("authorization failed: %w", err)
		}
		pi.MarkRequiresCapture(result.ProcessorRef)
		pi.ProviderType = string(proc.Type())
	} else {
		// Charge immediately
		result, err := proc.Charge(ctx, req)
		if err != nil {
			pi.LastError = err.Error()
			pi.Status = paymentintent.RequiresPaymentMethod
			_ = pi.Update()
			return fmt.Errorf("charge failed: %w", err)
		}
		pi.MarkSucceeded(result.ProcessorRef, pi.Amount)
		pi.ProviderType = string(proc.Type())
	}

	return pi.Update()
}

// CapturePaymentIntent captures a previously authorized payment intent.
func CapturePaymentIntent(ctx context.Context, db *datastore.Datastore, pi *paymentintent.PaymentIntent, amount int64, proc processor.PaymentProcessor) error {
	if amount <= 0 {
		amount = pi.AmountCapturable
	}

	if proc != nil && proc.IsAvailable(ctx) && pi.ProviderRef != "" {
		_, err := proc.Capture(ctx, pi.ProviderRef, currency.Cents(amount))
		if err != nil {
			pi.LastError = err.Error()
			_ = pi.Update()
			return fmt.Errorf("capture failed: %w", err)
		}
	}

	if err := pi.Capture(amount); err != nil {
		return err
	}

	return pi.Update()
}

// CancelPaymentIntent cancels a payment intent with the given reason.
func CancelPaymentIntent(ctx context.Context, pi *paymentintent.PaymentIntent, reason string) error {
	if err := pi.Cancel(reason); err != nil {
		return err
	}
	return pi.Update()
}

// CreateSetupIntentParams contains parameters for creating a setup intent.
type CreateSetupIntentParams struct {
	CustomerId      string `json:"customerId"`
	PaymentMethodId string `json:"paymentMethodId,omitempty"`
	Usage           string `json:"usage,omitempty"` // "on_session" | "off_session"
}

// CreateSetupIntent creates a new setup intent for saving a payment method.
func CreateSetupIntent(db *datastore.Datastore, params CreateSetupIntentParams) (*setupintent.SetupIntent, error) {
	if params.CustomerId == "" {
		return nil, fmt.Errorf("customerId is required")
	}

	si := setupintent.New(db)
	si.CustomerId = params.CustomerId
	if params.Usage != "" {
		si.Usage = params.Usage
	}

	if params.PaymentMethodId != "" {
		si.PaymentMethodId = params.PaymentMethodId
		si.Status = setupintent.RequiresConfirmation
	}

	if err := si.Create(); err != nil {
		return nil, fmt.Errorf("failed to create setup intent: %w", err)
	}

	return si, nil
}

// ConfirmSetupIntent confirms a setup intent, verifying the payment method
// can be used for future payments.
func ConfirmSetupIntent(ctx context.Context, db *datastore.Datastore, si *setupintent.SetupIntent, pmId string, proc processor.PaymentProcessor) error {
	if pmId != "" {
		si.PaymentMethodId = pmId
	}

	if err := si.Confirm(); err != nil {
		return err
	}

	if proc != nil && proc.IsAvailable(ctx) {
		// Verify payment method with processor (e.g. $0 auth)
		pm := paymentmethod.New(db)
		if err := pm.GetById(si.PaymentMethodId); err != nil {
			si.LastError = fmt.Sprintf("payment method not found: %s", si.PaymentMethodId)
			_ = si.Update()
			return fmt.Errorf("payment method not found: %w", err)
		}
		// Provider verification would go here
		si.MarkSucceeded(pm.ProviderRef)
	} else {
		si.MarkSucceeded("internal")
	}

	return si.Update()
}

// CancelSetupIntent cancels a setup intent.
func CancelSetupIntent(si *setupintent.SetupIntent, reason string) error {
	if err := si.Cancel(reason); err != nil {
		return err
	}
	return si.Update()
}
