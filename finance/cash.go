package finance

// cash.go — WHERE A WALLET'S MONEY CAME FROM.
//
// A wallet holds two kinds of money. CASH is money a customer paid: a settled card
// payment, a wire. CREDIT is money Hanzo minted: a signup or promo grant, an admin
// grant, a carry from an older ledger, a seller's x402 receipt, Hanzo's own balance.
// Both buy any work; the split is what a refund takes back and what payouts accrue on.
//
// They are two ACCOUNTS, not a tag: cash[:<user>] is funded from funding:cash and
// wallet[:<user>] from funding:platform. The account a posting names is the record of
// where the money came from, so the balance, the gate and the books read the same
// postings and nothing parses a memo to know it.
//
// A debit draws CASH FIRST ([draw]): metered usage, a plan paid from the balance and a
// hold all take cash before credit. Metered usage then draws the org's CREDIT LINE —
// cash below zero, down to the limit, owed and invoiced — and only then granted
// credit ([drawUsage]); what none of them covers is absorbed by the platform rather
// than creating customer debt beyond the line. A usage the policy keeps off granted
// credit ([money.UsageInput.Cash]) draws cash and the line and never credit.
//
// THE LINE IS THE ORG'S. Only the org's own account (cash/wallet, no member suffix)
// draws on it: in the signup org every self-serve person has a wallet of their own
// beside the org's, and an org-wide line spent from those would be the platform's
// credit handed to strangers.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/mint"
)

// Funds is one wallet's money by source. Every part is >= 0.
//
// Cash is what the customer paid and has not spent; Credit what the platform minted
// and has not been spent. Owed is what the wallet owes: cash below zero, the amount
// due, which granted credit never pays and a payment pays down first. Limit is the
// org's credit line and Line what of it is left to draw, Limit - Owed. A credit
// account below zero — an overdraft from before the line — is owed out of cash first.
// Account is an org on account ([SetAccount]): its line has no limit, and Line reads
// [open] whatever it owes.
type Funds struct {
	Cash    money.Amount
	Credit  money.Amount
	Line    money.Amount
	Owed    money.Amount
	Limit   money.Amount
	Account bool
}

// Own is what the wallet's own money may still pay: its cash, then the line left. It
// is everything but granted credit, so it is what a usage the policy keeps off granted
// credit can draw ([drawUsage] with cashOnly), and the usage policy's prepaid.
func (f Funds) Own() money.Amount { return f.Cash.Add(f.Line) }

// Total is what the wallet may still spend: its own money and its credit. The gate
// admits against it.
func (f Funds) Total() money.Amount { return f.Own().Add(f.Credit) }

// fundsOf folds a wallet's two signed account balances into what it holds and owes,
// with no line.
func fundsOf(cash, credit money.Amount) Funds {
	zero := money.Zero()
	c, k := cash, credit
	if k.IsNeg() {
		c, k = c.Add(k), zero
	}
	out := Funds{Cash: c, Credit: k, Line: zero, Owed: zero, Limit: zero}
	if c.IsNeg() {
		out.Cash, out.Owed = zero, c.Neg()
	}
	return out
}

// fundsWith is [fundsOf] on a wallet the org's credit line of limit covers.
func fundsWith(cash, credit, limit money.Amount) Funds {
	f := fundsOf(cash, credit)
	f.Limit = limit
	if left := limit.Sub(f.Owed); left.Sign() > 0 {
		f.Line = left
	}
	return f
}

// lined reports whether subject's wallet is the org's own account, the one its
// credit line covers.
func lined(subject string) bool { return walletAcct(subject) == acctWallet }

// limitIn is the line subject's wallet may draw on, read inside tx: the org's limit
// for the org's own account, zero for anyone else's.
func limitIn(tx ledger.Tx, subject string) (money.Amount, error) {
	if !lined(subject) {
		return money.Zero(), nil
	}
	l, ok := tx.(interface{ Limit() (money.Amount, error) })
	if !ok {
		return money.Zero(), errors.New("finance: this ledger keeps no credit limit")
	}
	return l.Limit()
}

// cashAcct is subject's cash account, named by the rule [walletAcct] states.
func cashAcct(subject string) string { return account(acctCash, subject) }

// Holds reports whether a deposit's account belongs to subject's wallet: its cash, or
// its credit for a payment written before the ledger kept cash apart. It is how a
// reader outside this package compares a deposit to the account it was meant for,
// without restating how an account is named.
func Holds(account, subject string) bool {
	return account == cashAcct(subject) || account == walletAcct(subject)
}

