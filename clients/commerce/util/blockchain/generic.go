package blockchain

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
)

// PaymentFn is the per-chain payment dispatcher. EVM payment support ships in
// github.com/hanzoai/cloud/clients/commerce/luxfi/cevm and registers itself on
// import; consumers who don't need EVM never link geth.
type PaymentFn func(ctx context.Context, from wallet.Account, to string, amount, fee currency.Cents, password []byte) (string, error)

var paymentFns = map[blockchains.Type]PaymentFn{}

// RegisterPayment wires a payment function for a chain type. Later
// registrations override earlier ones; callers should register once at init
// time.
func RegisterPayment(typ blockchains.Type, fn PaymentFn) {
	paymentFns[typ] = fn
}

// ErrNoPayment is returned when MakePayment is called for a chain type whose
// payment function hasn't been registered (e.g. an EVM chain without the
// luxfi/cevm sub-module wired in).
var ErrNoPayment = errors.New("blockchain: no payment function registered for chain type")

func MakePayment(ctx context.Context, from wallet.Account, to string, amount, fee currency.Cents, password []byte) (string, error) {
	switch from.Type {
	case blockchains.BitcoinType, blockchains.BitcoinTestnetType:
		return MakeBitcoinPayment(ctx, from, to, amount, fee, password)
	}
	if fn, ok := paymentFns[from.Type]; ok {
		return fn(ctx, from, to, amount, fee, password)
	}
	return "", fmt.Errorf("%w: %v", ErrNoPayment, from.Type)
}
