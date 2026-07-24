package books

// anomalies.go — the CLARIFYING-QUESTIONS detector: GET /v1/books/questions. It scans
// recent GL entries for UNUSUAL transactions and turns each into ONE sharp, specific
// question a founder can actually answer — never busywork. This is the "follow the money"
// prompt: instead of asking the user to review everything, the books proactively surface
// only the handful of postings that look off.
//
// WHAT COUNTS AS UNUSUAL (each detector fires on a REAL ledger signal, gated to avoid noise):
//
//	outlier   — a usage charge far above the org's own typical size (≥ outlierFactor × median
//	            and above a floor), the classic "did something run away?" question.
//	reversal  — a de-recognition (revenue debited back) or a deposit chargeback (clearing
//	            credited back): sign anomalies that usually mean a refund or correction.
//	roundoff  — a material amount plugged to the round-off account (only above a floor, so the
//	            routine 1–2¢ artifacts the posting pipeline absorbs are NEVER surfaced).
//	uncosted  — usage revenue recognized with NO matching COGS accrual, but ONLY when other
//	            usage IS costed (partial coverage). If nothing is costed yet (the live
//	            revenue-only path) this stays silent — asking about every row would be busywork.
//	overdrawn — the customer-wallet liability has gone into a debit balance: more credit was
//	            consumed than was ever deposited, which should be impossible.
//
// The detector is PURE over the GL rows (no store, no model) so it is unit-tested directly,
// and it is strictly READ-ONLY — it observes the ledger and never posts.

import (
	"net/http"
	"sort"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Detector tuning — the gates that keep questions sharp rather than noisy.
const (
	outlierFactor  = 3    // a usage ≥ 3× the median usage is an outlier
	outlierFloorC  = 5000 // …and only if it is at least $50, so tiny charges never trip it
	roundoffFloorC = 100  // only surface round-off plugs ≥ $1 (routine 1–2¢ artifacts are ignored)
	maxQuestions   = 25   // cap the prompt — sharpest (largest) first
	scanRows       = 2000 // how many recent GL rows to scan
)

// Question is one clarifying question about an unusual transaction: the sharp text, the
// formatted amount that makes it concrete, and the source/account/time that anchor it.
type Question struct {
	ID       string `json:"id"`     // the source transaction id it concerns
	Kind     string `json:"kind"`   // outlier|reversal|roundoff|uncosted|overdrawn
	Text     string `json:"text"`   // the specific question to ask the founder
	Amount   string `json:"amount"` // formatted figure ($…)
	Account  string `json:"account,omitempty"`
	PostedAt string `json:"postedAt,omitempty"`
}

// QuestionsResponse is the GET /v1/books/questions payload.
type QuestionsResponse struct {
	Questions []Question `json:"questions"`
}

// questionsHandler returns the caller's OWN org's clarifying questions from its recent GL.
func questionsHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books questions")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	rows, err := st.listGL(c.Context(), scanRows)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books gl read failed")
	}
	return booksJSON(c, QuestionsResponse{Questions: detectQuestions(rows)})
}

// voucherView is the reconstructed multi-leg voucher for one source event, grouped from the
// flat GL rows so a detector can reason about the WHOLE posting (which accounts it moved
// against), not a single leg.
type voucherView struct {
	kind     string
	id       string
	postedAt string
	legs     []Leg
}

