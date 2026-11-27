package finance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/mint"
)

// A hold is a payment AGREED before it is MADE: a job's buyer signs when the work
// begins, and the money moves when the work is released, days or weeks later. A
// check of the wallet at the start proves nothing at the end — the same balance
// can be promised to any number of jobs, or spent on anything else in between —
// so the amount is set aside when the payment is agreed.
//
// The money stays the payer's and stays in its books. It moves out of the wallet
// into an account of its own beside each bucket it came from — held:cash:<ref> for
// the cash part, held:wallet:<ref> for the credit part, cash first — so the wallet's balance — what
// the prepaid gate, a draw and the balance page read — no longer counts it, and
// nothing else can spend it. The payment is made from there: a draw under the
// same ref takes the held money first ([ledgerFinance.debit]), so it lands however
// the wallet stands by then. A payment that is never made is given up, and the
// money returns to the wallet it came from.
//
// A hold is its own kind, [KindHold], because it is neither money in nor money
// out: the platform owes the payer the same sum before and after it, so the books
// recognize nothing, the spend cap counts nothing, and the ledger page shows the
// money set aside and returned.

// Holder is the ledger that can set money aside for a payment. The in-process
// finance client is one; callers reach it by interface assertion, as they reach
// Draw.
type Holder interface {
	// Hold sets in.Amount aside from subject's wallet under in.Ref, refused with
	// [ErrShortWallet] when the wallet cannot cover it. A replay of the same ref
	// answers the first hold with posted false; a ref already holding another
	// amount is [ErrRefReused], and one returned, held first or not, is
	// [ErrReturned].
	Hold(ctx context.Context, in money.UsageInput) (entryID string, posted bool, err error)
	// Return puts whatever is still held under in.Ref back in the wallet, and
	// answers how much. Nothing, once the payment was made or the hold returned.
	// From the first return on, nothing is ever held under in.Ref.
	Return(ctx context.Context, in money.UsageInput) (money.Amount, error)
}

var _ Holder = (*ledgerFinance)(nil)

// ErrReturned is a hold asked for under a ref that was returned, whether it held
// first or its hold was still on its way. The payment it set money aside for was
// given up, and a payment agreed again is held under a ref of its own.
var ErrReturned = errors.New("was returned")

// heldAcct is the account a hold under ref keeps the money in, beside the bucket
// (a wallet's cash or its credit account) it came from.
func heldAcct(bucket, ref string) string { return "held:" + bucket + ":" + ref }

// returnRef is the ref a hold's return is posted under.
func returnRef(ref string) string { return "return:" + ref }

