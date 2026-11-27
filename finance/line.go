package finance

// line.go — THE ORG'S CREDIT LINE: served calls paid in arrears.
//
// An org with a line may spend past its cash: metered usage takes cash below zero,
// down to minus the limit, after the org's cash and before its granted credit
// ([drawUsage]). Cash below zero is AMOUNT DUE — real money the org owes — and it is
// the org's receivable on these books: a payment lands in cash and pays it down
// before it is spendable again, granted credit never pays it, and the gate refuses a
// priced call once cash, the line and credit are spent (finance.Funds.Total).
//
// The limit lives in the org's own books beside the money it bounds (sqlstore
// treasury_limit), so the line and the debt it allows are one file, shipped together.
// It is zero until a SuperAdmin sets it; who set it and from what is on the audit
// trail, recorded by the op that sets it (apps/commerce line_rpc.go).
//
// AN ORG ON ACCOUNT has a line with no limit: the platform bills it in arrears and it
// settles out of band. Its own account always has [open] left to draw, so no gate
// refuses one of its calls for money, and every call posts exactly as on any line —
// to Owed, the receivable, until a payment pays it down. Which orgs are on account is
// a platform switch finance does not read; the host installs the reader
// ([SetAccount]). Only the org's own account is on account: a member's wallet in the
// signup org is that person's, and stays theirs.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/mint"
)

// Line is the org's own account as its books state it: what it holds, what it owes,
// and its credit line. Books false: the org has no ledger, and every figure is zero.
//
// Since is when the account last went from owing nothing to owing, in unix seconds:
// how old what it owes is. It is 0 when it owes nothing, and when the debt began
// before the books kept the moment. Used is the metered usage the account paid for
// since Period, the start of the current UTC month.
type Line struct {
	Books bool
	Funds
	Since  int64
	Used   money.Amount
	Period int64
}

// Liner is the ledger that keeps credit lines. The in-process finance client is one;
// callers reach it by interface assertion, as they reach Draw.
type Liner interface {
	// Line reads org's own account. It opens nothing: an org with no ledger answers
	// Books false rather than being handed an empty file.
	Line(ctx context.Context, org string, test bool) (Line, error)
	// SetLimit sets org's credit limit and answers the account after it.
	SetLimit(ctx context.Context, org string, test bool, limit money.Amount) (Line, error)
	// Waive forgives what org's own account currently owes, bringing Owed to zero
	// without minting cash or touching granted credit.
	Waive(ctx context.Context, org string, test bool, reason string) (Line, error)
}

var _ Liner = (*ledgerFinance)(nil)

// open is the line an org on account always has left: more than any call, hold or
// burst of calls asks, and small enough that every gate's integer reading of a
// balance, in nano-dollars or in cents, holds it exactly. It is the line left, not
// a limit: what the org owes never takes from it.
var open = money.FromCents(100_000_000_000) // $1,000,000,000

// arrears is the host's answer to "is this org on account", installed at boot.
var arrears atomic.Pointer[func(ctx context.Context, org string) bool]

// SetAccount installs the reader that says whether an org is on account. The host
// that holds the books installs it (apps/commerce, which reads the platform switch);
// with none installed, no org is. Pass nil to clear.
func SetAccount(fn func(ctx context.Context, org string) bool) {
	if fn == nil {
		arrears.Store(nil)
		return
	}
	arrears.Store(&fn)
}

// onAccount reports whether org is on account.
func onAccount(ctx context.Context, org string) bool {
	fn := arrears.Load()
	return fn != nil && (*fn)(ctx, org)
}

// opened is f for an org that is on account when account is true: its line left is
// [open], whatever it owes and whatever its limit.
func opened(f Funds, account bool) Funds {
	if account {
		f.Account, f.Line = true, open
	}
	return f
}

