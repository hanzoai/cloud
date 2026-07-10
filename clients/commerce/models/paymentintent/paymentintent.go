package paymentintent

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"
)

var kind = "payment-intent"

// Status represents the state of a PaymentIntent.
type Status string

const (
	RequiresPaymentMethod Status = "requires_payment_method"
	RequiresConfirmation  Status = "requires_confirmation"
	RequiresAction        Status = "requires_action"
	Processing            Status = "processing"
	RequiresCapture       Status = "requires_capture"
	Succeeded             Status = "succeeded"
	Canceled              Status = "canceled"
)

// PaymentIntent represents a payment flow from creation to completion.

func init() { orm.Register[PaymentIntent]("payment-intent") }

type PaymentIntent struct {
	mixin.Model[PaymentIntent]

	CustomerId         string                 `json:"customerId,omitempty"`
	Amount             int64                  `json:"amount"`
	Currency           currency.Type          `json:"currency"`
	Status             Status                 `json:"status"`
	PaymentMethodId    string                 `json:"paymentMethodId,omitempty"`
	CaptureMethod      string                 `json:"captureMethod,omitempty"`
	ConfirmationMethod string                 `json:"confirmationMethod,omitempty"`
	AmountCapturable   int64                  `json:"amountCapturable"`
	AmountReceived     int64                  `json:"amountReceived"`
	Description        string                 `json:"description,omitempty"`
	ReceiptEmail       string                 `json:"receiptEmail,omitempty"`
	ProviderRef        string                 `json:"providerRef"`
	ProviderType       string                 `json:"providerType"`
	CanceledAt         time.Time              `json:"canceledAt,omitempty"`
	CancellationReason string                 `json:"cancellationReason,omitempty"`
	LastError          string                 `json:"lastError,omitempty"`
	ClientSecret       string                 `json:"clientSecret,omitempty"`
	InvoiceId          string                 `json:"invoiceId,omitempty"`
	SetupFutureUsage   string                 `json:"setupFutureUsage,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

// Confirm transitions the intent from RequiresConfirmation to Processing.
func (pi *PaymentIntent) Confirm() error {
	if pi.Status != RequiresConfirmation && pi.Status != RequiresPaymentMethod {
		return fmt.Errorf("cannot confirm payment intent in status %s", pi.Status)
	}
	if pi.PaymentMethodId == "" {
		return fmt.Errorf("payment method is required to confirm")
	}
	pi.Status = Processing
	return nil
}

// MarkSucceeded marks the intent as succeeded after a charge completes.
func (pi *PaymentIntent) MarkSucceeded(providerRef string, amountReceived int64) {
	pi.Status = Succeeded
	pi.ProviderRef = providerRef
	pi.AmountReceived = amountReceived
	pi.AmountCapturable = 0
}

// MarkRequiresCapture marks the intent as authorized but not yet captured.
func (pi *PaymentIntent) MarkRequiresCapture(providerRef string) {
	pi.Status = RequiresCapture
	pi.ProviderRef = providerRef
	pi.AmountCapturable = pi.Amount
}

// Capture transitions from RequiresCapture to Succeeded.
func (pi *PaymentIntent) Capture(amount int64) error {
	if pi.Status != RequiresCapture {
		return fmt.Errorf("cannot capture payment intent in status %s", pi.Status)
	}
	if amount > pi.AmountCapturable {
		return fmt.Errorf("capture amount %d exceeds capturable %d", amount, pi.AmountCapturable)
	}
	pi.AmountReceived = amount
	pi.AmountCapturable = 0
	pi.Status = Succeeded
	return nil
}

// Cancel transitions the intent to Canceled.
func (pi *PaymentIntent) Cancel(reason string) error {
	if pi.Status == Succeeded || pi.Status == Canceled {
		return fmt.Errorf("cannot cancel payment intent in status %s", pi.Status)
	}
	pi.Status = Canceled
	pi.CancellationReason = reason
	pi.CanceledAt = time.Now()
	return nil
}

func (pi *PaymentIntent) Defaults() {
	pi.Parent = pi.Datastore().NewKey("synckey", "", 1, nil)
	if pi.Status == "" {
		pi.Status = RequiresPaymentMethod
	}
	if pi.Currency == "" {
		pi.Currency = "usd"
	}
	if pi.CaptureMethod == "" {
		pi.CaptureMethod = "automatic"
	}
	if pi.ConfirmationMethod == "" {
		pi.ConfirmationMethod = "automatic"
	}
}

func New(db *datastore.Datastore) *PaymentIntent {
	pi := new(PaymentIntent)
	pi.Init(db)
	pi.Defaults()
	return pi
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
