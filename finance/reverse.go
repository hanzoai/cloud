package finance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/mint"
)

// A reversal undoes a deposit by posting its mirror: the same legs with their signs
// turned, as a NEGATIVE deposit. That is the ledger's one spelling for money leaving
// a wallet the way it came in — the books (apps/books ruleFor) book a negative deposit
// as the exact reversal of the deposit it undoes, and no revenue is recognized — so a
// reversal is never a usage debit, never counts toward spend, and never needs a third
// kind. The original entry stays; the pair is the record.
//
// Exactly once per original: the reversal's ref is "reverse:<id>", unique within the
// deposit kind, so a second reversal of the same entry finds the first and is refused
// with it ([ErrReversed]).

// ReverseInput names the deposit to undo and why.
type ReverseInput struct {
	Org    string
	Entry  string // the deposit's entry id, e.g. "dep_…"
	Reason string
	Test   bool
}

// Reversal is what a reversal wrote: its own entry, the entry it undoes, the wallet
// the money left, and how much.
type Reversal struct {
	ID     string
	Of     string
	Wallet string
	Amount money.Amount
}

// Reverser is the ledger that can undo a deposit. The in-process finance client is
// one; callers reach it by interface assertion, as they reach RecordUsageOnce.
type Reverser interface {
	Reverse(ctx context.Context, in ReverseInput) (Reversal, error)
}

var _ Reverser = (*ledgerFinance)(nil)

var (
	// ErrNoEntry is an entry id this org's books do not hold.
	ErrNoEntry = errors.New("no such entry")
	// ErrNotDeposit is an entry that is not a positive deposit: usage is not undone
	// by this, and a reversal is not reversed.
	ErrNotDeposit = errors.New("is not a deposit")
	// ErrReversed is a deposit that already has its reversal; the Reversal returned
	// beside it names that one.
	ErrReversed = errors.New("is already reversed")
	// ErrShortWallet is a wallet holding less than an all-or-nothing debit needs:
	// the reversal of a deposit part of which was spent, or a draw it cannot
	// cover. Neither ever takes a wallet below zero.
	ErrShortWallet = errors.New("wallet holds less than the amount")
)

// reversalRef is the ref a reversal of entry id is posted under.
func reversalRef(id string) string { return "reverse:" + id }

// Reverse posts the mirror of the deposit in.Entry and returns it.
func (f *ledgerFinance) Reverse(ctx context.Context, in ReverseInput) (Reversal, error) {
	id := strings.TrimSpace(in.Entry)
	if id == "" {
		return Reversal{}, fmt.Errorf("finance: reverse: %w", ErrNoEntry)
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return Reversal{}, err
	}
	var out Reversal
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		orig, ok, gerr := tx.EntryByID(id)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return fmt.Errorf("finance: entry %q %w", id, ErrNoEntry)
		}
		if ParseKind(orig.Kind) != KindDeposit || orig.Amount.Sign() <= 0 {
			return fmt.Errorf("finance: entry %q %w", id, ErrNotDeposit)
		}
		wallet := credited(orig)
		out = Reversal{Of: orig.ID, Wallet: wallet, Amount: orig.Amount}
		prior, done, rerr := tx.EntryByRef(string(KindDeposit), "", reversalRef(orig.ID))
		if rerr != nil {
			return rerr
		}
		if done {
			out.ID = prior.ID
			return fmt.Errorf("finance: entry %q %w by %s", id, ErrReversed, prior.ID)
		}
		legs, lerr := reversal(tx, orig, wallet)
		if lerr != nil {
			return fmt.Errorf("finance: entry %q: %w", id, lerr)
		}
		memo := "Reversal of " + orig.ID
		if r := strings.TrimSpace(in.Reason); r != "" {
			memo += ": " + r
		}
		out.ID = mint.ID("dep")
		return tx.Insert(ledger.JournalEntry{
			ID:        out.ID,
			Kind:      string(KindDeposit),
			Ref:       reversalRef(orig.ID),
			Memo:      memo,
			Amount:    orig.Amount.Neg(),
			CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		if errors.Is(err, ErrReversed) {
			return out, err
		}
		return Reversal{}, err
	}
	if err := f.settle(in.Org, in.Test, "the reversal"); err != nil {
		return Reversal{}, err
	}
	return out, nil
}

