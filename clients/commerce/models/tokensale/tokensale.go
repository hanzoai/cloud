package tokensale

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	// "github.com/hanzoai/cloud/clients/commerce/models/payment"
	// "github.com/hanzoai/cloud/clients/commerce/models/types/pricing"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[TokenSale]("tokensale") }

type TokenSale struct {
	mixin.Model[TokenSale]

	// Auditor Wallet
	wallet.WalletHolder `json:"-"`

	Name        string `json:"name"`
	TotalTokens int    `json:"totalTokens"`

	// // Fee structure for this tokensale
	// Fees pricing.Fees `json:"fees" datastore:",noindex"`

	// // Partner fees (private, should be up to partner to disclose)
	// Partners []pricing.Partner `json:"-" datastore:",noindex"`

	// Slug string `json:"slug"`

	// SupportedPaymentTypes []payment.Type `json:"supportedPaymentTypes`

	// Passphrase for the wallet accounts the order controls, never send to the client
	WalletPassphrase string `json:"-"`
}

// func (ts TokenSale) Pricing() (*pricing.Fees, []pricing.Partner) {
// 	// Ensure our id is set on fees used
// 	fees := ts.Fees
// 	fees.Id = ts.Id()
// 	return &fees, ts.Partners
// }

func New(db *datastore.Datastore) *TokenSale {
	ts := new(TokenSale)
	ts.Init(db)
	return ts
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("tokensale")
}
