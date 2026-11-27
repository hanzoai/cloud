package books

// import_pdf.go — the PDF bank-statement parser. Bank of America eStatements are PDF (not
// OFX/QFX/CSV), so this is the format that lets a REAL statement — the one a human downloads
// from online banking — book directly. It reuses the SAME normalization every connector
// shares (parseCents → int64 cents, directed → Direction + magnitude): this file only turns a
// page of drawn text into the canonical BankTxn, then GUARDS it.
//
// TEXT, then ROWS. internal/pdftext reads the PDF in pure Go (no poppler, no external binary — it works unchanged
// in the deploy image). It yields per-fragment text with X/Y positions, not lines, so
// pdfLines reconstructs the printed layout: fragments are grouped into rows by Y proximity
// (top-of-page first, since PDF Y increases upward) and ordered within a row by X. A summary
// or detail row — "MM/DD/YY  <description>  <-?$?N,NNN.NN>" — is recoverable from that.
//
// THE FOOT-CHECK IS THE ACCURACY GUARANTEE. Extraction is best-effort and OCR-adjacent; a
// mis-read amount must never silently mis-book real money. So parseBoAStatement does not trust
// what it parsed: it re-derives the statement's own arithmetic and REFUSES the whole file if
// any total disagrees. Each section is reconciled by its SIGNED row total against the section's
// stated total with the sign FORCED by the section's side (credits positive, debits negative),
// so a debit row whose leading minus is missing — which would otherwise book as money IN while
// its magnitude still adds up — fails to reconcile. Specifically it asserts (exact int64 cents):
//   - signedSum(deposit rows)     == +the summary "Deposits and other credits" total
//   - signedSum(withdrawal rows)  == −the summary "Withdrawals and other debits" total
//   - signedSum(service-fee rows) == −the summary "Service fees" total (when that section prints)
//   - signedSum(check rows)       == −the summary "Checks" total; and a Checks total that prints
//     with NO parsed check rows is refused, never imported with the check debits invisible
//   - beginning + deposits − withdrawals − fees − checks == ending  (signs preserved: an
//     overdrawn account has NEGATIVE beginning/ending balances)
// If ALL reconcile, the rows are safe to book. If ANY mismatch, parsePDF returns an error and
// books NOTHING — a rejected garbage import is correct; a mis-booked one is not.
//
// MONEY is exact int64 cents end to end (parseCents, never float64). Summary magnitudes are
// taken as the FIRST amount on their line — a Service-fees line also prints an unrelated
// "Average ledger balance" figure, and the first amount is the fees value.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/pdftext"
)

// looksPDF reports whether the body is a PDF.
func looksPDF(data []byte) bool { return pdftext.Looks(data) }

// parsePDF extracts the printed text of a PDF bank statement, reconstructs its rows, and runs
// the proven Bank of America parse-and-reconcile. It returns booked BankTxns only when the
// statement's own totals reconcile to the penny; otherwise it returns an error and no rows.
func parsePDF(data []byte) ([]BankTxn, error) {
	lines, err := pdfLines(data)
	if err != nil {
		return nil, err
	}
	for i := range lines {
		lines[i] = normalizeSigns(lines[i])
	}
	return parseBoAStatement(lines)
}

// parenNeg matches a parenthesized money magnitude, BoA's print form for a
// negative — "($1,234.56)" — which it rewrites to "-$1,234.56". An optional inner
// '-' is absorbed so "(-$12.00)" (a paren wrapping an already-minus value, e.g.
// after U+2212 normalization) collapses to a single leading minus, not "(-…)".
var parenNeg = regexp.MustCompile(`\(-?(\$?[\d,]+\.\d{2})\)`)