// Hold sets a payment's amount aside from the wallet until it is made or released.
func (f *ledgerFinance) Hold(ctx context.Context, in money.UsageInput) (string, bool, error) {
	if in.Amount.Sign() <= 0 {
		return "", false, fmt.Errorf("finance: hold amount must be positive, got %s", in.Amount)
	}
	if in.Ref == "" {
		return "", false, errors.New("finance: a hold names the payment it holds for: ref is required")
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", false, err
	}
	wallet, cash := walletAcct(in.Subject), cashAcct(in.Subject)
	var entryID string
	var posted bool
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		prior, ok, gerr := tx.EntryByRef(string(KindHold), wallet, in.Ref)
		if gerr != nil {
			return gerr
		}
		if ok && prior.Amount.Cmp(in.Amount) != 0 {
			return fmt.Errorf("finance: hold ref %q %w", in.Ref, ErrRefReused)
		}
		// A ref returned is refused whether or not it held first: a return that found
		// nothing held is recorded, so a hold still on its way when the payment was
		// given up lands here and moves nothing.
		if _, returned, rerr := tx.EntryByRef(string(KindHold), wallet, returnRef(in.Ref)); rerr != nil {
			return rerr
		} else if returned {
			return fmt.Errorf("finance: hold %q %w", in.Ref, ErrReturned)
		}
		if ok {
			entryID = prior.ID
			return nil // the same hold again: it stands, or it was paid
		}
		rawCash, rawCredit, berr := bucketsIn(tx, in.Subject)
		if berr != nil {
			return berr
		}
		if total := fundsOf(rawCash, rawCredit).Total(); total.Cmp(in.Amount) < 0 {
			return fmt.Errorf("finance: hold %s from %s: %w (%s of %s)", in.Ref, wallet, ErrShortWallet, total, in.Amount)
		}
		fromCash, fromCredit := draw(in.Amount, rawCash)
		var legs []ledger.Posting
		for _, m := range []struct {
			bucket string
			amount money.Amount
		}{{cash, fromCash}, {wallet, fromCredit}} {
			if m.amount.Sign() != 0 {
				legs = append(legs,
					ledger.Posting{Account: m.bucket, Amount: m.amount.Neg()},
					ledger.Posting{Account: heldAcct(m.bucket, in.Ref), Amount: m.amount})
			}
		}
		entryID, posted = mint.ID("hld"), true
		return tx.Insert(ledger.JournalEntry{
			ID: entryID, Kind: string(KindHold), Program: wallet, Ref: in.Ref, Memo: in.Model,
			Amount: in.Amount, CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		return "", false, err
	}
	if posted {
		if err := f.settle(in.Org, in.Test, "the hold"); err != nil {
			return "", false, err
		}
	}
	return entryID, posted, nil
}

// Return puts a hold's money back in the wallet it came from: a NEGATIVE hold,
// the mirror of the first, posted once per ref.
//
// A return is decided when it is asked. One that finds nothing held — never
// held, paid, or a hold still on its way from a caller that stopped waiting for
// it — is posted all the same, as a return of nothing, so a hold under the ref
// that lands after it is refused ([ErrReturned]) instead of setting money aside
// for a payment already given up, where nothing would ever give it back.
func (f *ledgerFinance) Return(ctx context.Context, in money.UsageInput) (money.Amount, error) {
	if in.Ref == "" {
		return money.Zero(), errors.New("finance: a return names the payment it held for: ref is required")
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return money.Zero(), err
	}
	wallet, cash := walletAcct(in.Subject), cashAcct(in.Subject)
	returned, posted, replayed, gave := money.Zero(), false, false, false
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		if prior, done, rerr := tx.EntryByRef(string(KindHold), wallet, returnRef(in.Ref)); rerr != nil || done {
			// Returned already: nothing is held under it any more. Whether the earlier call gave
			// money back is on its entry.
			replayed, gave = done, done && prior.Amount.Sign() < 0
			return rerr
		}
		var legs []ledger.Posting
		for _, bucket := range []string{cash, wallet} {
			held := heldAcct(bucket, in.Ref)
			bal, berr := tx.Balance(held)
			if berr != nil {
				return berr
			}
			if bal.Sign() <= 0 {
				continue
			}
			returned = returned.Add(bal)
			legs = append(legs,
				ledger.Posting{Account: held, Amount: bal.Neg()},
				ledger.Posting{Account: bucket, Amount: bal})
		}
		if len(legs) == 0 {
			// A return of nothing still names the wallet, so it reads in the wallet's
			// movements as the zero it is.
			legs = []ledger.Posting{{Account: heldAcct(wallet, in.Ref), Amount: money.Zero()}, {Account: wallet, Amount: money.Zero()}}
		}
		posted = true
		return tx.Insert(ledger.JournalEntry{
			ID: mint.ID("hld"), Kind: string(KindHold), Program: wallet, Ref: returnRef(in.Ref), Memo: in.Model,
			Amount: returned.Neg(), CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		return money.Zero(), err
	}
	// A return already made is settled and told again, as a refund's is: the call that made it
	// may have failed to settle, and its retry is the only one that gets this far.
	if posted || replayed {
		if err := f.settle(in.Org, in.Test, "the return"); err != nil {
			return money.Zero(), err
		}
		if returned.Sign() > 0 || gave {
			told(in.Org)
		}
	}
	return returned, nil
}