// reversal is the legs that undo a deposit. Minted credit comes back out of the credit
// account it went into, which must still hold it. A PAYMENT — deposited as cash, or
// sourced as one since — undoes funding:cash and comes out of the wallet cash first,
// then credit: a bounced payment is money the customer never had, so the cash it
// became is taken back before anything else, and the wallet as a whole must cover it.
func reversal(tx ledger.Tx, orig ledger.JournalEntry, bucket string) ([]ledger.Posting, error) {
	was, err := sourced(tx, orig.ID)
	if err != nil {
		return nil, err
	}
	if !was && !strings.HasPrefix(bucket, acctCash) {
		bal, err := tx.Balance(bucket)
		if err != nil {
			return nil, err
		}
		if bal.Cmp(orig.Amount) < 0 {
			return nil, fmt.Errorf("%w (%s of %s)", ErrShortWallet, bal, orig.Amount)
		}
		legs := make([]ledger.Posting, 0, len(orig.Postings))
		for _, p := range orig.Postings {
			legs = append(legs, ledger.Posting{Account: p.Account, Amount: p.Amount.Neg()})
		}
		return legs, nil
	}
	wallet := acctWallet + strings.TrimPrefix(strings.TrimPrefix(bucket, acctWallet), acctCash)
	cash := cashOf(wallet)
	c, err := tx.Balance(cash)
	if err != nil {
		return nil, err
	}
	k, err := tx.Balance(wallet)
	if err != nil {
		return nil, err
	}
	if total := fundsOf(c, k).Total(); total.Cmp(orig.Amount) < 0 {
		return nil, fmt.Errorf("%w (%s of %s)", ErrShortWallet, total, orig.Amount)
	}
	fromCash, fromCredit := draw(orig.Amount, c)
	if fromCredit.Sign() > 0 {
		// Cash set aside for a payment is still the customer's cash and comes back as
		// cash when the hold is returned. Taking the shortfall from credit while it is
		// held would turn minted credit into cash; the hold is settled first.
		p, ok := tx.(interface {
			Prefixed(string) (money.Amount, error)
		})
		if !ok {
			return nil, errors.New("this ledger cannot read what is held")
		}
		held, err := p.Prefixed("held:" + cash + ":")
		if err != nil {
			return nil, err
		}
		if held.Sign() > 0 {
			return nil, fmt.Errorf("%w: %s of it is held for a payment not yet made; settle or return the hold first", ErrShortWallet, held)
		}
	}
	legs := []ledger.Posting{{Account: acctFundingCash, Amount: orig.Amount}}
	if fromCash.Sign() != 0 {
		legs = append(legs, ledger.Posting{Account: cash, Amount: fromCash.Neg()})
	}
	if fromCredit.Sign() != 0 {
		legs = append(legs, ledger.Posting{Account: wallet, Amount: fromCredit.Neg()})
	}
	return legs, nil
}

// ClawbackInput names a settled payment (its processor reference, the deposit's Ref),
// the processor's own id for the refund or dispute that returned money to the payer,
// and the amount returned.
type ClawbackInput struct {
	Org     string
	Subject string
	Payment string
	Ref     string
	Amount  money.Amount
	Reason  string
	Test    bool
}