// normalizeSigns canonicalizes the two ways a statement prints a minus that the
// ASCII-only amount parser would otherwise miss — dropping the sign and making an
// overdrawn balance (negative beginning/ending) or a reversal row fail to
// reconcile and false-refuse the whole statement. It maps the Unicode minus
// (U+2212), often emitted by PDF text extraction, to ASCII '-', and rewrites
// parenthesized negatives to a leading '-'. Idempotent and amount-shaped only, so
// ordinary parentheses in a description are untouched.
func normalizeSigns(line string) string {
	line = strings.ReplaceAll(line, "−", "-")
	return parenNeg.ReplaceAllString(line, "-$1")
}

// pdfLines renders a PDF into layout-ordered text lines (internal/pdftext). A
// bank statement is a few pages, so the bound is 100 pages and 20000 lines.
func pdfLines(data []byte) ([]string, error) {
	lines, err := pdftext.Lines(data, 100, 20000)
	if err != nil {
		return nil, fmt.Errorf("books import: %w", err)
	}
	return lines, nil
}

// summary is the Account Summary block of a BoA statement, in exact signed cents. beginning
// and ending PRESERVE sign (an overdrawn account is negative). deposits/withdrawals/fees/checks
// are magnitudes — the account-summary line prints them as the debit/credit totals and the
// balance identity subtracts the debit-side ones.
type summary struct {
	beginning, ending             int64
	deposits, withdrawals         int64
	fees, checks                  int64
	feesSeen                      bool
	haveBeginning, haveEnding     bool
	haveDeposits, haveWithdrawals bool
}

var (
	amountRe  = regexp.MustCompile(`-?\$?\d{1,3}(?:,\d{3}){0,5}(?:\.\d{2})|-?\$?\d{1,15}\.\d{2}`)
	rowDateRe = regexp.MustCompile(`^(\d{2}/\d{2}/\d{2})\b`)
)

// parseBoAStatement is the proven algorithm, isolated from PDF extraction so it can be
// unit-tested directly on layout text. It reads the Account Summary block, parses each detail
// section's rows into BankTxns, and REFUSES the whole statement (returns an error, no rows)
// unless every stated total reconciles with what was parsed. Callers get all-or-nothing:
// either a fully self-consistent set of transactions, or an error.
func parseBoAStatement(lines []string) ([]BankTxn, error) {
	sum, sumEnd, err := readSummary(lines)
	if err != nil {
		return nil, err
	}

	var deposits, withdrawals, fees, checks []BankTxn
	section := "" // "deposits" | "withdrawals" | "fees" | "checks" | ""
	for _, line := range lines[sumEnd:] {
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, "total "):
			// closes any detail section ("Total deposits...", "Total checks", etc.)
			section = ""
		case strings.Contains(low, "deposits and other credits"):
			section = "deposits"
		case strings.Contains(low, "withdrawals and other debits"):
			section = "withdrawals" // "- continued" page headers re-enter the same section
		case strings.Contains(low, "service fees"):
			section = "fees"
		case strings.HasPrefix(low, "checks"):
			section = "checks"
		case section != "" && rowDateRe.MatchString(line):
			var seq int
			switch section {
			case "deposits":
				seq = len(deposits)
			case "withdrawals":
				seq = len(withdrawals)
			case "fees":
				seq = len(fees)
			case "checks":
				seq = len(checks)
			}
			bt, err := detailRow(line, section, seq)
			if err != nil {
				return nil, err
			}
			switch section {
			case "deposits":
				deposits = append(deposits, bt)
			case "withdrawals":
				withdrawals = append(withdrawals, bt)
			case "fees":
				fees = append(fees, bt)
			case "checks":
				checks = append(checks, bt)
			}
		}
	}

	if err := footCheck(sum, deposits, withdrawals, fees, checks); err != nil {
		return nil, err
	}

	out := make([]BankTxn, 0, len(deposits)+len(withdrawals)+len(fees)+len(checks))
	out = append(out, deposits...)
	out = append(out, withdrawals...)
	out = append(out, fees...)
	out = append(out, checks...)
	if len(out) == 0 {
		return nil, fmt.Errorf("books import: PDF statement has no transaction rows")
	}
	return out, nil
}

