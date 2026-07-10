package processor

import (
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// ProcessorType identifies the payment processor
type ProcessorType string

const (
	Stripe           ProcessorType = "stripe"
	Square           ProcessorType = "square"
	PayPal           ProcessorType = "paypal"
	Adyen            ProcessorType = "adyen"
	Braintree        ProcessorType = "braintree"
	Recurly          ProcessorType = "recurly"
	LemonSqueezy     ProcessorType = "lemonsqueezy"
	BitPay           ProcessorType = "bitpay"
	CoinbaseCommerce ProcessorType = "coinbase_commerce"
	OpenNode         ProcessorType = "opennode"
	Bitcoin          ProcessorType = "bitcoin"
	Ethereum         ProcessorType = "ethereum"
	MPC              ProcessorType = "mpc"
	Wire             ProcessorType = "wire"
	SolanaPay        ProcessorType = "solanapay"
	Circle           ProcessorType = "circle"
	MoonPay          ProcessorType = "moonpay"
)

// PaymentRequest represents a payment to be processed
type PaymentRequest struct {
	Amount      currency.Cents         `json:"amount"`
	Currency    currency.Type          `json:"currency"`
	Description string                 `json:"description,omitempty"`
	CustomerID  string                 `json:"customerId,omitempty"`
	OrderID     string                 `json:"orderId,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`

	// Processor-specific options
	Options map[string]interface{} `json:"options,omitempty"`

	// For card payments
	Token string `json:"token,omitempty"`

	// For crypto payments
	Address string `json:"address,omitempty"`
	Chain   string `json:"chain,omitempty"`
}

// PaymentResult represents the outcome of a payment
type PaymentResult struct {
	Success       bool                   `json:"success"`
	TransactionID string                 `json:"transactionId"`
	ProcessorRef  string                 `json:"processorRef"`
	Fee           currency.Cents         `json:"fee"`
	Error         error                  `json:"-"`
	ErrorMessage  string                 `json:"error,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Status        string                 `json:"status"`
}

// RefundRequest represents a refund to be processed
type RefundRequest struct {
	TransactionID string         `json:"transactionId"`
	Amount        currency.Cents `json:"amount"`
	Reason        string         `json:"reason,omitempty"`
	// IdempotencyKey, when set, is forwarded to the gateway as ITS idempotency
	// key so a retried refund (same key) is de-duplicated at the processor — the
	// money move is idempotent regardless of pods/mutexes/in-flight guards. When
	// empty the processor falls back to a random key (legacy, non-idempotent).
	IdempotencyKey string                 `json:"idempotencyKey,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// RefundResult represents the outcome of a refund
type RefundResult struct {
	Success      bool   `json:"success"`
	RefundID     string `json:"refundId"`
	ProcessorRef string `json:"processorRef"`
	Error        error  `json:"-"`
	ErrorMessage string `json:"error,omitempty"`
}

// Transaction represents a stored transaction
type Transaction struct {
	ID           string                 `json:"id"`
	ProcessorRef string                 `json:"processorRef"`
	Type         string                 `json:"type"` // charge, refund, transfer
	Amount       currency.Cents         `json:"amount"`
	Currency     currency.Type          `json:"currency"`
	Status       string                 `json:"status"`
	Fee          currency.Cents         `json:"fee"`
	CustomerID   string                 `json:"customerId,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt    int64                  `json:"createdAt"`
	UpdatedAt    int64                  `json:"updatedAt"`
}

// Balance represents a wallet balance
type Balance struct {
	Available currency.Cents `json:"available"`
	Pending   currency.Cents `json:"pending"`
	Currency  currency.Type  `json:"currency"`
}

// SubscriptionRequest represents a subscription creation request
type SubscriptionRequest struct {
	CustomerID   string                 `json:"customerId"`
	PlanID       string                 `json:"planId"`
	Quantity     int                    `json:"quantity"`
	TrialDays    int                    `json:"trialDays,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	PaymentToken string                 `json:"paymentToken,omitempty"`
}

// Subscription represents an active subscription
type Subscription struct {
	ID                 string                 `json:"id"`
	CustomerID         string                 `json:"customerId"`
	PlanID             string                 `json:"planId"`
	Status             string                 `json:"status"` // active, canceled, past_due, trialing
	CurrentPeriodStart int64                  `json:"currentPeriodStart"`
	CurrentPeriodEnd   int64                  `json:"currentPeriodEnd"`
	CancelAtPeriodEnd  bool                   `json:"cancelAtPeriodEnd"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

// SubscriptionUpdate represents subscription modification
type SubscriptionUpdate struct {
	PlanID            string `json:"planId,omitempty"`
	Quantity          int    `json:"quantity,omitempty"`
	CancelAtPeriodEnd *bool  `json:"cancelAtPeriodEnd,omitempty"`
}

// WebhookEvent represents an incoming webhook from a processor
type WebhookEvent struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Processor ProcessorType          `json:"processor"`
	Data      map[string]interface{} `json:"data"`
	Timestamp int64                  `json:"timestamp"`
}

// CryptoCurrencies supported by MPC (extend beyond what's in currency.IsCrypto)
var CryptoCurrencies = map[currency.Type]bool{
	currency.BTC: true,
	currency.ETH: true,
	currency.XBT: true,
	"sol":        true, // Solana
	"usdc":       true, // USDC stablecoin
	"usdt":       true, // USDT stablecoin
	"matic":      true, // Polygon
	"avax":       true, // Avalanche
	"lux":        true, // Lux Network
}

// IsCryptoCurrency checks if a currency is a cryptocurrency
func IsCryptoCurrency(c currency.Type) bool {
	// First check the built-in crypto check
	if c.IsCrypto() {
		return true
	}
	// Then check our extended list
	return CryptoCurrencies[c]
}