// Clawback takes back money a processor returned to the payer from the wallet the
// payment credited: funding:cash is undone and the wallet pays out of cash first,
// then credit, which may go below zero — the money has left either way, and a debt
// on the books is the true record of it. Posted once per in.Ref.
//
// The payment must be one these books credited as cash, under its own reference:
// money nobody here credited is nobody's to take back.
func (f *ledgerFinance) Clawback(ctx context.Context, in ClawbackInput) (string, error) {
	if in.Amount.Sign() <= 0 || strings.TrimSpace(in.Payment) == "" || strings.TrimSpace(in.Ref) == "" {
		return "", errors.New("finance: a clawback names the payment, its own ref and a positive amount")
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", err
	}
	ref, program := "clawback:"+in.Ref, clawbackProgram(in.Payment)
	var id string
	posted := false
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		if prior, done, rerr := tx.EntryByRef(string(KindDeposit), program, ref); rerr != nil || done {
			id = prior.ID
			return rerr
		}
		dep, ok, derr := tx.EntryByRef(string(KindDeposit), "", in.Payment)
		if derr != nil {
			return derr
		}
		wallet, cash := walletAcct(in.Subject), cashAcct(in.Subject)
		if !ok || (credited(dep) != cash && credited(dep) != wallet) {
			return fmt.Errorf("finance: clawback %s: payment %q is not a deposit to %s in these books", in.Ref, in.Payment, in.Subject)
		}
		if credited(dep) == wallet {
			was, serr := sourced(tx, dep.ID)
			if serr != nil {
				return serr
			}
			if !was {
				return fmt.Errorf("finance: clawback %s: payment %q was credited as credit, not cash", in.Ref, in.Payment)
			}
		}
		// Never more than the payment brought in, across every refund and dispute of
		// it: what earlier clawbacks took (net of restores) is already gone.
		sp, ok := tx.(interface {
			SumProgram(kind, program string) (money.Amount, error)
		})
		if !ok {
			return errors.New("finance: this ledger cannot total a payment's clawbacks")
		}
		taken, serr := sp.SumProgram(string(KindDeposit), program)
		if serr != nil {
			return serr
		}
		amount := minAmount(in.Amount, dep.Amount.Add(taken))
		if amount.Sign() <= 0 {
			return nil // the whole payment is taken back already
		}
		c, cerr := tx.Balance(cash)
		if cerr != nil {
			return cerr
		}
		fromCash, fromCredit := draw(amount, c)
		legs := []ledger.Posting{{Account: acctFundingCash, Amount: amount}}
		if fromCash.Sign() != 0 {
			legs = append(legs, ledger.Posting{Account: cash, Amount: fromCash.Neg()})
		}
		if fromCredit.Sign() != 0 {
			legs = append(legs, ledger.Posting{Account: wallet, Amount: fromCredit.Neg()})
		}
		id, posted = mint.ID("dep"), true
		return tx.Insert(ledger.JournalEntry{
			ID: id, Kind: string(KindDeposit), Program: program, Ref: ref,
			Memo: "Clawback of " + in.Payment + ": " + strings.TrimSpace(in.Reason), Amount: amount.Neg(),
			CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		return "", err
	}
	if posted {
		if err := f.settle(in.Org, in.Test, "the clawback"); err != nil {
			return "", err
		}
	}
	return id, nil
}

// clawbackProgram groups every clawback and restore of one payment, so their total is
// one sum and a payment is never taken back past what it brought in.
func clawbackProgram(payment string) string { return "clawback:" + payment }

// Restore gives back a clawback the processor reversed — a dispute the merchant won:
// the clawback's own legs, mirrored, so each account gets back what it gave. Posted
// once per in.Ref; a ref that took nothing restores nothing.
func (f *ledgerFinance) Restore(ctx context.Context, in ClawbackInput) (string, error) {
	if strings.TrimSpace(in.Payment) == "" || strings.TrimSpace(in.Ref) == "" {
		return "", errors.New("finance: a restore names the payment and the clawback's ref")
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", err
	}
	program := clawbackProgram(in.Payment)
	var id string
	posted := false
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		if prior, done, rerr := tx.EntryByRef(string(KindDeposit), program, "restore:"+in.Ref); rerr != nil || done {
			id = prior.ID
			return rerr
		}
		orig, ok, gerr := tx.EntryByRef(string(KindDeposit), program, "clawback:"+in.Ref)
		if gerr != nil || !ok {
			return gerr
		}
		legs := make([]ledger.Posting, 0, len(orig.Postings))
		for _, p := range orig.Postings {
			legs = append(legs, ledger.Posting{Account: p.Account, Amount: p.Amount.Neg()})
		}
		id, posted = mint.ID("dep"), true
		return tx.Insert(ledger.JournalEntry{
			ID: id, Kind: string(KindDeposit), Program: program, Ref: "restore:" + in.Ref,
			Memo: "Restore of " + orig.ID + ": " + strings.TrimSpace(in.Reason), Amount: orig.Amount.Neg(),
			CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		return "", err
	}
	if posted || id != "" {
		if err := f.settle(in.Org, in.Test, "the restore"); err != nil {
			return "", err
		}
		told(in.Org)
	}
	return id, nil
}
