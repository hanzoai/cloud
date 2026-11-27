package finance

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/mint"
)

// A draw is a payment the wallet must cover in full: a plan period paid from
// prepaid money. It moves money the way a usage debit does — wallet →
// revenue:platform, idempotent on its ref within the wallet — and differs in two
// ways. It is refused when the wallet holds less than the amount: a metered call
// is admitted before it runs and debited after, so its debit must land whatever
// the wallet holds, while a payment is decided before anything is sold, so the
// check and the posting are one transaction and a concurrent spend cannot take
// the wallet below zero between them. And it is its own kind, [KindPayment], so
// the spend cap, the spend alerts and the usage page — which count metered usage
// — never count a plan's payment as AI spend.

// Drawer is the ledger that can draw a payment. The in-process finance client is
// one; callers reach it by interface assertion, as they reach Reverse.
type Drawer interface {
	Draw(ctx context.Context, in money.UsageInput) (entryID string, posted bool, err error)
}

var _ Drawer = (*ledgerFinance)(nil)

// Draw debits in from subject's wallet only when the wallet covers it, and
// answers [ErrShortWallet] otherwise, having moved nothing. A replay of the same
// ref answers the first entry with posted false and moves nothing, even when the
// wallet has since run dry; a ref that names a different amount is
// [ErrRefReused].
func (f *ledgerFinance) Draw(ctx context.Context, in money.UsageInput) (string, bool, error) {
	return f.debit(ctx, in, KindPayment)
}

// Refund gives back the payment drawn under in.Ref from in.Subject's wallet, exactly:
// the entry's own legs mirrored, so each bucket — cash, credit, a hold — gets back what
// it gave. It is posted once (ref "refund:<ref>"); a ref that drew nothing refunds
// nothing and answers "". It is how a payment for something that was then not
// delivered — a plan that did not open — is undone.
func (f *ledgerFinance) Refund(ctx context.Context, in money.UsageInput) (string, error) {
	if strings.TrimSpace(in.Ref) == "" {
		return "", errors.New("finance: a refund names the payment it gives back: ref is required")
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", err
	}
	program := usageProgram(in.Subject)
	var id string
	posted := false
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		live, last, lerr := liveRef(tx, program, in.Ref)
		if lerr != nil {
			return lerr
		}
		if live == "" {
			// Nothing holds money under the ref: the last attempt was refunded already,
			// and its refund is the answer; a ref that drew nothing answers "".
			if last != "" {
				prior, _, rerr := tx.EntryByRef(string(KindPayment), program, refundRef(last))
				id = prior.ID
				return rerr
			}
			return nil
		}
		if prior, done, rerr := tx.EntryByRef(string(KindPayment), program, refundRef(live)); rerr != nil || done {
			id = prior.ID
			return rerr
		}
		orig, ok, gerr := tx.EntryByRef(string(KindPayment), program, live)
		if gerr != nil || !ok {
			return gerr
		}
		legs := make([]ledger.Posting, 0, len(orig.Postings))
		for _, p := range orig.Postings {
			legs = append(legs, ledger.Posting{Account: p.Account, Amount: p.Amount.Neg()})
		}
		id, posted = mint.ID("use"), true
		return tx.Insert(ledger.JournalEntry{
			ID: id, Kind: string(KindPayment), Program: program, Ref: refundRef(live),
			Memo: "Refund of " + orig.ID, Amount: orig.Amount.Neg(), CreatedAt: time.Now().Unix(),
		}, legs)
	}); err != nil {
		return "", err
	}
	// A refund already posted is settled and told again: the call that posted it may have
	// failed before it could, and its retry is the only one that gets this far.
	if posted || id != "" {
		if err := f.settle(in.Org, in.Test, "the refund"); err != nil {
			return "", err
		}
		told(in.Org)
	}
	return id, nil
}

// refundRef is the ref a payment's refund is posted under.
func refundRef(ref string) string { return "refund:" + ref }

// A payment's ref names one act, and a refunded act is over: the same ref asked again
// is a new payment, not a replay of the one given back. The ledger keeps one entry per
// (kind, wallet, ref), so the n-th attempt under a ref posts as "<ref>#<n>".

// openRef is the ref a payment under ref posts as now: ref itself, or the first
// attempt after it that has not been refunded.
func openRef(tx ledger.Tx, program, ref string) (string, error) {
	cur := ref
	for n := 1; ; n++ {
		_, posted, err := tx.EntryByRef(string(KindPayment), program, cur)
		if err != nil || !posted {
			return cur, err
		}
		_, refunded, err := tx.EntryByRef(string(KindPayment), program, refundRef(cur))
		if err != nil || !refunded {
			return cur, err
		}
		cur = ref + "#" + strconv.Itoa(n)
	}
}

// liveRef is the attempt under ref that holds money now — the last one posted and not
// refunded — or "" when none does, beside the last attempt that was refunded.
func liveRef(tx ledger.Tx, program, ref string) (live, refunded string, err error) {
	cur := ref
	for n := 1; ; n++ {
		_, posted, err := tx.EntryByRef(string(KindPayment), program, cur)
		if err != nil || !posted {
			return "", refunded, err
		}
		_, back, err := tx.EntryByRef(string(KindPayment), program, refundRef(cur))
		if err != nil {
			return "", "", err
		}
		if !back {
			return cur, refunded, nil
		}
		refunded = cur
		cur = ref + "#" + strconv.Itoa(n)
	}
}