// Line reads org's own account: its cash, credit, what it owes and its line, how old
// the debt is and what the account used this month.
func (f *ledgerFinance) Line(ctx context.Context, org string, test bool) (Line, error) {
	period := monthOf(time.Now())
	if !f.books.Has(org, test) {
		return Line{Funds: opened(fundsOf(money.Zero(), money.Zero()), onAccount(ctx, org)), Used: money.Zero(), Period: period}, nil
	}
	funds, err := f.Funds(ctx, org, org, test)
	if err != nil {
		return Line{}, err
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return Line{}, err
	}
	out := Line{Books: true, Funds: funds, Period: period}
	if funds.Owed.Sign() > 0 {
		if out.Since, err = store.Owing(ctx); err != nil {
			return Line{}, err
		}
	}
	if out.Used, err = store.SumProgramSince(ctx, string(KindUsage), usageProgram(org), period); err != nil {
		return Line{}, err
	}
	return out, nil
}

// monthOf is the start of t's UTC month, in unix seconds.
func monthOf(t time.Time) int64 {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
}

// SetLimit records org's credit limit. A limit below what the org already owes is
// allowed — it stops further drawing; it does not forgive or call in the debt.
func (f *ledgerFinance) SetLimit(ctx context.Context, org string, test bool, limit money.Amount) (Line, error) {
	if limit.IsNeg() {
		return Line{}, errNegativeLimit
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return Line{}, err
	}
	before, err := store.Limit(ctx)
	if err != nil {
		return Line{}, err
	}
	if err := store.SetLimit(ctx, limit, time.Now().Unix()); err != nil {
		return Line{}, err
	}
	if err := f.settle(org, test, "the credit limit"); err != nil {
		return Line{}, err
	}
	// Only a line raised can let a refusal go: a line lowered or written again clears nothing.
	if limit.Cmp(before) > 0 {
		told(org)
	}
	return f.Line(ctx, org, test)
}

// Waive forgives what org's own account currently owes (cash and/or legacy credit
// below zero), bringing Owed to zero without minting paid cash or touching granted
// credit. It posts one balanced reversing usage entry (cash/wallet up,
// revenue:platform down) so both the receivable and the erroneous revenue are
// undone. Idempotent: when Owed is already zero, nothing moves.
func (f *ledgerFinance) Waive(ctx context.Context, org string, test bool, reason string) (Line, error) {
	if !f.books.Has(org, test) {
		return f.Line(ctx, org, test)
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return Line{}, err
	}
	memo := strings.TrimSpace(reason)
	if memo == "" {
		memo = "debt waived"
	}
	var waived bool
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		cash, credit, berr := bucketsIn(tx, org)
		if berr != nil {
			return berr
		}
		owed := fundsOf(cash, credit).Owed
		if owed.Sign() <= 0 {
			return nil
		}
		var legs []ledger.Posting
		credited := money.Zero()
		if credit.IsNeg() {
			amt := credit.Neg()
			legs = append(legs, ledger.Posting{Account: walletAcct(org), Amount: amt})
			credited = credited.Add(amt)
		}
		if cash.IsNeg() {
			amt := cash.Neg()
			legs = append(legs, ledger.Posting{Account: cashAcct(org), Amount: amt})
			credited = credited.Add(amt)
		}
		// If positive cash partially offset a legacy negative credit, settle the net owed
		// directly into cash so the journal entry balances exactly.
		if credited.Cmp(owed) != 0 {
			legs = []ledger.Posting{{Account: cashAcct(org), Amount: owed}}
		}
		legs = append(legs, ledger.Posting{Account: acctRevenue, Amount: owed.Neg()})
		id := mint.ID("use")
		waived = true
		return tx.Insert(ledger.JournalEntry{
			ID:        id,
			Kind:      string(KindUsage),
			Program:   usageProgram(org),
			Ref:       "waive:" + id,
			Memo:      memo,
			Amount:    owed.Neg(),
			CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		return Line{}, err
	}
	if waived {
		if err := f.settle(org, test, "the debt waiver"); err != nil {
			return Line{}, err
		}
		told(org)
	}
	return f.Line(ctx, org, test)
}

// errNegativeLimit is a credit limit below zero, which names nothing.
var errNegativeLimit = errors.New("finance: a credit limit is zero or more")
