// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/commerce/billing/creditledger"
)

// ledger implements commerce's creditledger.CreditLedger over cloud's native
// finance ledger — the SAME per-org account (finance.Current()) the AI spend-gate
// reads and the edge meter debits. Injected at mountCommerce (EmbedConfig.Ledger),
// it makes commerce's POST /v1/billing/credit mint into the ONE ledger: a granted
// credit is immediately visible to the gate (one ledger, no split). This is the
// cloud half of the one-ledger client — commerce defines the interface, cloud
// implements it once here, the compiler enforces the match.
//
// Fails closed when no finance ledger is co-resident; in the unified cloud binary
// finance is always published, so Get() != nil ⇒ credit routes here.
type ledger struct{}

// compile-time proof the adapter satisfies commerce's exported client.
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
//
// AND Test IS THE THIRD PART OF THAT ADDRESS. finance keeps sandbox money in a
// separate file per org, so which books to write is not a mode this adapter can leave
// unsaid — it was saying "live" for every credit that came through here, which put a
// sandbox tenant's grant in the books the gate spends real inference from. It is the
// input's, unchanged, because the caller is the one that knows whether the charge it
// is crediting was a sandbox charge.
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
		Test:     sandboxed(in.Test),
	})
	if err != nil {
		return "", 0, err
	}
	// Read back the account that was CREDITED, never the pool and never the other
	// books — reporting a pool balance after crediting a member, or a live balance
	// after crediting the sandbox, is how a caller concludes the grant vanished. The
	// read repeats the deposit's whole address, one value at a time.
	bal, berr := fin.Balance(ctx, in.Org, subject, cur, sandboxed(in.Test))
	if berr != nil {
		return id, 0, berr
	}
	return id, bal.Cents(), nil
}

// Balance returns the available balance in cents held at (org, subject) in currency,
// from the sandbox books when test — the same read the AI gate performs, so
// GET /v1/billing/balance and the gate agree.
//
// It takes the whole address for the reason Credit writes it: an empty subject is the
// org's pool, and a named one is the member's own wallet. Answering the pool for a
// member's read is answering about a different account, and it answers without
// erroring — which is the shape of a balance bug nobody notices.
func (ledger) Balance(ctx context.Context, org, subject, currency string, test bool) (int64, error) {
	fin := finance.Current()
	if fin == nil {
		return 0, fmt.Errorf("commerce balance: no finance ledger co-resident")
	}
	if currency == "" {
		currency = "usd"
	}
	if subject == "" {
		subject = org // pooled org: the slug IS the pool account the gate reads
	}
	bal, err := fin.Balance(ctx, org, subject, currency, sandboxed(test))
	if err != nil {
		return 0, err
	}
	return bal.Cents(), nil
}