// cashOf is the cash account beside a credit account: "wallet:bob" → "cash:bob".
func cashOf(wallet string) string { return acctCash + strings.TrimPrefix(wallet, acctWallet) }

// sourceOf is the (funding, bucket) pair a deposit posts between: a payment's money
// lands in cash, anything minted in credit.
func sourceOf(in money.DepositInput) (funding, bucket string) {
	if in.Cash {
		return acctFundingCash, cashAcct(in.Subject)
	}
	return acctFunding, walletAcct(in.Subject)
}

// bucketsIn reads a wallet's two accounts inside tx, signed.
func bucketsIn(tx ledger.Tx, subject string) (cash, credit money.Amount, err error) {
	if cash, err = tx.Balance(cashAcct(subject)); err != nil {
		return money.Zero(), money.Zero(), err
	}
	if credit, err = tx.Balance(walletAcct(subject)); err != nil {
		return money.Zero(), money.Zero(), err
	}
	return cash, credit, nil
}

// draw splits an amount across a wallet's buckets, cash first: cash gives what it
// holds, up to the amount, and credit the rest. Cash never goes below zero.
func draw(amount, cash money.Amount) (fromCash, fromCredit money.Amount) {
	fromCash = money.Zero()
	if cash.Sign() > 0 {
		fromCash = minAmount(amount, cash)
	}
	return fromCash, amount.Sub(fromCash)
}

// split is what each source pays of one metered debit.
type split struct {
	cash, line, credit, over money.Amount
}

// drawUsage splits a metered debit across the wallet in the owner's order: cash,
// then the line, then granted credit (unless cashOnly), and what none of them covers
// is left in over (platform-absorbed, never posted as debt past the line).
func drawUsage(amount money.Amount, f Funds, cashOnly bool) split {
	var s split
	rest := amount
	s.cash = minAmount(rest, f.Cash)
	rest = rest.Sub(s.cash)
	s.line = minAmount(rest, f.Line)
	rest = rest.Sub(s.line)
	s.credit = money.Zero()
	if !cashOnly {
		s.credit = minAmount(rest, f.Credit)
		rest = rest.Sub(s.credit)
	}
	s.over = rest
	return s
}

// paidBy names who paid a metered debit, in the order they paid: one word when one
// source paid all of it, "prepaid+line" when more did. Anything beyond cash, the
// line and credit is absorbed by the platform and reads as hanzo, never debt.
func (s split) paidBy() string {
	var by []string
	if s.cash.Sign() > 0 {
		by = append(by, money.PaidByPrepaid)
	}
	if s.line.Sign() > 0 {
		by = append(by, money.PaidByLine)
	}
	if s.credit.Sign() > 0 {
		by = append(by, money.PaidByCredits)
	}
	if s.over.Sign() > 0 {
		by = append(by, money.PaidByHanzo)
	}
	return strings.Join(by, "+")
}