// detectQuestions is the PURE detector: recent GL rows → the sharp questions, largest first.
// It groups rows into vouchers, runs each gated detector, and returns only real anomalies —
// an empty slice when the books look clean (no busywork). Deterministic over its input.
func detectQuestions(rows []GLRow) []Question {
	vouchers, order := groupVouchers(rows)

	// Median usage magnitude, over normal recognitions only (Cr 4000), for the outlier gate.
	var usageMags []int64
	costed := map[string]bool{}
	var walletNet int64 // Σ(debit − credit) on the Customer Wallet across all rows
	for _, r := range rows {
		if r.Account == CustomerWallet {
			walletNet += r.Debit - r.Credit
		}
	}
	for _, key := range order {
		v := vouchers[key]
		if v.kind == "cogs_accrual" {
			costed[v.id] = true
		}
		if isUsageRecognition(v) {
			usageMags = append(usageMags, magnitude(v))
		}
	}
	med := median(usageMags)
	anyCosted := len(costed) > 0

	type scored struct {
		q   Question
		amt int64
	}
	var out []scored
	seen := map[string]bool{} // dedupe by kind+id

	add := func(q Question, amt int64) {
		k := q.Kind + "/" + q.ID
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, scored{q: q, amt: amt})
	}

	for _, key := range order {
		v := vouchers[key]
		mag := magnitude(v)
		date := dateOf(v.postedAt)

		switch {
		// De-recognition: usage revenue debited back (Dr 4000). A sign anomaly — usually a
		// refund or a correction of a wrong charge.
		case legDebit(v.legs, UsageRevenue) > 0:
			add(Question{
				ID: v.id, Kind: "reversal", Account: UsageRevenue, PostedAt: date,
				Amount: formatUSD(legDebit(v.legs, UsageRevenue)),
				Text:   "Usage revenue of " + formatUSD(legDebit(v.legs, UsageRevenue)) + " was reversed on " + date + " — was this a refund, a de-recognition, or a mis-charge correction?",
			}, legDebit(v.legs, UsageRevenue))

		// Deposit chargeback: clearing credited back against the wallet (Dr 2000 / Cr 1010).
		case legDebit(v.legs, CustomerWallet) > 0 && legCredit(v.legs, SquareClearing) > 0:
			add(Question{
				ID: v.id, Kind: "reversal", Account: SquareClearing, PostedAt: date,
				Amount: formatUSD(legCredit(v.legs, SquareClearing)),
				Text:   "A deposit of " + formatUSD(legCredit(v.legs, SquareClearing)) + " was charged back on " + date + " — do you know why this payment reversed?",
			}, legCredit(v.legs, SquareClearing))

		// Outlier usage: a normal recognition far above the org's own median.
		case isUsageRecognition(v) && med > 0 && mag >= outlierFactor*med && mag >= outlierFloorC:
			add(Question{
				ID: v.id, Kind: "outlier", Account: UsageRevenue, PostedAt: date,
				Amount: formatUSD(mag),
				Text:   "A usage charge of " + formatUSD(mag) + " on " + date + " is well above your typical " + formatUSD(med) + " — is that expected, or should it be reviewed?",
			}, mag)

		// Uncosted usage — only when SOME usage is costed (partial coverage).
		case isUsageRecognition(v) && anyCosted && !costed[v.id]:
			add(Question{
				ID: v.id, Kind: "uncosted", Account: CloudCOGS, PostedAt: date,
				Amount: formatUSD(mag),
				Text:   "The " + formatUSD(mag) + " usage on " + date + " has no matching infra cost recorded, though other usage does — should COGS be accrued for it?",
			}, mag)
		}

		// Round-off plug ≥ floor — an "uncategorized" signal beyond a routine cent artifact.
		if ro := legDebit(v.legs, RoundOff) + legCredit(v.legs, RoundOff); ro >= roundoffFloorC {
			add(Question{
				ID: v.id, Kind: "roundoff", Account: RoundOff, PostedAt: date,
				Amount: formatUSD(ro),
				Text:   formatUSD(ro) + " was booked to round-off on " + date + " — was there a rounding split, or is a real imbalance being masked?",
			}, ro)
		}
	}

	// Overdrawn wallet — one ledger-wide question, not per-voucher.
	if walletNet > 0 {
		add(Question{
			ID: "wallet", Kind: "overdrawn", Account: CustomerWallet,
			Amount: formatUSD(walletNet),
			Text:   "The customer wallet shows a " + formatUSD(walletNet) + " debit balance — more credit was consumed than deposited, which should be impossible. Investigate.",
		}, walletNet)
	}

	// Sharpest (largest amount) first, then deterministically by id/kind for ties.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].amt != out[j].amt {
			return out[i].amt > out[j].amt
		}
		if out[i].q.ID != out[j].q.ID {
			return out[i].q.ID < out[j].q.ID
		}
		return out[i].q.Kind < out[j].q.Kind
	})

	qs := make([]Question, 0, len(out))
	for i, sc := range out {
		if i >= maxQuestions {
			break
		}
		qs = append(qs, sc.q)
	}
	return qs
}

// groupVouchers reconstructs vouchers from flat GL rows, keyed (source_kind, source_id),
// preserving first-seen order so the detector output is deterministic.
func groupVouchers(rows []GLRow) (map[string]*voucherView, []string) {
	vouchers := map[string]*voucherView{}
	var order []string
	for _, r := range rows {
		key := r.SourceKind + "/" + r.SourceID
		v, ok := vouchers[key]
		if !ok {
			v = &voucherView{kind: r.SourceKind, id: r.SourceID, postedAt: r.PostingAt}
			vouchers[key] = v
			order = append(order, key)
		}
		v.legs = append(v.legs, Leg{Account: r.Account, Debit: r.Debit, Credit: r.Credit})
	}
	return vouchers, order
}

// isUsageRecognition reports whether a voucher is a normal usage-revenue recognition
// (Cr 4000 against the wallet) — the population the outlier median is built from.
func isUsageRecognition(v *voucherView) bool {
	return v.kind == "commerce_txn" && legCredit(v.legs, UsageRevenue) > 0
}

// magnitude is a voucher's size in cents: the sum of its debit legs (== the credit legs, as
// the voucher balances).
func magnitude(v *voucherView) int64 {
	var d int64
	for _, l := range v.legs {
		d += l.Debit
	}
	return d
}

// median is the middle value of a copy-sorted slice (0 on empty), used for the outlier gate.
func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// legDebit / legCredit read the debit / credit posted to an account within a voucher body —
// the ONE way both the detector and the tests inspect a single side of a multi-leg posting.
func legDebit(legs []Leg, account string) int64 {
	for _, l := range legs {
		if l.Account == account {
			return l.Debit
		}
	}
	return 0
}

func legCredit(legs []Leg, account string) int64 {
	for _, l := range legs {
		if l.Account == account {
			return l.Credit
		}
	}
	return 0
}

// dateOf trims an RFC3339 posting time to its calendar date for a readable question.
func dateOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}
