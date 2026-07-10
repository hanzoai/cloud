package wallet

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains/blockaddress"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Wallet]("wallet") }

type Wallet struct {
	mixin.Model[Wallet]

	Accounts []Account `json:"accounts,omitempty"`
}

// Create a new Account, saves if wallet is created. Key generation is
// dispatched to a per-chain registry populated by importing the relevant
// blockchain package (e.g. thirdparty/bitcoin for BTC, luxfi/cevm
// for EVM). CreateAccount returns ErrNoKeyGen for chain types whose
// generator hasn't been registered.
func (w *Wallet) CreateAccount(name string, typ blockchains.Type, withPassword []byte) (*Account, error) {
	_, found := w.GetAccountByName(name)

	if found {
		return nil, ErrorNameCollision
	}

	switch typ {
	case blockchains.EthereumType, blockchains.EthereumRopstenType,
		blockchains.BitcoinType, blockchains.BitcoinTestnetType:
		// handled via registry
	default:
		return nil, ErrorInvalidTypeSpecified
	}

	priv, pub, add, err := generateKey(typ)
	if err != nil {
		return nil, err
	}

	a := &Account{
		Name:       name,
		PrivateKey: priv,
		PublicKey:  pub,
		Address:    add,
		Type:       typ,
		CreatedAt:  time.Now(),
	}

	if err := a.Encrypt(withPassword); err != nil {
		return nil, err
	}

	log.Debug("Attempting append to w.Accounts. Current state: %v", w.Accounts)
	log.Debug("Appending a. Current state: %v", a)
	w.Accounts = append(w.Accounts, *a)

	// Create a blockaddress so we track this in the readers
	ba := blockaddress.New(w.Datastore())
	ba.Address = a.Address
	ba.Type = typ
	ba.WalletId = w.Id()

	ba.WalletNamespace = nscontext.GetNamespace(w.Datastore().Context)
	if err := ba.Create(); err != nil {
		log.Error("Could not create BlockAddress: %v", err, w.Context())
	}

	// Only save if the wallet is created
	// Otherwise let the user manage that
	if w.Created() {
		if err := w.Update(); err != nil {
			return nil, err
		}
	}

	return &w.Accounts[len(w.Accounts)-1], nil
}

func (w *Wallet) GetAccountByName(name string) (*Account, bool) {
	// Find The Test Account
	for i, a := range w.Accounts {
		if a.Name != name {
			continue
		}

		// Lazy Migration to Remove TestNetAddress as needed
		if a.Type == blockchains.BitcoinTestnetType && a.AddressBackup == "" && a.TestNetAddress != "" {
			log.Warn("Migrating TestNetAddress '%s', overwriting '%s'", a.TestNetAddress, a.Address, w.Context())
			w.Accounts[i].AddressBackup = a.Address
			w.Accounts[i].Address = a.TestNetAddress
			w.Accounts[i].TestNetAddress = ""
			w.MustUpdate()

			a = w.Accounts[i]
		}

		return &a, true
	}

	return nil, false
}

func (w *Wallet) Defaults() {
	w.Accounts = make([]Account, 0)
}

func New(db *datastore.Datastore) *Wallet {
	w := new(Wallet)
	w.Init(db)
	w.Defaults()
	return w
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("wallet")
}