func minAmount(a, b money.Amount) money.Amount {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

// Funder is the ledger that can say where a wallet's balance came from. The
// in-process finance client is one; callers reach it by interface assertion, as they
// reach Draw.
type Funder interface {
	Funds(ctx context.Context, org, subject string, test bool) (Funds, error)
}

var _ Funder = (*ledgerFinance)(nil)

// Funds reads subject's wallet in org as cash and credit, from one snapshot of the
// file. It is the ONE place the split is computed: the balance route, the plane's
// balance op and the paid-lane gate all read it. A read that fails is an error, never
// a zero.
func (f *ledgerFinance) Funds(ctx context.Context, org, subject string, test bool) (Funds, error) {
	none := fundsOf(money.Zero(), money.Zero())
	store, err := f.storeFor(org, test)
	if err != nil {
		return none, err
	}
	cash, credit := cashAcct(subject), walletAcct(subject)
	bals, err := store.Balances(ctx, cash, credit)
	if err != nil {
		return none, err
	}
	limit, account := money.Zero(), false
	if lined(subject) {
		if limit, err = store.Limit(ctx); err != nil {
			return none, err
		}
		account = onAccount(ctx, org)
	}
	return opened(fundsWith(bals[cash], bals[credit], limit), account), nil
}

// OrgCash is one org's customer money as its books state it. Paid is every dollar a
// payment brought in, net of reversals; Owed the cash still unspent across its
// wallets, held or not — what Hanzo owes the customer; Spent what cash has paid for.
// Credit is minted money still unspent, which Hanzo owes nobody.
type OrgCash struct {
	Books  bool // false: the org has no ledger, and every figure is zero
	Paid   money.Amount
	Owed   money.Amount
	Spent  money.Amount
	Credit money.Amount
}

// Cash reads OrgCash for org. It opens nothing: an org with no ledger answers
// Books=false rather than being handed an empty file.
func (f *ledgerFinance) Cash(ctx context.Context, org string, test bool) (OrgCash, error) {
	out := OrgCash{Paid: money.Zero(), Owed: money.Zero(), Spent: money.Zero(), Credit: money.Zero()}
	if !f.books.Has(org, test) {
		return out, nil
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return out, err
	}
	funding, err := store.Balance(ctx, acctFundingCash)
	if err != nil {
		return out, err
	}
	out.Books = true
	out.Paid = funding.Neg()
	// Per wallet, through fundsOf — the same fold the customer's balance takes — so a
	// wallet whose credit is overdrawn owes out of its cash here exactly as it does there.
	cash, err := store.BalancesWithPrefix(ctx, acctCash)
	if err != nil {
		return out, err
	}
	credit, err := store.BalancesWithPrefix(ctx, acctWallet)
	if err != nil {
		return out, err
	}
	held, err := store.BalancesWithPrefix(ctx, "held:")
	if err != nil {
		return out, err
	}
	for acct, bal := range held {
		switch {
		case strings.HasPrefix(acct, "held:"+acctCash):
			out.Owed = out.Owed.Add(bal)
		case strings.HasPrefix(acct, "held:"+acctWallet):
			out.Credit = out.Credit.Add(bal)
		}
	}
	for wallet, k := range credit {
		c, ok := cash[cashOf(wallet)]
		if !ok {
			c = money.Zero()
		}
		funds := fundsOf(c, k)
		out.Owed = out.Owed.Add(funds.Cash)
		out.Credit = out.Credit.Add(funds.Credit)
	}
	for acct, c := range cash {
		if _, ok := credit[acctWallet+strings.TrimPrefix(acct, acctCash)]; !ok && c.Sign() > 0 {
			out.Owed = out.Owed.Add(c) // a wallet with cash and no credit account yet
		}
	}
	out.Spent = out.Paid.Sub(out.Owed)
	return out, nil
}

// Sourced is one wallet's sourcing: which of its deposits were payments, what of them
// is still unspent, and the entry that moved it.
type Sourced struct {
	Wallet  string       // the credit account the deposits landed in, e.g. "wallet:bob"
	Paid    money.Amount // those deposits' sum, net of reversals
	Cash    money.Amount // what of Paid is unspent, replayed cash first
	Balance money.Amount // the wallet's credit account, which sourcing leaves summed with cash as it was
	Entries []string     // the deposits counted as paid
	Holds   int          // hold movements in the replay; money a hold returns comes back as credit
	Posted  string       // the wallet's source entry; empty on a dry run
	Done    bool         // sourced before: Posted is the earlier entry and nothing was replayed
}

// sourceRef is the ref a source entry is posted under: one per deposit shown to be a
// payment ("source:dep_…"), whose legs re-source its funding, and one per wallet
// ("source:wallet:bob"), whose legs move the unspent part into cash. Both post once.
func sourceRef(of string) string { return "source:" + of }

// sourced reports whether deposit id was shown to be a payment after it was written.
func sourced(tx ledger.Tx, id string) (bool, error) {
	_, ok, err := tx.EntryByRef(string(KindSource), "", sourceRef(id))
	return ok, err
}

// legger is the transaction that can read an account's history where it writes.
type legger interface {
	Legs(accounts ...string) ([]sqlstore.Leg, error)
}

// Source records which of an org's earlier deposits were payments.
//
// Every deposit written before the ledger kept cash apart sits in a wallet's credit
// account. paid names the ones a payment funded, by entry id. For each wallet holding
// one, Source replays the wallet's history cash first — the rule every debit follows
// from now on — inside the transaction that posts, so no debit lands between the
// replay and the entry. It posts one entry per payment, re-sourcing its funding from
// funding:platform to funding:cash, and one per wallet moving the unspent part into
// cash. The balance does not move; Paid, Owed and Spent read true from then on, and a
// reversal of a sourced payment takes cash first (reverse.go). Exactly once per wallet.
// apply false answers the same table and posts nothing.
//
// A paid id that is not a deposit in these books, or is already cash, is refused
// before anything posts.
func (f *ledgerFinance) Source(ctx context.Context, org string, test bool, paid map[string]bool, apply bool) ([]Sourced, error) {
	store, err := f.storeFor(org, test)
	if err != nil {
		return nil, err
	}
	deposits, err := store.EntriesOfKind(ctx, string(KindDeposit))
	if err != nil {
		return nil, err
	}
	byID := map[string]ledger.JournalEntry{}
	for _, e := range deposits {
		byID[e.ID] = e
	}
	wallets := map[string][]string{}
	for id := range paid {
		e, ok := byID[id]
		if !ok || e.Amount.Sign() <= 0 {
			return nil, fmt.Errorf("finance: source %s: %q is not a deposit in these books", org, id)
		}
		w := credited(e)
		if !strings.HasPrefix(w, acctWallet) {
			return nil, fmt.Errorf("finance: source %s: %q credited %s, which is already cash", org, id, w)
		}
		wallets[w] = append(wallets[w], id)
	}
	names := make([]string, 0, len(wallets))
	for w := range wallets {
		names = append(names, w)
	}
	sort.Strings(names)
	out := make([]Sourced, 0, len(names))
	for _, w := range names {
		c, err := f.source(ctx, store, org, test, w, wallets[w], paid, apply)
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, nil
}

// source replays and, when apply, posts one wallet's source entries, in one
// transaction. A dry run rolls it back.
func (f *ledgerFinance) source(ctx context.Context, store *sqlstore.Store, org string, test bool, wallet string, named []string, paid map[string]bool, apply bool) (Sourced, error) {
	out := Sourced{Wallet: wallet, Paid: money.Zero(), Cash: money.Zero(), Balance: money.Zero()}
	posted := false
	err := store.Tx(ctx, func(tx ledger.Tx) error {
		prior, done, ferr := tx.EntryByRef(string(KindSource), "", sourceRef(wallet))
		if ferr != nil {
			return ferr
		}
		if done {
			// Sourced once, for good. A payment named after that is refused rather than
			// reported as cash the ledger does not hold.
			for _, id := range named {
				was, serr := sourced(tx, id)
				if serr != nil {
					return serr
				}
				if !was {
					return fmt.Errorf("finance: source %s/%s: sourced by %s already, and %s was not among its payments", org, wallet, prior.ID, id)
				}
			}
			out.Done, out.Posted = true, prior.ID
			return nil
		}
		lt, ok := tx.(legger)
		if !ok {
			return errors.New("finance: this ledger cannot read a wallet's history in a transaction")
		}
		legs, lerr := lt.Legs(wallet)
		if lerr != nil {
			return lerr
		}
		r := replay(legs, paid)
		out.Paid, out.Cash, out.Balance, out.Entries, out.Holds = r.paid, r.cash, r.balance, r.entries, r.holds
		if !apply || out.Paid.Sign() <= 0 {
			return errDryRun
		}
		now := time.Now().Unix()
		for _, id := range r.entries {
			a := r.byDeposit[id]
			if a.Sign() <= 0 {
				continue // reversed: nothing left to re-source
			}
			if err := tx.Insert(ledger.JournalEntry{
				ID: mint.ID("src"), Kind: string(KindSource), Ref: sourceRef(id),
				Memo: "Source: " + id + " was a payment", Amount: a, CreatedAt: now,
			}, []ledger.Posting{
				{Account: acctFundingCash, Amount: a.Neg()},
				{Account: acctFunding, Amount: a},
			}); err != nil {
				return err
			}
		}
		moved := r.cash
		legsOut := []ledger.Posting{{Account: wallet, Amount: moved.Neg()}, {Account: cashOf(wallet), Amount: moved}}
		out.Posted = mint.ID("src")
		posted = true
		return tx.Insert(ledger.JournalEntry{
			ID: out.Posted, Kind: string(KindSource), Ref: sourceRef(wallet),
			Memo:   fmt.Sprintf("Source: %d earlier deposit(s) were payments; %s unspent moves to cash", len(r.entries), moved),
			Amount: moved, CreatedAt: now,
		}, legsOut)
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	if err != nil {
		return out, err
	}
	if posted {
		if err := f.settle(org, test, "the source"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// errDryRun ends a transaction that must post nothing.
var errDryRun = errors.New("dry run")

// replayed is a wallet's history replayed cash first.
type replayed struct {
	paid, cash, balance money.Amount
	byDeposit           map[string]money.Amount // each payment, net of its reversal
	entries             []string
	holds               int
}

// replay walks one credit account's postings in the order they were written, as if
// every debit had drawn cash first all along. A deposit named in paid is cash; its
// reversal takes back cash first. Anything else moving money out takes cash first;
// money coming back in — a hold returned, a hold's surplus — returns as credit, so the
// replay can under-state cash and never over-state it.
func replay(legs []sqlstore.Leg, paid map[string]bool) replayed {
	r := replayed{paid: money.Zero(), cash: money.Zero(), balance: money.Zero(), byDeposit: map[string]money.Amount{}}
	for _, l := range legs {
		r.balance = r.balance.Add(l.Amount)
		kind := ParseKind(l.Kind)
		if kind == KindHold {
			r.holds++
		}
		orig := strings.TrimPrefix(l.Ref, reversalRef(""))
		switch {
		case kind == KindDeposit && l.EntryAmount.Sign() > 0 && paid[l.Entry]:
			r.cash = r.cash.Add(l.Amount)
			r.paid = r.paid.Add(l.Amount)
			r.byDeposit[l.Entry] = l.Amount
			r.entries = append(r.entries, l.Entry)
		case kind == KindDeposit && l.EntryAmount.Sign() < 0 && paid[orig]:
			back := l.Amount.Neg()
			r.paid = r.paid.Sub(back)
			if a, ok := r.byDeposit[orig]; ok {
				r.byDeposit[orig] = a.Sub(back)
			}
			fromCash, _ := draw(back, r.cash)
			r.cash = r.cash.Sub(fromCash)
		case l.Amount.Sign() < 0:
			fromCash, _ := draw(l.Amount.Neg(), r.cash)
			r.cash = r.cash.Sub(fromCash)
		}
	}
	return r
}

// DepositRow is one deposit as the books hold it: the bucket it funded (a credit
// account "wallet[:<user>]" or a cash account "cash[:<user>]"), its ref and note, and
// whether a reversal has undone it.
type DepositRow struct {
	ID        string
	Wallet    string
	Ref       string
	Memo      string
	Amount    money.Amount
	CreatedAt int64
	Reversed  bool
	Sourced   bool // shown to be a payment after it was written
}

// Cash reports whether the deposit is a payment: it funded a cash account, or was
// sourced as one since.
func (d DepositRow) Cash() bool { return d.Sourced || strings.HasPrefix(d.Wallet, acctCash) }

// Deposits lists every deposit in org's books, oldest first — the rows an operator
// reads to say which of the earlier ones were payments ([ledgerFinance.Source]). It
// reads deposits and source entries only, never the metered debits. An org with no
// books has no deposits, and none are opened for it.
func (f *ledgerFinance) Deposits(ctx context.Context, org string, test bool) ([]DepositRow, error) {
	if !f.books.Has(org, test) {
		return nil, nil
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return nil, err
	}
	entries, err := store.EntriesOfKind(ctx, string(KindDeposit))
	if err != nil {
		return nil, err
	}
	sources, err := store.EntriesOfKind(ctx, string(KindSource))
	if err != nil {
		return nil, err
	}
	was := map[string]bool{}
	for _, e := range sources {
		was[strings.TrimPrefix(e.Ref, sourceRef(""))] = true
	}
	reversed := map[string]bool{}
	for _, e := range entries {
		if e.Amount.Sign() < 0 {
			reversed[strings.TrimPrefix(e.Ref, reversalRef(""))] = true
		}
	}
	var out []DepositRow
	for _, e := range entries {
		if e.Amount.Sign() <= 0 {
			continue
		}
		out = append(out, DepositRow{
			ID: e.ID, Wallet: credited(e), Ref: e.Ref, Memo: e.Memo,
			Amount: e.Amount, CreatedAt: e.CreatedAt, Reversed: reversed[e.ID], Sourced: was[e.ID],
		})
	}
	return out, nil
}

// SumCashUsageSince is the part of org's metered usage at/after since (unix seconds)
// that cash paid for, in cents — what payout programs accrue on: a commission or a
// royalty is a share of money a customer paid, never of credit Hanzo minted.
func (f *ledgerFinance) SumCashUsageSince(ctx context.Context, org string, test bool, since int64) (int64, error) {
	if !f.books.Has(org, test) {
		return 0, nil
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return 0, err
	}
	sum, err := store.SumLegsSince(ctx, string(KindUsage), since, acctCash)
	if err != nil {
		return 0, err
	}
	// Net of money processors took back in the same window: a refunded or disputed
	// payment accrues nothing for anyone (restores add it back).
	back, err := store.SumProgramsSince(ctx, string(KindDeposit), "clawback:", since)
	if err != nil {
		return 0, err
	}
	return max(sum.Neg().Add(back).CentsDown(), 0), nil
}
