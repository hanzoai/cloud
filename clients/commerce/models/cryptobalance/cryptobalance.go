package cryptobalance

import (
	"fmt"
	"math/big"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[CryptoBalance]("crypto-balance") }

// CryptoBalance tracks custodial crypto holdings for a customer.
type CryptoBalance struct {
	mixin.Model[CryptoBalance]

	// Customer who owns the balance
	CustomerId string `json:"customerId"`

	// Blockchain
	Chain string `json:"chain"` // ethereum, solana, base, etc.

	// Token identifier
	Token string `json:"token"` // usdc, usdt, eth, sol, etc.

	// Balance in smallest unit (wei, lamports, etc.) as string for precision
	Balance string `json:"balance" orm:"default:0"`

	// Custody address for this customer+chain+token
	Address string `json:"address"`

	// Reserved amount (pending withdrawal or payment)
	Reserved string `json:"reserved,omitempty" orm:"default:0"`

	Metadata Map `json:"metadata,omitempty" orm:"default:{}"`
}

// Available returns the available balance (total - reserved).
func (cb *CryptoBalance) Available() (*big.Int, error) {
	total := new(big.Int)
	if _, ok := total.SetString(cb.Balance, 10); !ok {
		return nil, fmt.Errorf("invalid balance: %s", cb.Balance)
	}

	reserved := new(big.Int)
	if cb.Reserved != "" {
		if _, ok := reserved.SetString(cb.Reserved, 10); !ok {
			return nil, fmt.Errorf("invalid reserved: %s", cb.Reserved)
		}
	}

	return new(big.Int).Sub(total, reserved), nil
}

// Credit adds to the balance.
func (cb *CryptoBalance) Credit(amount string) error {
	total := new(big.Int)
	if _, ok := total.SetString(cb.Balance, 10); !ok {
		total.SetInt64(0)
	}

	add := new(big.Int)
	if _, ok := add.SetString(amount, 10); !ok {
		return fmt.Errorf("invalid credit amount: %s", amount)
	}

	if add.Sign() <= 0 {
		return fmt.Errorf("credit amount must be positive")
	}

	cb.Balance = new(big.Int).Add(total, add).String()
	return nil
}

// Debit subtracts from the balance.
func (cb *CryptoBalance) Debit(amount string) error {
	available, err := cb.Available()
	if err != nil {
		return err
	}

	sub := new(big.Int)
	if _, ok := sub.SetString(amount, 10); !ok {
		return fmt.Errorf("invalid debit amount: %s", amount)
	}

	if sub.Sign() <= 0 {
		return fmt.Errorf("debit amount must be positive")
	}

	if sub.Cmp(available) > 0 {
		return fmt.Errorf("insufficient balance: available %s, debit %s", available.String(), amount)
	}

	total := new(big.Int)
	total.SetString(cb.Balance, 10)
	cb.Balance = new(big.Int).Sub(total, sub).String()
	return nil
}

// Reserve places a hold on funds.
func (cb *CryptoBalance) Reserve(amount string) error {
	available, err := cb.Available()
	if err != nil {
		return err
	}

	res := new(big.Int)
	if _, ok := res.SetString(amount, 10); !ok {
		return fmt.Errorf("invalid reserve amount: %s", amount)
	}

	if res.Cmp(available) > 0 {
		return fmt.Errorf("insufficient available balance for reservation")
	}

	reserved := new(big.Int)
	if cb.Reserved != "" {
		reserved.SetString(cb.Reserved, 10)
	}

	cb.Reserved = new(big.Int).Add(reserved, res).String()
	return nil
}

// Release removes a hold on funds.
func (cb *CryptoBalance) Release(amount string) error {
	reserved := new(big.Int)
	if cb.Reserved != "" {
		reserved.SetString(cb.Reserved, 10)
	}

	rel := new(big.Int)
	if _, ok := rel.SetString(amount, 10); !ok {
		return fmt.Errorf("invalid release amount: %s", amount)
	}

	if rel.Cmp(reserved) > 0 {
		return fmt.Errorf("release exceeds reserved amount")
	}

	cb.Reserved = new(big.Int).Sub(reserved, rel).String()
	return nil
}

// IsZero returns true if the balance is zero or empty.
func (cb *CryptoBalance) IsZero() bool {
	if cb.Balance == "" || cb.Balance == "0" {
		return true
	}
	b := new(big.Int)
	b.SetString(cb.Balance, 10)
	return b.Sign() == 0
}

func New(db *datastore.Datastore) *CryptoBalance {
	cb := new(CryptoBalance)
	cb.Init(db)
	cb.Parent = db.NewKey("synckey", "", 1, nil)
	return cb
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("crypto-balance")
}