// readSummary parses the Account Summary block and returns it plus the index one past the
// "Ending balance" line, where the transaction detail begins. The summary is bounded by the
// Beginning- and Ending-balance lines so the same labels reused as detail-section headers
// later are never mistaken for summary values. Each value is the FIRST amount on its line.
func readSummary(lines []string) (summary, int, error) {
	var s summary
	end := -1
	for i, line := range lines {
		low := strings.ToLower(line)
		switch {
		case !s.haveBeginning && strings.Contains(low, "beginning balance"):
			if v, ok := firstAmount(line); ok {
				s.beginning, s.haveBeginning = v, true
			}
		case strings.Contains(low, "ending balance"):
			if v, ok := firstAmount(line); ok {
				s.ending, s.haveEnding, end = v, true, i
			}
		case !s.haveDeposits && strings.Contains(low, "deposits and other credits"):
			if v, ok := firstAmount(line); ok {
				s.deposits, s.haveDeposits = abs(v), true
			}
		case !s.haveWithdrawals && strings.Contains(low, "withdrawals and other debits"):
			if v, ok := firstAmount(line); ok {
				s.withdrawals, s.haveWithdrawals = abs(v), true
			}
		case !s.feesSeen && strings.HasPrefix(low, "service fees"):
			if v, ok := firstAmount(line); ok {
				s.fees, s.feesSeen = abs(v), true
			}
		case strings.HasPrefix(low, "checks"):
			if v, ok := firstAmount(line); ok {
				s.checks = abs(v)
			}
		}
		if s.haveEnding {
			break
		}
	}
	if !s.haveBeginning || !s.haveEnding {
		return summary{}, 0, fmt.Errorf("books import: PDF is not a recognizable statement (no Beginning/Ending balance)")
	}
	if !s.haveDeposits || !s.haveWithdrawals {
		return summary{}, 0, fmt.Errorf("books import: statement summary missing deposits/withdrawals totals")
	}
	return s, end + 1, nil
}

// detailRow parses one "MM/DD/YY  <description>  <amount>" line into a BankTxn. The amount is
// the LAST amount on the line (a description may itself contain digits, but the money column
// is rightmost). Its sign resolves Direction via directed — but the SECTION is authoritative:
// footCheck reconciles each section's SIGNED row total against the section's stated total, so a
// row whose printed sign disagrees with its section's convention (a debit missing its leading
// minus) breaks that section's reconciliation and the whole import is refused rather than
// booked with a flipped direction. ExternalID is a deterministic content hash keyed by the
// row's position within its section (seq), so two genuinely identical same-day rows (two equal
// fees) get distinct ids and both book, while re-importing the same statement reproduces the
// same ids and the (connector, external_id) guard makes it a no-op.
func detailRow(line, section string, seq int) (BankTxn, error) {
	m := rowDateRe.FindStringSubmatch(line)
	postedAt, err := pdfDate(m[1])
	if err != nil {
		return BankTxn{}, err
	}
	amts := amountRe.FindAllString(line, -1)
	if len(amts) == 0 {
		return BankTxn{}, fmt.Errorf("books import: no amount in row %q", line)
	}
	amtRaw := amts[len(amts)-1]
	signed, err := parseCents(amtRaw)
	if err != nil {
		return BankTxn{}, fmt.Errorf("books import: row %q: %w", line, err)
	}
	if signed == 0 {
		return BankTxn{}, fmt.Errorf("books import: zero-amount row %q", line)
	}

	desc := strings.TrimSpace(line[len(m[1]):])
	desc = strings.TrimSpace(strings.TrimSuffix(desc, amtRaw))
	dir, cents := directed(signed)
	return BankTxn{
		Connector:   "import",
		ExternalID:  pdfExternalID(m[1], amtRaw, desc, section, seq),
		PostedAt:    postedAt,
		AmountCents: cents,
		Currency:    "usd",
		Description: desc,
		Merchant:    desc,
		Direction:   dir,
	}, nil
}

