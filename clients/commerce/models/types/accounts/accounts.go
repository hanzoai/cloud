package accounts

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

type Type string

const (
	AffirmType       Type = "affirm"
	AuthorizeNetType Type = "authorizenet"
	BalanceType      Type = "balance"
	EthereumType     Type = "ethereum"
	BitcoinType      Type = "bitcoin"
	NullType         Type = "null"
	PayPalType       Type = "paypal"
	SquareType       Type = "square"
	StripeType       Type = "stripe"
	PlaidType        Type = "plaid"
)

type AffirmAccount struct {
	CaptureId     string `json:"captureId,omitempty"`
	TransactionId string `json:"transactionId,omitempty"`
	CheckoutToken string `json:"checkoutToken,omitempty"`
}

type AuthorizeNetAccount struct {
	AuthCode       string `json:"authCode,omitempty"`
	AvsResultCode  string `json:"avsResultCode,omitempty"`
	CvvResultCode  string `json:"cvvResultCode,omitempty"`
	CavvResultCode string `json:"cavvResultCode,omitempty"`
	TransId        string `json:"transId,omitempty"`
	RefTransId     string `json:"refTransId,omitempty"`
	TransHash      string `json:"transHash,omitempty"`
	TestRequest    string `json:"testRequest,omitempty"`
	AccountNumber  string `json:"accountNumber,omitempty"`
	AccountType    string `json:"accountType,omitempty"`
	Messages       string `json:"message,omitempty"`
	ErrorMessages  string `json:"errorMessage,omitempty"`
}

type PayPalAccount struct {
	Email       string `json:"email,omitempty"`
	SellerEmail string `json:"sellerEmail,omitempty"`
	RedirectUrl string `json:"redirectUrl,omitempty"`
	Ipn         string `json:"ipn,omitempty"`

	PayKey         string `json:"payKey,omitempty"`
	PreapprovalKey string `json:"preapprovalKey,omitempty"`

	// Preapproval expiration date (Unix timestamp in milliseconds).
	Ending int `json:"ending,omitempty"`

	// Preapproval expiration date (ISO 8601 timestamp).
	EndingDate string `json:"endingDate,omitempty"`
}

type StripeAccount struct {
	// Very important to never store these!
	Name   string `json:"name,omitempty" datastore:"-"`
	Number string `json:"number,omitempty" datastore:"-"`
	CVC    string `json:"cvc,omitempty" datastore:"-"`

	BalanceTransactionId string `json:"balanceTransactionId,omitempty"`
	CardId               string `json:"cardId,omitempty"`
	ChargeId             string `json:"chargeId,omitempty"`
	CustomerId           string `json:"customerId,omitempty"`

	Fingerprint string `json:"fingerprint,omitempty"`
	Funding     string `json:"funding,omitempty"`
	Brand       string `json:"brand,omitempty"`
	LastFour    string `json:"lastFour,omitempty"`
	Month       int    `json:"month,string,omitempty"`
	Year        int    `json:"year,string,omitempty"`
	Country     string `json:"country,omitempty"`

	CVCCheck string `json:"cvcCheck,omitempty"`
}

func (sa StripeAccount) CardMatches(acct Account) bool {
	log.Debug("Checking for match")
	log.Debug("Old card: %v", sa)
	log.Debug("New card: %v", acct)

	if sa.Month != acct.Month {
		return false
	}
	if sa.Year != acct.Year {
		return false
	}
	if len(sa.LastFour) == 4 && sa.LastFour != acct.LastFour {
		return false
	}
	return true
}

// type PlaidAccount struct {
// 	StripeAccount
// }

type SquareAccount struct {
	PaymentId  string `json:"paymentId,omitempty"`
	CustomerId string `json:"customerId,omitempty"`
	LocationId string `json:"locationId,omitempty"`
	CardBrand  string `json:"cardBrand,omitempty"`
	LastFour   string `json:"lastFour,omitempty"`
	ReceiptURL string `json:"receiptUrl,omitempty"`
}

type BitcoinTransaction struct {
	BitcoinTransactionTxId string           `json:"bitcoinTransactionTxId,omitempty"`
	BitcoinChainType       blockchains.Type `json:"bitcoinChainType,omitempty"`
	BitcoinAmount          currency.Cents   `json:"bitcoinAmount,omitempty"`

	// Denotes whether or not a successful transfer occurred
	BitcoinTransferred bool `json:"bitcoinTransfered"`

	BitcoinFinalTransactionTxId string         `json:"finalbitcoinTransactionTxId,omitempty"`
	BitcoinFinalTransactionCost currency.Cents `json:"finalbitcoinTransactionCost,omitempty"`
	BitcoinFinalAddress         string         `json:"finalbitcoinAddress,omitempty"`
	BitcoinFinalAmount          currency.Cents `json:"finalbitcoinAmount,omitempty"`
}

type EthereumTransaction struct {
	EthereumTransactionHash string                `json:"ethereumTransactionHash,omitempty"`
	EthereumChainType       blockchains.Type      `json:"ethereumChainType,omitempty"`
	EthereumAmount          blockchains.BigNumber `json:"ethereumAmount,omitempty"`
	EthereumFromAddress     string                `json:"ethereumFromAddress,omitempty"`
	EthereumToAddress       string                `json:"ethereumToAddress,omitempty"`

	// Denotes whether or not a successful transfer occurred
	EthereumTransferred bool `json:"ethereumTransfered"`

	EthereumFinalTransactionHash string                `json:"finalEthereumTransactionHash,omitempty"`
	EthereumFinalTransactionCost blockchains.BigNumber `json:"finalEthereumTransactionCost,omitempty"`
	EthereumFinalAddress         string                `json:"finalEthereumAddress,omitempty"`
	EthereumFinalAmount          blockchains.BigNumber `json:"finalEthereumAmount,omitempty"`
}

// Sort of a union type of all possible payment accounts, used everywhere for convenience
type Account struct {
	Type Type `json:"type"`

	// Deprecated
	AffirmAccount
	PayPalAccount
	StripeAccount
	// PlaidAccount
	BitcoinTransaction
	EthereumTransaction
	AuthorizeNetAccount

	Affirm AffirmAccount `json:"affirm,omitempty"`
	PayPal PayPalAccount `json:"paypal,omitempty"`
	Square SquareAccount `json:"square,omitempty"`
	Stripe StripeAccount `json:"stripe,omitempty"`
	// Plaid        PlaidAccount        `json:"plaid,omitempty"`
	Bitcoin      BitcoinTransaction  `json:"bitcoin,omitempty"`
	Ethereum     EthereumTransaction `json:"ethereum,omitempty"`
	AuthorizeNet AuthorizeNetAccount `json:"authorizenet,omitempty"`

	Error string `json:"error,omitempty"`
}
