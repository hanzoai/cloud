package books

// bank_map.go — the ONE rule that turns a normalized BankTxn into a balanced GL voucher
// (or a clarifying question) and posts it through the spine's post() choke point. No AI, no
// guessing: a fixed classification by Direction and reconciliation state, mirroring
// ingest.go's rule map for commerce. Every posting is idempotent by (connector,
// external_id) — the bank_txn UNIQUE row and the voucher UNIQUE key both guard it — so a
// re-import or re-sync never double-books.
//
// THE FOUR CASES (see PHILOSOPHY: fail secure — an inflow we cannot explain raises a
// question, it never fabricates revenue):
//
//	OUTFLOW           → Dr 5xxx expense (categorized; default 5900 Uncategorized) / Cr 1000 Bank
//	INFLOW  matched   → Dr 1000 Bank / Cr 1010 Square-clearing   (RECONCILIATION, no revenue)
//	INFLOW  unmatched → NO voucher; raise a clarifying question   (never guess revenue)
//	TRANSFER          → NO voucher; recorded for audit            (own-account move, no P&L)

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const bankSourceKind = "bank_txn"

// bankSourceID is the voucher idempotency key for a bank transaction: connector-scoped so
// two connectors' identical external ids never collide.
func bankSourceID(bt BankTxn) string { return bt.Connector + ":" + bt.ExternalID }

// BankResult reports what mapAndPost did with one transaction — for the sync/import
// response tallies and for tests to assert the taken path.
type BankResult struct {
	Status         string `json:"status"`
	VoucherPosted  bool   `json:"voucherPosted"`
	QuestionRaised bool   `json:"questionRaised"`
	Skipped        bool   `json:"skipped"` // already processed (idempotent no-op)
}

// mapAndPost records, classifies, and posts a single BankTxn into one org's books. It is
// the shared engine's core: every connector's rows flow through here. Ordering is chosen
// for idempotent safety — classification and the (idempotent) post()/raiseQuestion run
// FIRST, then the bank_txn row is recorded with its final status; a fast pre-check skips a
// row already recorded.
func mapAndPost(ctx context.Context, st *store, bt BankTxn) (BankResult, error) {
	if err := validateTxn(bt); err != nil {
		return BankResult{}, err
	}
	// Fast idempotency: a row already recorded has already been handled.
	if status, found, err := st.bankTxnStatus(ctx, bt.Connector, bt.ExternalID); err != nil {
		return BankResult{}, err
	} else if found {
		return BankResult{Status: status, Skipped: true}, nil
	}

	raw := rawJSON(bt)

	switch bt.Direction {
	case Transfer:
		// Own-account move: recorded for audit, no P&L. With a single consolidated Bank
		// account the two legs net to zero, so there is nothing to post — recording the
		// row is the whole action.
		if err := st.insertBankTxn(ctx, bt, raw, "", statusTransfer); err != nil {
			return BankResult{}, err
		}
		return BankResult{Status: statusTransfer}, nil

	case Outflow:
		acct := categorize(bt)
		v := Voucher{
			SourceKind:  bankSourceKind,
			SourceID:    bankSourceID(bt),
			PostingAt:   bt.PostedAt,
			Description: descOf(bt),
			Legs: []Leg{
				{Account: acct, Debit: bt.AmountCents},
				{Account: Bank, Credit: bt.AmountCents},
			},
		}
		posted, err := st.post(ctx, v, RoundOffAllowance)
		if err != nil {
			return BankResult{}, err
		}
		if err := st.insertBankTxn(ctx, bt, raw, v.SourceID, statusPosted); err != nil {
			return BankResult{}, err
		}
		return BankResult{Status: statusPosted, VoucherPosted: posted}, nil

	case Inflow:
		rs, err := matchInflow(ctx, st, bt)
		if err != nil {
			return BankResult{}, err
		}
		if rs.Matched {
			// A Square-clearing settlement arriving — CLEAR 1010 → 1000. This is a
			// reconciliation move: revenue was already recognized at usage, so NO income
			// account is touched. Booking this as revenue would DOUBLE-COUNT. It clears the
			// SPECIFIC capture claimed in matchInflow (rs.CaptureID), named here for audit.
			v := Voucher{
				SourceKind:  bankSourceKind,
				SourceID:    bankSourceID(bt),
				PostingAt:   bt.PostedAt,
				Description: "bank settlement of Square capture " + rs.CaptureID + " — " + descOf(bt),
				Legs: []Leg{
					{Account: Bank, Debit: bt.AmountCents},
					{Account: SquareClearing, Credit: bt.AmountCents},
				},
			}
			posted, err := st.post(ctx, v, RoundOffAllowance)
			if err != nil {
				return BankResult{}, err
			}
			if err := st.insertBankTxn(ctx, bt, raw, v.SourceID, statusReconciled); err != nil {
				return BankResult{}, err
			}
			return BankResult{Status: statusReconciled, VoucherPosted: posted}, nil
		}
		// UNMATCHED inflow: do NOT guess revenue. Record it, raise a clarifying question.
		if err := st.insertBankTxn(ctx, bt, raw, "", statusUnmatched); err != nil {
			return BankResult{}, err
		}
		if err := st.raiseQuestion(ctx, bt.Connector, bt.ExternalID, questionPrompt(bt), bt.PostedAt); err != nil {
			return BankResult{}, err
		}
		return BankResult{Status: statusUnmatched, QuestionRaised: true}, nil

	default:
		return BankResult{}, fmt.Errorf("books bank: unknown direction %q for %s", bt.Direction, bankSourceID(bt))
	}
}

