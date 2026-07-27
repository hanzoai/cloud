// Copyright © 2026 Hanzo AI. MIT License.

package apps

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/commerce/billing/creditledger"
)

// ledger implements commerce's creditledger.CreditLedger over cloud's native
// finance ledger — the SAME per-org account (finance.Current()) the AI spend-gate
// reads and the edge meter debits. Injected at mountCommerce (EmbedConfig.Ledger),
// it makes commerce's POST /v1/billing/credit mint into the ONE ledger: a granted
// credit is immediately visible to the gate (one ledger, no split). This is the
// cloud half of the one-ledger seam — commerce defines the interface, cloud
// implements it once here, the compiler enforces the match.
//
// Fails closed when no finance ledger is co-resident; in the unified cloud binary
// finance is always published, so Get() != nil ⇒ credit routes here.
type ledger struct{}

// compile-time proof the adapter satisfies commerce's exported seam.
var _ creditledger.CreditLedger = ledger{}

// Credit posts a balanced deposit (funding:platform → wallet) to the ADDRESS the
// input names — (Org, Subject), where an empty Subject is the org POOL — and returns
// the ledger entry id + that account's new available balance in cents. Idempotent on
// IdempotencyKey: finance dedups on Ref, so the same key credits AT MOST once.
//
// Subject exists because the pool is not always the account the gate reads. In the
// shared signup org each member spends from their own wallet, so a pool-only credit
// funds a balance nobody can spend while the member it was meant for is refused at
// $0. The caller resolves the address with the SAME rule the gate resolves the payer
// with (account.Payer, via principal), so a credit and the spend it funds land on one
// wallet by construction. An org whose members share one balance names no subject and
// is byte-for-byte unchanged.
func (ledger) Credit(ctx context.Context, in creditledger.CreditInput) (string, int64, error) {
	fin := finance.Current()
	if fin == nil {
		return "", 0, fmt.Errorf("commerce credit: no finance ledger co-resident")
	}
	cur := in.Currency
	if cur == "" {
		cur = "usd"
	}
	tag := in.Tag
	if tag == "" {
		tag = "grant:admin" // non-cash grant bucket (finance is a single wallet; Tags is a memo)
	}
	subject := in.Subject
	if subject == "" {
		subject = in.Org // pooled org: the slug IS the pool account the gate reads
	}
	id, err := fin.Deposit(ctx, types.DepositInput{
		Org:      in.Org,
		Subject:  subject,
		Amount:   money.FromCents(in.AmountCents),
		Currency: cur,
		Notes:    in.Reason,
		Tags:     tag,
		Ref:      in.IdempotencyKey,
	})
	if err != nil {
		return "", 0, err
	}
	// Read back the account that was CREDITED, never the pool — reporting a pool
	// balance after crediting a member is how a caller concludes the grant vanished.
	bal, berr := fin.Balance(ctx, in.Org, subject, cur, false)
	if berr != nil {
		return id, 0, berr
	}
	return id, bal.Cents(), nil
}

// Balance returns the org pool's available balance in cents for currency — the
// same read the AI gate performs, so GET /v1/billing/balance and the gate agree.
func (ledger) Balance(ctx context.Context, org, currency string) (int64, error) {
	fin := finance.Current()
	if fin == nil {
		return 0, fmt.Errorf("commerce balance: no finance ledger co-resident")
	}
	if currency == "" {
		currency = "usd"
	}
	bal, err := fin.Balance(ctx, org, org, currency, false)
	if err != nil {
		return 0, err
	}
	return bal.Cents(), nil
}
