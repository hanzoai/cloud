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

// kind is the ONE accounting event a commerce transaction maps to. classify is the single
// classifier both ruleFor (revenue posting) and cogsVoucher (cost accrual) read, so the
// type vocabulary lives in exactly one place.
type kind int

const (
	kindNone kind = iota
	kindDeposit
	kindGrant
	kindUsage
	kindRefund
)

func classify(t commerceTxn) kind {
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "deposit", "topup", "top-up", "credit", "grant":
		if isGrant(t) {
			return kindGrant
		}
		return kindDeposit
	case "withdraw", "usage", "debit", "charge":
		return kindUsage
	case "refund":
		return kindRefund
	}
	return kindNone
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
// SIGN IS MEANING, not noise. A commerce amount is a signed cents figure: a POSITIVE amount
// is the event above; a NEGATIVE amount is its REVERSAL (a chargeback of a top-up, a
// de-recognition of usage, a clawed-back grant) and books the EXACT OPPOSITE legs — a
// negative deposit is Dr 2000 Customer Wallet / Cr 1010 Square-clearing, a negative usage
// is Dr 4000 Usage revenue / Cr 2000 Customer Wallet. Coercing the sign away with abs()
// would silently drop a real reversal and leave the wallet overstated, so the magnitude is
// booked on the flipped side instead. Only a ZERO amount (no accounting weight) and an
// unknown type are skipped.
func ruleFor(t commerceTxn) (Voucher, bool) {
	if t.Amount == 0 {
		return Voucher{}, false
	}
	k := classify(t)
	if k == kindNone {
		return Voucher{}, false
	}
	reverse := t.Amount < 0
	mag := abs64(t.Amount)
	v := Voucher{
		SourceKind:  "commerce_txn",
		SourceID:    t.ID,
		PostingAt:   t.CreatedAt,
		Description: firstNonEmpty(strings.TrimSpace(t.Notes), strings.TrimSpace(t.Tags), strings.TrimSpace(t.Type)),
	}
	switch k {
	case kindDeposit:
		v.Legs = entry(reverse, SquareClearing, CustomerWallet, mag)
	case kindGrant:
		v.Legs = entry(reverse, PromoCredit, CustomerWallet, mag)
	case kindUsage:
		v.Legs = entry(reverse, CustomerWallet, UsageRevenue, mag)
	case kindRefund:
		v.Legs = entry(reverse, CustomerWallet, Bank, mag)
	}
	return v, true
}

// entry builds the two-leg body Dr `dr` / Cr `cr` for `amount` cents, swapping the two
// sides when `reverse` is set so a negative-amount transaction books the exact opposite of
// the event it reverses. This is the ONE place sign becomes debit/credit direction.
func entry(reverse bool, dr, cr string, amount int64) []Leg {
	if reverse {
		dr, cr = cr, dr
	}
	return []Leg{{Account: dr, Debit: amount}, {Account: cr, Credit: amount}}
}

// costSource is the measurement seam for COST-OF-GOODS matching: given a recognized usage
// transaction it reports the matching cloud/GPU cost in cents and whether a REAL figure
// exists (ok). The production projection reads the ai-owned cloud_usage ledger — the same
// hanzo.cloud_usage cost_cents column the admin compute/o11y surfaces aggregate
// (clients/admin/compute.go, clients/admin/o11y.go) — per org, per usage window. Until that
// projection is wired the default noCost returns ok=false, so the LIVE path books
// revenue-only and never invents a cost number. Tests inject a fake with real figures.
type costSource interface {
	usageCost(ctx context.Context, org string, sandbox bool, t commerceTxn) (cents int64, ok bool)
}

// noCost is the default cost seam: NO real figure, so nothing is accrued. Wiring the
// hanzo.cloud_usage cost_cents projection here (per-org, matched to the usage window) is
// the followup that makes COGS — and thus a real gross-margin P&L — book in production.
type noCost struct{}

func (noCost) usageCost(context.Context, string, bool, commerceTxn) (int64, bool) { return 0, false }

// cogsVoucher accrues the cloud/GPU cost of goods MATCHING a recognized usage revenue:
// Dr 5000 Cloud COGS / Cr 2300 Accrued infra payable, for the `cents` the cost seam
// reports. It is a SEPARATE voucher (keyed cogs_accrual/txn, never commerce_txn) so revenue
// posting and cost accrual stay orthogonal and independently idempotent — commerce
// transactions remain the SOLE revenue source and are never double-counted. Only a usage
// recognition accrues COGS; a negative (de-recognition) reverses the accrual, keeping cost
// matched to revenue. A non-positive cost books nothing (the gate the seam's ok already
// enforces, belt-and-braces so no zero-cost voucher is ever emitted).
func cogsVoucher(t commerceTxn, cents int64) (Voucher, bool) {
	if classify(t) != kindUsage || cents <= 0 {
		return Voucher{}, false
	}
	return Voucher{
		SourceKind:  "cogs_accrual",
		SourceID:    t.ID,
		PostingAt:   t.CreatedAt,
		Description: "cloud/GPU COGS accrual",
		Legs:        entry(t.Amount < 0, CloudCOGS, AccruedInfra, cents),
	}, true
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
func ingestOrg(ctx context.Context, src txnSource, cost costSource, st *store, org string, sandbox bool) (int, error) {
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
		// COGS accrual runs OUTSIDE the revenue cursor, over EVERY usage row in the window.
		// The accrual is keyed (cogs_accrual, txn.ID) and idempotency is its correctness
		// boundary — never the revenue cursor — so the cost pass does NOT skip on cur.LastAt.
		// This is what lets the cost seam BACKFILL: when the cloud_usage cost projection is
		// wired AFTER the revenue cursor has already advanced past a usage txn, the next sync
		// re-scans that txn and books its matching COGS voucher (posting it exactly once).
		// Coupling this to the revenue cursor would strand every pre-wiring period as
		// revenue-only, overstating gross margin forever. Gated on the seam's ok, so the live
		// noCost path still books nothing.
		if cost != nil {
			if cents, has := cost.usageCost(ctx, org, sandbox, t); has {
				if cv, ok := cogsVoucher(t, cents); ok {
					didCOGS, err := st.post(ctx, cv, RoundOffAllowance)
					if err != nil {
						return posted, err
					}
					if didCOGS {
						posted++
					}
				}
			}
		}
		// Revenue posting: the cursor skip is an OPTIMIZATION (it caps re-scan); idempotency
		// (source_kind, source_id) is the correctness boundary. Reprocess the boundary
		// timestamp (>=) so an equal-timestamp row is never lost.
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
