// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/commerce/billing/creditledger"
)

// ledger implements commerce's creditledger.CreditLedger over cloud's native
// finance ledger — the SAME per-org account (finance.Current()) the AI spend-gate
// reads and the edge meter debits, and the account a plan paid from the balance
// is drawn from. Injected at mountCommerce (EmbedConfig.Ledger),
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
		tag = "grant:admin"
	}
	subject := in.Subject
	if subject == "" {
		subject = in.Org // pooled org: the slug IS the pool account the gate reads
	}
	id, err := fin.Deposit(ctx, money.DepositInput{
		Org:      in.Org,
		Subject:  subject,
		Amount:   money.FromCents(in.AmountCents),
		Currency: cur,
		Notes:    in.Reason,
		Tags:     tag,
		Ref:      in.IdempotencyKey,
		Test:     sandboxed(in.Test),
		// A settled payment is cash, keyed on its processor reference; everything else
		// through here is credit the platform minted (creditledger.CreditInput.Cash).
		Cash: in.Cash,
	})
	if err != nil {
		return "", 0, err
	}
	if !sandboxed(in.Test) {
		kind := "grant"
		var cashMinor, creditMinor int64
		if in.Cash {
			kind = "deposit"
			cashMinor = in.AmountCents
		} else {
			creditMinor = in.AmountCents
		}
		mirrorCommerceLedgerFact(datastore.CommerceLedgerFact{
			EntryID:     deterministicLedgerEntryID(kind, in.Org, id, in.IdempotencyKey),
			Org:         in.Org,
			Product:     "commerce",
			Kind:        kind,
			Sign:        1,
			AmountMinor: in.AmountCents,
			CashMinor:   cashMinor,
			CreditMinor: creditMinor,
			Currency:    cur,
		})
		mirrorOrgBook(in.Org)
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

// Refund gives back the payment Debit drew under in.Ref at the same address, exactly —
// finance mirrors the payment's own legs, so the cash and the credit it took each come
// back to where they were.
func (ledger) Refund(ctx context.Context, in creditledger.RefundInput) (string, error) {
	fin := finance.Current()
	if fin == nil {
		return "", fmt.Errorf("commerce refund: no finance ledger co-resident")
	}
	r, ok := fin.(interface {
		Refund(context.Context, money.UsageInput) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("commerce refund: this finance ledger cannot refund a payment")
	}
	subject := in.Subject
	if subject == "" {
		subject = in.Org
	}
	id, err := r.Refund(ctx, money.UsageInput{Org: in.Org, Subject: subject, Ref: in.Ref, Test: sandboxed(in.Test)})
	if err == nil && !sandboxed(in.Test) {
		mirrorCommerceLedgerFact(datastore.CommerceLedgerFact{
			EntryID: deterministicLedgerEntryID("refund", in.Org, id, in.Ref),
			Org:     in.Org,
			Product: "commerce",
			Kind:    "refund",
			Sign:    -1,
		})
		mirrorOrgBook(in.Org)
	}
	return id, err
}

// Clawback takes back money a processor returned to the payer, from the wallet the
// payment credited (finance.Clawback).
func (ledger) Clawback(ctx context.Context, in creditledger.ClawbackInput) (string, error) {
	fin := finance.Current()
	if fin == nil {
		return "", fmt.Errorf("commerce clawback: no finance ledger co-resident")
	}
	c, ok := fin.(interface {
		Clawback(context.Context, finance.ClawbackInput) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("commerce clawback: this finance ledger cannot take money back")
	}
	subject := in.Subject
	if subject == "" {
		subject = in.Org
	}
	id, err := c.Clawback(ctx, finance.ClawbackInput{
		Org: in.Org, Subject: subject, Payment: in.Payment, Ref: in.Ref,
		Amount: money.FromCents(in.AmountCents), Reason: in.Reason, Test: sandboxed(in.Test),
	})
	if err == nil && !sandboxed(in.Test) {
		mirrorCommerceLedgerFact(datastore.CommerceLedgerFact{
			EntryID:     deterministicLedgerEntryID("chargeback", in.Org, id, in.Ref),
			Org:         in.Org,
			Product:     "commerce",
			Kind:        "chargeback",
			Sign:        -1,
			AmountMinor: in.AmountCents,
			CashMinor:   in.AmountCents,
		})
		mirrorOrgBook(in.Org)
	}
	return id, err
}

// Restore gives back a clawback the processor reversed — a dispute won
// (finance.Restore).
func (ledger) Restore(ctx context.Context, in creditledger.ClawbackInput) (string, error) {
	fin := finance.Current()
	if fin == nil {
		return "", fmt.Errorf("commerce restore: no finance ledger co-resident")
	}
	r, ok := fin.(interface {
		Restore(context.Context, finance.ClawbackInput) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("commerce restore: this finance ledger cannot restore a clawback")
	}
	subject := in.Subject
	if subject == "" {
		subject = in.Org
	}
	id, err := r.Restore(ctx, finance.ClawbackInput{
		Org: in.Org, Subject: subject, Payment: in.Payment, Ref: in.Ref, Reason: in.Reason, Test: sandboxed(in.Test),
	})
	if err == nil && !sandboxed(in.Test) {
		mirrorCommerceLedgerFact(datastore.CommerceLedgerFact{
			EntryID:     deterministicLedgerEntryID("adjustment", in.Org, id, in.Ref),
			Org:         in.Org,
			Product:     "commerce",
			Kind:        "adjustment",
			Sign:        1,
			AmountMinor: in.AmountCents,
			CashMinor:   in.AmountCents,
		})
		mirrorOrgBook(in.Org)
	}
	return id, err
}

// Debit draws a payment from the ADDRESS the input names, all or nothing: finance
// checks the wallet and posts the debit in one transaction, so a wallet that holds
// less than the amount is refused with nothing moved ([creditledger.ErrShort]) and
// a concurrent spend cannot take it below zero between the two. Idempotent on Ref
// within the wallet: a replay answers the first entry and moves nothing. It is posted as a payment, not usage, so the AI spend cap
// never counts a plan against the org's budget.
func (ledger) Debit(ctx context.Context, in creditledger.DebitInput) (string, int64, error) {
	fin := finance.Current()
	if fin == nil {
		return "", 0, fmt.Errorf("commerce debit: no finance ledger co-resident")
	}
	d, ok := fin.(finance.Drawer)
	if !ok {
		return "", 0, fmt.Errorf("commerce debit: this finance ledger cannot draw a payment")
	}
	cur := in.Currency
	if cur == "" {
		cur = "usd"
	}
	subject := in.Subject
	if subject == "" {
		subject = in.Org // pooled org: the slug IS the pool account the gate reads
	}
	id, _, err := d.Draw(ctx, money.UsageInput{
		Org:      in.Org,
		Subject:  subject,
		Amount:   money.FromCents(in.AmountCents),
		Currency: cur,
		Model:    in.Reason,
		Service:  "billing",
		Ref:      in.Ref,
		Test:     sandboxed(in.Test),
	})
	if errors.Is(err, finance.ErrShortWallet) {
		return "", 0, fmt.Errorf("%w: %v", creditledger.ErrShort, err)
	}
	if err != nil {
		return "", 0, err
	}
	if !sandboxed(in.Test) {
		mirrorCommerceLedgerFact(datastore.CommerceLedgerFact{
			EntryID:     deterministicLedgerEntryID("usage", in.Org, id, in.Ref),
			Org:         in.Org,
			Product:     "billing",
			Kind:        "usage",
			Sign:        1,
			AmountMinor: in.AmountCents,
			Currency:    cur,
		})
		mirrorOrgBook(in.Org)
	}
	// The money has moved. The balance after it is the receipt's, not the draw's,
	// so a failed read is a zero balance beside a posted entry, never an error the
	// caller would read as a draw that did not happen.
	bal, berr := fin.Balance(ctx, in.Org, subject, cur, sandboxed(in.Test))
	if berr != nil {
		return id, 0, nil
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

// settleCryptoDepositCash carries a confirmed native on-chain deposit into the
// finance Cash book via the single sanctioned settlement adapter (ledger.go).
func settleCryptoDepositCash(ctx context.Context, org, subject, reason, idempotencyKey string, amountCents int64, test bool) error {
	if amountCents <= 0 {
		return nil
	}
	led := creditledger.Get()
	if led == nil {
		return nil
	}
	_, _, err := led.Credit(ctx, creditledger.CreditInput{
		Org:            org,
		Subject:        subject,
		Currency:       "usd",
		Reason:         reason,
		Tag:            "crypto-deposit",
		IdempotencyKey: idempotencyKey,
		AmountCents:    amountCents,
		Test:           test,
		Cash:           true,
	})
	return err
}