// footCheck is the accuracy guarantee: it re-derives the statement's arithmetic from the
// parsed rows and refuses the whole import if anything disagrees. Each section is reconciled by
// its SIGNED total against the section's stated total with the sign FORCED by the section
// (credits positive, debits negative), so a debit section whose rows print without their
// leading minus — booking as inflows while their magnitudes still add up — fails to reconcile
// instead of silently flipping money IN. A Checks total that prints with no parsed check rows
// is likewise refused (real debits must never import invisibly). Finally the signed balance
// identity must close exactly.
func footCheck(s summary, deposits, withdrawals, fees, checks []BankTxn) error {
	if got := signedSum(deposits); got != s.deposits {
		return fmt.Errorf("books import: deposits do not reconcile: rows signed sum %d cents, statement total %d cents", got, s.deposits)
	}
	if got := signedSum(withdrawals); got != -s.withdrawals {
		return fmt.Errorf("books import: withdrawals do not reconcile: rows signed sum %d cents, statement total %d cents", got, -s.withdrawals)
	}
	if s.feesSeen {
		if got := signedSum(fees); got != -s.fees {
			return fmt.Errorf("books import: service fees do not reconcile: rows signed sum %d cents, statement total %d cents", got, -s.fees)
		}
	}
	if s.checks != 0 && len(checks) == 0 {
		return fmt.Errorf("books import: statement states a Checks total of %d cents but parsed no check rows; refusing rather than under-book", s.checks)
	}
	if len(checks) > 0 {
		if got := signedSum(checks); got != -s.checks {
			return fmt.Errorf("books import: checks do not reconcile: rows signed sum %d cents, statement total %d cents", got, -s.checks)
		}
	}
	if got := s.beginning + s.deposits - s.withdrawals - s.fees - s.checks; got != s.ending {
		return fmt.Errorf("books import: balance does not reconcile: begin %d + dep %d - wd %d - fees %d - checks %d = %d cents, stated ending %d cents",
			s.beginning, s.deposits, s.withdrawals, s.fees, s.checks, got, s.ending)
	}
	return nil
}

// signedSum totals a slice of BankTxns as SIGNED cents (Inflow positive, Outflow negative). The
// footCheck compares it against a section total whose sign is forced by the section's side
// (credits positive, debits negative), so a row carrying the wrong sign convention cannot hide.
func signedSum(txns []BankTxn) int64 {
	var n int64
	for _, bt := range txns {
		if bt.Direction == Outflow {
			n -= bt.AmountCents
		} else {
			n += bt.AmountCents
		}
	}
	return n
}

// firstAmount returns the first money figure on a line as signed cents. On a Service-fees line
// this is the fees value (an unrelated "Average ledger balance" prints later on the same line).
func firstAmount(line string) (int64, bool) {
	raw := amountRe.FindString(line)
	if raw == "" {
		return 0, false
	}
	v, err := parseCents(raw)
	if err != nil {
		return 0, false
	}
	return v, true
}

// pdfDate parses a statement row's "MM/DD/YY" into RFC3339 UTC midnight (the reconciler matches
// on a day window, so time-of-day is irrelevant). Go maps a two-digit year 00–68 to 20xx.
func pdfDate(s string) (string, error) {
	t, err := time.Parse("01/02/06", strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("books import: bad PDF date %q: %w", s, err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// pdfExternalID is the deterministic idempotency key for a PDF row (no bank FITID exists): a
// content hash over the fields that make the row unique within its section, including seq — the
// row's position within its section. seq disambiguates two genuinely identical same-day rows
// (two equal fees), which a PDF has no running-balance column to tell apart, so both book
// instead of one silently colliding away under the (connector, external_id) guard.
func pdfExternalID(date, amount, desc, section string, seq int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d", section, date, amount, desc, seq)))
	return "boa-pdf:" + hex.EncodeToString(sum[:])[:24]
}

// abs is the int64 absolute value used to compare signed cents against non-negative totals.
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
