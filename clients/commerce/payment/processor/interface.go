package processor

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// PaymentProcessor is the interface all payment processors must implement
type PaymentProcessor interface {
	// Type returns the processor type
	Type() ProcessorType

	// Charge processes a payment
	Charge(ctx context.Context, req PaymentRequest) (*PaymentResult, error)

	// Authorize authorizes a payment without capturing
	Authorize(ctx context.Context, req PaymentRequest) (*PaymentResult, error)

	// Capture captures a previously authorized payment
	Capture(ctx context.Context, transactionID string, amount currency.Cents) (*PaymentResult, error)

	// Refund processes a refund
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)

	// GetTransaction retrieves transaction details
	GetTransaction(ctx context.Context, txID string) (*Transaction, error)

	// ValidateWebhook validates an incoming webhook
	ValidateWebhook(ctx context.Context, payload []byte, signature string) (*WebhookEvent, error)

	// SupportedCurrencies returns currencies this processor supports
	SupportedCurrencies() []currency.Type

	// IsAvailable checks if processor is configured and available
	IsAvailable(ctx context.Context) bool
}

// CryptoProcessor extends PaymentProcessor with crypto-specific methods
type CryptoProcessor interface {
	PaymentProcessor

	// GenerateAddress creates a new payment address for a customer
	GenerateAddress(ctx context.Context, customerID string, chain string) (string, error)

	// GetBalance returns the balance for an address
	GetBalance(ctx context.Context, address string, chain string) (*Balance, error)

	// EstimateFee estimates transaction fees for a payment
	EstimateFee(ctx context.Context, req PaymentRequest) (currency.Cents, error)

	// SupportedChains returns the list of supported blockchain networks
	SupportedChains() []string
}

// SubscriptionProcessor extends PaymentProcessor with subscription methods
type SubscriptionProcessor interface {
	PaymentProcessor

	// CreateSubscription creates a recurring subscription
	CreateSubscription(ctx context.Context, req SubscriptionRequest) (*Subscription, error)

	// GetSubscription retrieves subscription details
	GetSubscription(ctx context.Context, subscriptionID string) (*Subscription, error)

	// CancelSubscription cancels a subscription
	CancelSubscription(ctx context.Context, subscriptionID string, immediately bool) error

	// UpdateSubscription modifies a subscription
	UpdateSubscription(ctx context.Context, subscriptionID string, req SubscriptionUpdate) (*Subscription, error)

	// ListSubscriptions lists subscriptions for a customer
	ListSubscriptions(ctx context.Context, customerID string) ([]*Subscription, error)
}

// CustomerProcessor extends PaymentProcessor with customer management
type CustomerProcessor interface {
	PaymentProcessor

	// CreateCustomer creates a customer in the processor
	CreateCustomer(ctx context.Context, email, name string, metadata map[string]interface{}) (string, error)

	// GetCustomer retrieves customer details
	GetCustomer(ctx context.Context, customerID string) (map[string]interface{}, error)

	// UpdateCustomer updates customer details
	UpdateCustomer(ctx context.Context, customerID string, updates map[string]interface{}) error

	// DeleteCustomer removes a customer
	DeleteCustomer(ctx context.Context, customerID string) error

	// AddPaymentMethod adds a payment method to a customer
	AddPaymentMethod(ctx context.Context, customerID, token string) (string, error)

	// RemovePaymentMethod removes a payment method
	RemovePaymentMethod(ctx context.Context, customerID, paymentMethodID string) error
}
