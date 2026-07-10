package square

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// Charge processes a tokenized payment (Autocomplete=true on the Square
// side, i.e. capture-at-charge). The req.Token field must be a Square
// Web Payments SDK payment source id — card nonce, Apple Pay token, or
// Google Pay token. Square treats them identically once tokenized.
//
// BD calls this from /v1/bd/deposits/{id}/confirm with the token the
// fund SPA produced via the Web Payments SDK. The returned
// TransactionID is the Square payment_id, which BD pins to the deposit
// row for idempotent webhook reconciliation.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	return p.inner.Charge(ctx, req)
}

// Authorize performs an auth-only (Autocomplete=false) tokenized payment.
// Pairs with Capture to release/settle later. Used for deposit flows
// where we want to confirm the card is good before committing ledger
// state — today BD goes directly via Charge for simplicity.
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	return p.inner.Authorize(ctx, req)
}

// Capture completes a previously authorized payment by its Square
// payment_id.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	return p.inner.Capture(ctx, transactionID, amount)
}

// GetTransaction retrieves a payment by Square payment_id. Used by the
// reconciler to confirm settled state before crediting DEF ledger when
// the webhook is delayed.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	return p.inner.GetTransaction(ctx, txID)
}

// CancelAuthorization voids an uncaptured auth. Used by BD when the
// downstream routing step fails and we must release the hold rather
// than let it fall off after Square's 7-day auth window.
func (p *Provider) CancelAuthorization(ctx context.Context, paymentID string) error {
	if err := p.checkAvailable(); err != nil {
		return err
	}
	return p.inner.CancelAuthorization(ctx, paymentID)
}

// checkAvailable returns an error if the processor is not configured.
// Keeps the error shape consistent with braintree/ so BD's error-handling
// branches identically for both.
func (p *Provider) checkAvailable() error {
	if p.inner == nil || p.config.AccessToken == "" || p.config.LocationID == "" {
		return processor.NewPaymentError(processor.Square, "NOT_CONFIGURED",
			fmt.Sprintf("square processor not configured"), processor.ErrProcessorNotAvailable)
	}
	return nil
}