// validateTxn rejects a structurally invalid bank row before it can post. AmountCents is a
// non-negative MAGNITUDE here (the connector already resolved sign into Direction); a
// non-positive amount or a missing key is a malformed feed row, never a posting.
func validateTxn(bt BankTxn) error {
	if strings.TrimSpace(bt.Connector) == "" {
		return fmt.Errorf("books bank: empty connector")
	}
	if strings.TrimSpace(bt.ExternalID) == "" {
		return fmt.Errorf("books bank: empty external id")
	}
	if bt.AmountCents <= 0 {
		return fmt.Errorf("books bank: non-positive amount %d for %s", bt.AmountCents, bankSourceID(bt))
	}
	if bt.PostedAt == "" {
		return fmt.Errorf("books bank: empty posted_at for %s", bankSourceID(bt))
	}
	return nil
}

// categorize picks the expense account for an OUTFLOW from a small, fixed keyword map over
// merchant/description, defaulting to 5900 Uncategorized. It never invents a category: an
// unrecognized outflow lands in Uncategorized for a human to reclassify, which is the
// honest default — not a plausible-looking guess into COGS.
func categorize(bt BankTxn) string {
	hay := strings.ToLower(bt.Merchant + " " + bt.Description)
	switch {
	case containsAny(hay, "aws", "gpu", "gcp", "azure", "cloud", "lambda", "datacenter", "hosting"):
		return CloudCOGS
	case containsAny(hay, "stripe", "square", "processor", "interchange", "merchant fee"):
		return ProcessorFees
	default:
		return UncategorizedExpense
	}
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// descOf is the human description for a voucher: the merchant, else the description, else
// the connector-scoped source id — never empty.
func descOf(bt BankTxn) string {
	return firstNonEmpty(strings.TrimSpace(bt.Merchant), strings.TrimSpace(bt.Description), bankSourceID(bt))
}

// questionPrompt is the clarifying question for an unmatched inflow — it asks WHAT the
// money is rather than assuming revenue, listing the honest options.
func questionPrompt(bt BankTxn) string {
	return fmt.Sprintf(
		"Unmatched bank deposit of %s on %s (%s). It did not match a Square/commerce settlement. What is it — new revenue, an owner contribution, a loan/transfer, or a refund received?",
		dollars(bt.AmountCents), bt.PostedAt, descOf(bt))
}

// dollars renders exact int64 cents as a $-string for human-facing prompts (display only —
// the ledger always carries cents).
func dollars(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

// rawJSON is the connector's original BankTxn serialized for the bank_txn.raw audit column.
func rawJSON(bt BankTxn) string {
	b, err := json.Marshal(bt)
	if err != nil {
		return ""
	}
	return string(b)
}
