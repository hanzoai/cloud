package books

// ingest.go — the ONE posting source: a per-org cursor over commerce's transaction
// ledger (GET /v1/billing/transactions). commerce's prepaid wallet is the source of
// truth for money moved; this projects each transaction into a balanced double-entry
// voucher via a FIXED rule map (no AI, no heuristics) and posts it through the choke
// point. It is READ-ONLY against commerce — it never calls the mint-gated
// deposit/credit/payout endpoints — and the transactions endpoint is the SOLE source, so
// the books can never double-count a move that is also visible in cloud_usage/finance.
//
// IDEMPOTENCY. Every voucher is keyed (commerce_txn, txn.ID), so re-running ingestion —
// on a schedule, after a crash, over an overlapping cursor window — posts each commerce
// transaction exactly once. The cursor is an OPTIMIZATION over that guarantee (it caps
// re-scan), never the correctness boundary.

import (
	"context"
	"sort"
	"strings"
)

// commerceTxn is one row of commerce GET /v1/billing/transactions (only the fields the
// rule map reads). Type is "deposit" (a top-up / credit purchase) or "withdraw" (usage /
// consumption); Amount is the magnitude in cents.
type commerceTxn struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Tags      string `json:"tags"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"createdAt"`
}

// txnSource is the measurement seam to commerce: per-org transactions, live or sandbox.
// Production is commerceReader (the S2S read); tests inject a fake. It is the ONLY seam
// to the posting source — there is no second money reader.
type txnSource interface {
	transactions(ctx context.Context, org string, sandbox bool) ([]commerceTxn, error)
}

// ruleFor maps a commerce transaction to its double-entry voucher via the FIXED rule map:
//
//	deposit / topup (paid) →  Dr 1010 Square-clearing / Cr 2000 Customer Wallet
//	                          (cash arrives; the org's prepaid LIABILITY grows — NOT income)
//	deposit (grant:*)      →  Dr 5200 Promo credit     / Cr 2000 Customer Wallet
//	                          (FREE credit — no cash moved — is a promotional EXPENSE, not
//	                          a processor-cash asset; booking it to 1010 would invent money)
//	withdraw / usage       →  Dr 2000 Customer Wallet  / Cr 4000 AI usage revenue
//	                          (the revenue-RECOGNITION moment: consumed credit becomes revenue)
//	refund                 →  Dr 2000 Customer Wallet  / Cr 1000 Bank
//	                          (return of UNSPENT prepaid funds draws the liability down against
//	                          cash — NOT revenue; booking it as usage would fabricate revenue)
//
// An unknown type yields ok=false and is skipped (never a malformed posting). Amount is a
// MAGNITUDE: commerce may emit a mis-signed row (see usage.isSpend's Amount>0 guard), so a
// non-positive amount is skipped rather than abs()'d into a fabricated, possibly
// wrong-direction, entry.
func ruleFor(t commerceTxn) (Voucher, bool) {
	amount := t.Amount
	if amount <= 0 {
		return Voucher{}, false
	}
	desc := firstNonEmpty(strings.TrimSpace(t.Notes), strings.TrimSpace(t.Tags), strings.TrimSpace(t.Type))
	v := Voucher{
		SourceKind:  "commerce_txn",
		SourceID:    t.ID,
		PostingAt:   t.CreatedAt,
		Description: desc,
	}
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "deposit", "topup", "top-up", "credit", "grant":
		if isGrant(t) {
			v.Legs = []Leg{
				{Account: PromoCredit, Debit: amount},
				{Account: CustomerWallet, Credit: amount},
			}
		} else {
			v.Legs = []Leg{
				{Account: SquareClearing, Debit: amount},
				{Account: CustomerWallet, Credit: amount},
			}
		}
		return v, true
	case "withdraw", "usage", "debit", "charge":
		v.Legs = []Leg{
			{Account: CustomerWallet, Debit: amount},
			{Account: UsageRevenue, Credit: amount},
		}
		return v, true
	case "refund":
		v.Legs = []Leg{
			{Account: CustomerWallet, Debit: amount},
			{Account: Bank, Credit: amount},
		}
		return v, true
	default:
		return Voucher{}, false
	}
}

// isGrant reports whether a credit is a FREE promotional grant rather than a paid top-up.
// Grants carry a grant:* DepositKind (grant:starter / referral / affiliate / admin /
// author) in the ledger row's tag, or arrive as a "grant" type. No cash moves, so the
// offsetting debit is a promotional expense, never processor cash.
func isGrant(t commerceTxn) bool {
	return strings.EqualFold(strings.TrimSpace(t.Type), "grant") ||
		strings.Contains(strings.ToLower(t.Tags), "grant:")
}

// ingestOrg advances one org's books (a single ledger — live OR sandbox) over commerce's
// transactions since the cursor. It posts each mapped voucher idempotently, then advances
// the cursor to the newest transaction time seen. src supplies the rows; st is the org's
// store for this ledger. Returns how many NEW vouchers were posted.
func ingestOrg(ctx context.Context, src txnSource, st *store, org string, sandbox bool) (int, error) {
	txns, err := src.transactions(ctx, org, sandbox)
	if err != nil {
		return 0, err
	}
	cur, err := st.cursor(ctx)
	if err != nil {
		return 0, err
	}
	// Oldest-first so the cursor advances monotonically and postings read chronologically.
	sort.SliceStable(txns, func(i, j int) bool { return txns[i].CreatedAt < txns[j].CreatedAt })

	posted := 0
	newCur := cur
	for _, t := range txns {
		// Cursor skip is an optimization; idempotency (below) is the correctness boundary.
		// Reprocess the boundary timestamp (>=) so an equal-timestamp row is never lost.
		if cur.LastAt != "" && t.CreatedAt < cur.LastAt {
			continue
		}
		v, ok := ruleFor(t)
		if !ok {
			continue
		}
		didPost, err := st.post(ctx, v, RoundOffAllowance)
		if err != nil {
			return posted, err
		}
		if didPost {
			posted++
		}
		if t.CreatedAt > newCur.LastAt {
			newCur = cursorState{LastAt: t.CreatedAt, LastID: t.ID}
		}
	}
	if newCur != cur {
		if err := st.setCursor(ctx, newCur); err != nil {
			return posted, err
		}
	}
	return posted, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
