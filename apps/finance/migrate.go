// Copyright © 2026 Hanzo AI. MIT License.

package finance

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// MigrateOrg carries an org's existing prepaid balance into its native finance wallet,
// EXACTLY ONCE — the cutover primitive that moves each tenant's money from the legacy
// commerce ledger onto the ONE finance ledger. It deposits balanceCents into org's pooled
// wallet (subject == the org slug) under the FIXED idempotency ref "backfill:<org>", so a
// re-run (a retried operator call, a replayed job) finds the existing entry and credits
// the wallet at most once — returning the original entry id, moving no money. A
// non-positive balance is skipped ("" , nil): there is nothing to carry. It resolves the
// process-wide finance client published at boot; a nil client (finance not co-resident on
// this deployment) is an error, since there is no wallet to migrate into.
//
// # A re-run is a no-op whatever the legacy balance says by then
//
// The ref is fixed and the AMOUNT IS NOT, and that is the whole of the second read below.
// finance's own idempotency answers a replay with the first entry only when the ref names
// the SAME (subject, amount) — a rule that is exactly right for a settlement, where a ref
// is one payment and a differing amount means a DIFFERENT payment wearing a taken key
// ([ErrRefTaken]). A backfill is not that. Its ref names an ORG's one cutover, its amount
// is whatever commerce happened to hold when somebody ran it, and that figure moves:
// the customer spends, tops up, is granted credit. So the honest re-run — an operator
// running the cutover twice over a balance that changed in between — collided with the
// conflict rule and was answered with an error, breaking the exactly-once this function
// exists to promise and making a retried cutover look like a failed one.
//
// It is read back instead: the ref is THIS org's own and nothing else may write it, so a
// conflict on it means the carry already happened. The entry it happened under is
// answered, no money moves, and the answer is the same on every subsequent run — which
// is what "exactly once" has to mean for a primitive whose input is a moving number.
// The conflict rule itself is untouched: a SETTLEMENT ref that is another payment's is
// still refused, loudly, everywhere else.
func MigrateOrg(ctx context.Context, org string, balanceCents int64) (string, error) {
	if balanceCents <= 0 {
		return "", nil
	}
	fin := Current()
	if fin == nil {
		return "", fmt.Errorf("finance: not published; cannot migrate %q", org)
	}
	ref := carryRef(org)
	entry, err := fin.Deposit(ctx, types.DepositInput{
		Org:      org,
		Subject:  org, // the org-pool wallet (subject == slug)
		Amount:   money.FromCents(balanceCents),
		Currency: "usd",
		Notes:    "commerce balance backfill",
		Tags:     "backfill",
		Ref:      ref, // fixed ref → exactly-once cutover
	})
	if !errors.Is(err, ErrRefTaken) {
		return entry, err
	}
	return carried(ctx, fin, org, ref)
}

// carryRef is the org's ONE cutover key, stated once because two callers name it: the
// deposit that carries the balance and the read that answers a carry already done. A ref
// spelled at two call sites is a ref they can eventually spell differently, and this one
// is the whole of the exactly-once.
func carryRef(org string) string { return "backfill:" + org }

// carried answers the entry an org's carry is ALREADY posted under, whatever amount it
// carried at the time.
//
// It is deliberately NOT [creditedUnder]: that one asks whether THIS deposit is posted —
// same subject, same amount — which is the question a settlement has, and it answers a
// changed amount with the very conflict this is recovering from. The question here is the
// one a fixed per-org ref has: is this org's cutover done, and under which entry. Only the
// ref is matched, because only one writer may ever use it.
//
// It reads the LIVE books because [MigrateOrg] writes them: a cutover carries real money,
// and the sandbox ledger has no legacy balance to move.
//
// The read needs the ledger FILE and the money seam does not carry one, so it asks the
// published client for its books. *ledgerFinance is the only value in this process that
// has any — a client that is not one cannot answer, and says so rather than reporting a
// carry it did not verify.
func carried(ctx context.Context, fin Client, org, ref string) (string, error) {
	books, ok := fin.(*ledgerFinance)
	if !ok {
		return "", fmt.Errorf("finance: %q is already carried under ref %q and this client holds no books to read the entry back from", org, ref)
	}
	store, err := books.storeFor(org, false)
	if err != nil {
		return "", err
	}
	var entry string
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		e, ok, ferr := tx.EntryByRef(string(KindDeposit), "", ref)
		if ferr != nil || !ok {
			return ferr
		}
		entry = e.ID
		return nil
	}); err != nil {
		return "", fmt.Errorf("finance: read the carry already posted for %q: %w", org, err)
	}
	if entry == "" {
		// The ref was refused as taken and then named nothing. Nothing in this package
		// deletes an entry, so this is a state that should not exist — reported rather
		// than papered over with a second carry, which is the one outcome that would
		// move money twice.
		return "", fmt.Errorf("finance: the carry ref %q for %q is taken and names no entry", ref, org)
	}
	return entry, nil
}
