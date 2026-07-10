package paymentmethod

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/orm"
)

var kind = "paymentmethod"

// CardDetails holds card-specific information for a payment method.
type CardDetails struct {
	Brand    string `json:"brand"`
	Last4    string `json:"last4"`
	ExpMonth int    `json:"expMonth"`
	ExpYear  int    `json:"expYear"`
	Funding  string `json:"funding,omitempty"`
	Country  string `json:"country,omitempty"`
}

// BankAccountDetails holds US bank account information for a payment method.
type BankAccountDetails struct {
	BankName      string `json:"bankName,omitempty"`
	Last4         string `json:"last4"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	AccountType   string `json:"accountType,omitempty"`
}

// CryptoDetails holds a crypto wallet for a payment method. Field shape
// mirrors the billing client's `crypto` payload (network/walletAddress/label).
type CryptoDetails struct {
	Network       string `json:"network,omitempty"`
	WalletAddress string `json:"walletAddress"`
	Label         string `json:"label,omitempty"`
}

// WireDetails holds bank-wire information for a payment method. Field shape
// mirrors the billing client's `wire` payload.
type WireDetails struct {
	BankName      string `json:"bankName,omitempty"`
	AccountHolder string `json:"accountHolder,omitempty"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	AccountLast4  string `json:"accountLast4,omitempty"`
	Swift         string `json:"swift,omitempty"`
	Iban          string `json:"iban,omitempty"`
	Country       string `json:"country,omitempty"`
}

// PayPalDetails holds a PayPal account for a payment method.
type PayPalDetails struct {
	Email string `json:"email"`
}

// PaymentMethod represents a customer's payment instrument.

func init() { orm.Register[PaymentMethod]("paymentmethod") }

type PaymentMethod struct {
	mixin.Model[PaymentMethod]

	UserId         string                 `json:"userId,omitempty"`
	CustomerId     string                 `json:"customerId,omitempty"`
	Name           string                 `json:"name,omitempty"`
	Type           string                 `json:"type"`
	ProviderRef    string                 `json:"providerRef"`
	ProviderType   string                 `json:"providerType"`
	Card           *CardDetails           `json:"card,omitempty"`
	BankAccount    *BankAccountDetails    `json:"bankAccount,omitempty"`
	Crypto         *CryptoDetails         `json:"crypto,omitempty"`
	Wire           *WireDetails           `json:"wire,omitempty"`
	PayPal         *PayPalDetails         `json:"paypal,omitempty"`
	BillingAddress *types.Address         `json:"billingAddress,omitempty"`
	IsDefault      bool                   `json:"isDefault,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	Created        time.Time              `json:"created,omitempty"`
}

func (p *PaymentMethod) Defaults() {
	p.Parent = p.Datastore().NewKey("synckey", "", 1, nil)
	if p.Type == "" {
		p.Type = "card"
	}
}

func New(db *datastore.Datastore) *PaymentMethod {
	p := new(PaymentMethod)
	p.Init(db)
	p.Defaults()
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
