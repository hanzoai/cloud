package books

import (
	"strings"
	"testing"
)

// boaPDFLines is a synthetic Bank of America statement in the layout form pdfLines produces
// (and pdftotext -layout would print): an Account Summary block followed by three detail
// sections. It RECONCILES — every summary total equals the sum of its section's rows and the
// balance identity closes:
//
//	begin 1,000.00 + deposits 1,203.50 - withdrawals 71.20 - fees 12.00 - checks 0.00 = ending 2,120.30
//
// The Service-fees SUMMARY line deliberately also prints an unrelated "Average ledger balance"
// figure to prove firstAmount takes the fees value (the first amount), not the ledger balance.
var boaPDFLines = []string{
	"Bank of America Business Advantage",
	"Account summary",
	"Beginning balance on July 1, 2026            $1,000.00",
	"Deposits and other credits                    $1,203.50",
	"Withdrawals and other debits                    -$71.20",
	"Checks                                           -$0.00",
	"Service fees                                    -$12.00   Average ledger balance   $842.15",
	"Ending balance on July 31, 2026              $2,120.30",
	"",
	"Deposits and other credits",
	"Date       Description                                 Amount",
	"07/06/26   SQUARE INC DES:DEPOSIT                     1,203.50",
	"Total deposits and other credits               $1,203.50",
	"",
	"Withdrawals and other debits",
	"Date       Description                                 Amount",
	"07/05/26   CHECKCARD 0705 AWS CLOUD DES:AWS SVC         -71.20",
	"Total withdrawals and other debits               -$71.20",
	"",
	"Service fees",
	"Date       Description                                 Amount",
	"07/31/26   MONTHLY MAINTENANCE FEE                      -12.00",
	"Total service fees                               -$12.00",
}

// TestPDFStatementReconciles proves a well-formed statement parses to exact-cent BankTxns with
// the right Direction, dates, and descriptions, and passes the foot-check.
func TestPDFStatementReconciles(t *testing.T) {
	txns, err := parseBoAStatement(boaPDFLines)
	if err != nil {
		t.Fatalf("reconciling statement must parse, got error: %v", err)
	}
	if len(txns) != 3 {
		t.Fatalf("want 3 txns, got %d: %+v", len(txns), txns)
	}

	var dep, wd, fee BankTxn
	var seen int
	for _, bt := range txns {
		switch bt.AmountCents {
		case 120350:
			dep, seen = bt, seen+1
		case 7120:
			wd, seen = bt, seen+1
		case 1200:
			fee, seen = bt, seen+1
		}
	}
	if seen != 3 {
		t.Fatalf("expected the three known amounts, got %+v", txns)
	}

	if dep.Direction != Inflow || dep.PostedAt != "2026-07-06T00:00:00Z" {
		t.Fatalf("deposit must be Inflow on 2026-07-06, got %s %s", dep.Direction, dep.PostedAt)
	}
	if !strings.Contains(dep.Description, "SQUARE") {
		t.Fatalf("deposit description lost: %q", dep.Description)
	}
	if wd.Direction != Outflow || wd.PostedAt != "2026-07-05T00:00:00Z" {
		t.Fatalf("withdrawal must be Outflow on 2026-07-05, got %s %s", wd.Direction, wd.PostedAt)
	}
	if !strings.Contains(wd.Description, "AWS") {
		t.Fatalf("withdrawal description lost: %q", wd.Description)
	}
	if fee.Direction != Outflow || fee.PostedAt != "2026-07-31T00:00:00Z" {
		t.Fatalf("fee must be Outflow on 2026-07-31, got %s %s", fee.Direction, fee.PostedAt)
	}

	for _, bt := range txns {
		if bt.Connector != "import" || bt.Currency != "usd" || bt.ExternalID == "" {
			t.Fatalf("every row must be import/usd with a stable id, got %+v", bt)
		}
	}

	// Deterministic ids: re-parsing the same statement reproduces the same external ids, so a
	// re-upload is an idempotent no-op through the (connector, external_id) guard.
	again, err := parseBoAStatement(boaPDFLines)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	for i := range txns {
		if again[i].ExternalID != txns[i].ExternalID {
			t.Fatalf("external ids must be deterministic, %q != %q", again[i].ExternalID, txns[i].ExternalID)
		}
	}
}

// replaceLine returns a copy of the fixture with the row containing sub rewritten to repl —
// used to introduce a single mis-parse and prove the foot-check catches it.
func replaceLine(lines []string, sub, repl string) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	for i, l := range out {
		if strings.Contains(l, sub) {
			out[i] = repl
			return out
		}
	}
	return out
}

// TestPDFStatementRefusesOnMismatch is the ACCURACY GUARANTEE. Two independent corruptions —
// a detail row that no longer sums to its section total, and a balance identity that no longer
// closes — must each cause a REFUSAL: an error and ZERO booked transactions. Booking nothing on
// a statement that does not reconcile is correct; mis-booking real money is not.
func TestPDFStatementRefusesOnMismatch(t *testing.T) {
	// Corruption 1: a withdrawal row misread as -81.20 while the summary total stays -71.20.
	// Section sum (81.20) no longer equals the stated withdrawals total (71.20).
	badRow := replaceLine(boaPDFLines,
		"07/05/26   CHECKCARD",
		"07/05/26   CHECKCARD 0705 AWS CLOUD DES:AWS SVC         -81.20")
	if txns, err := parseBoAStatement(badRow); err == nil || len(txns) != 0 {
		t.Fatalf("mis-summed section must REFUSE: got %d txns, err=%v", len(txns), err)
	}

	// Corruption 2: rows all sum correctly, but the stated ending balance is wrong, so the
	// signed balance identity begin+dep-wd-fees-checks == ending no longer closes.
	badBalance := replaceLine(boaPDFLines,
		"Ending balance",
		"Ending balance on July 31, 2026              $9,999.99")
	if txns, err := parseBoAStatement(badBalance); err == nil || len(txns) != 0 {
		t.Fatalf("broken balance identity must REFUSE: got %d txns, err=%v", len(txns), err)
	}
}

// TestPDFDebitMissingMinusRefused is the whole-section sign-flip guard (finding 1). A
// withdrawals row printed as a bare positive — a BoA template that drops the leading minus on
// its debit column — has the RIGHT magnitude but the WRONG side: on the abs-magnitude foot-check
// it would book as money IN (Inflow) while still reconciling. The SIGNED section foot-check
// (signedSum(withdrawals) must equal −stated total) rejects it: nothing books. The balance
// identity alone would NOT catch this (it uses the summary magnitude, which is correct), so this
// proves the section-signed reconciliation is doing the work.
func TestPDFDebitMissingMinusRefused(t *testing.T) {
	lines := []string{
		"Account summary",
		"Beginning balance                              $1,000.00",
		"Deposits and other credits                         $0.00",
		"Withdrawals and other debits                     -$500.00",
		"Ending balance                                   $500.00",
		"",
		"Withdrawals and other debits",
		"Date       Description                          Amount",
		"07/05/26   OUTGOING WIRE                          500.00", // minus dropped
		"Total withdrawals and other debits             -$500.00",
	}
	if txns, err := parseBoAStatement(lines); err == nil || len(txns) != 0 {
		t.Fatalf("a debit row missing its minus must REFUSE, got %d txns err=%v", len(txns), err)
	}
}

// TestPDFChecksTotalNoRowsRefused is the invisible-debits guard (finding 2). A statement whose
// summary carries a Checks total but whose Checks detail section is not parsed would, under the
// old code, import the deposit and silently book ZERO check debits while the balance identity
// (which uses the summary Checks magnitude) still closes. That is real money leaving invisibly.
// It must REFUSE.
func TestPDFChecksTotalNoRowsRefused(t *testing.T) {
	lines := []string{
		"Account summary",
		"Beginning balance                                  $0.00",
		"Deposits and other credits                        $600.00",
		"Withdrawals and other debits                        $0.00",
		"Checks                                            -$500.00",
		"Ending balance                                     $100.00",
		"",
		"Deposits and other credits",
		"Date       Description                          Amount",
		"07/06/26   INCOMING PAYMENT                       600.00",
		"Total deposits and other credits                 $600.00",
	}
	if txns, err := parseBoAStatement(lines); err == nil || len(txns) != 0 {
		t.Fatalf("a Checks total with no parsed check rows must REFUSE, got %d txns err=%v", len(txns), err)
	}
}

// TestPDFChecksSectionBooks proves the Checks detail section parses into Outflow BankTxns and
// reconciles against the summary Checks total (finding 2), so check debits book instead of
// vanishing.
func TestPDFChecksSectionBooks(t *testing.T) {
	lines := []string{
		"Account summary",
		"Beginning balance                                  $0.00",
		"Deposits and other credits                        $600.00",
		"Withdrawals and other debits                        $0.00",
		"Checks                                            -$500.00",
		"Ending balance                                     $100.00",
		"",
		"Deposits and other credits",
		"Date       Description                          Amount",
		"07/06/26   INCOMING PAYMENT                       600.00",
		"Total deposits and other credits                 $600.00",
		"",
		"Checks",
		"Date       Description                          Amount",
		"07/10/26   CHECK 1001                            -300.00",
		"07/12/26   CHECK 1002                            -200.00",
		"Total checks                                     -$500.00",
	}
	txns, err := parseBoAStatement(lines)
	if err != nil {
		t.Fatalf("checks section that reconciles must parse: %v", err)
	}
	var checks int
	for _, bt := range txns {
		if bt.AmountCents == 30000 || bt.AmountCents == 20000 {
			if bt.Direction != Outflow {
				t.Fatalf("check row must be Outflow, got %s", bt.Direction)
			}
			checks++
		}
	}
	if checks != 2 {
		t.Fatalf("both check debits must book, got %d of 2 in %+v", checks, txns)
	}
}

// TestPDFDuplicateSameDayRowsBothBook is the duplicate-collision guard (finding 3). Two
// genuinely identical same-day rows (two equal monthly fees) — which a PDF has no running-balance
// column to distinguish — must receive DISTINCT external ids from the per-section sequence index,
// so the (connector, external_id) guard books both instead of silently dropping one.
func TestPDFDuplicateSameDayRowsBothBook(t *testing.T) {
	lines := []string{
		"Account summary",
		"Beginning balance                              $1,000.00",
		"Deposits and other credits                         $0.00",
		"Withdrawals and other debits                        $0.00",
		"Service fees                                      -$24.00",
		"Ending balance                                    $976.00",
		"",
		"Service fees",
		"Date       Description                          Amount",
		"07/31/26   MONTHLY MAINTENANCE FEE                -12.00",
		"07/31/26   MONTHLY MAINTENANCE FEE                -12.00",
		"Total service fees                               -$24.00",
	}
	txns, err := parseBoAStatement(lines)
	if err != nil {
		t.Fatalf("two identical fees must parse: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("both identical fees must book, got %d: %+v", len(txns), txns)
	}
	if txns[0].ExternalID == txns[1].ExternalID {
		t.Fatalf("identical same-day rows must get distinct external ids, both %q", txns[0].ExternalID)
	}
}

// TestLooksPDF proves the magic-byte detector routes only real PDFs to the PDF parser, leaving
// the OFX and CSV text formats to their own parsers.
func TestLooksPDF(t *testing.T) {
	if !looksPDF([]byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")) {
		t.Fatalf("a %%PDF- header must be detected as PDF")
	}
	if looksPDF([]byte(ofxSGML)) || looksPDF([]byte(boaCSV)) {
		t.Fatalf("OFX/CSV bodies must not be misdetected as PDF")
	}
}

// TestPDFOverdrawnUnicodeAndParenReconciles proves the sign-normalization fix: an
// OVERDRAWN statement whose negatives print as the Unicode minus (U+2212) and as
// parenthesized magnitudes "($x)" — the real Bank of America print forms an
// ASCII-only parser would drop — reconciles instead of false-refusing. This is the
// exact class of a real overdrawn business account (negative beginning + ending).
func TestPDFOverdrawnUnicodeAndParenReconciles(t *testing.T) {
	// begin −335.90 + deposits 500.00 − withdrawals 800.00 − fees 0 = −635.90 ending.
	lines := []string{
		"Account summary",
		"Beginning balance on July 1, 2026            (−$335.90)", // U+2212 inside parens
		"Deposits and other credits                    $500.00",
		"Withdrawals and other debits                  ($800.00)", // parenthesized negative
		"Ending balance on July 31, 2026              (−$635.90)",
		"Deposits and other credits",
		"07/10/26   ONLINE TRANSFER FROM CHK 8685             500.00",
		"Total deposits and other credits               $500.00",
		"Withdrawals and other debits",
		"07/12/26   WIRE OUT VENDOR PAYMENT                  −800.00", // Unicode minus row
		"Total withdrawals and other debits             ($800.00)",
	}
	for i := range lines {
		lines[i] = normalizeSigns(lines[i])
	}
	txns, err := parseBoAStatement(lines)
	if err != nil {
		t.Fatalf("overdrawn statement (U+2212 + parens) must reconcile, got error: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("want 2 txns (1 deposit, 1 withdrawal), got %d: %+v", len(txns), txns)
	}
	var dep, wd BankTxn
	for _, bt := range txns {
		switch bt.AmountCents {
		case 50000:
			dep = bt
		case 80000:
			wd = bt
		}
	}
	if dep.Direction != Inflow {
		t.Fatalf("deposit must be Inflow, got %s", dep.Direction)
	}
	if wd.Direction != Outflow || wd.AmountCents != 80000 {
		t.Fatalf("withdrawal must be Outflow $800.00, got %s %d", wd.Direction, wd.AmountCents)
	}
}

// TestNormalizeSigns proves the two print-form conversions in isolation, and that
// ordinary parentheses in a description (no money shape) are left untouched.
func TestNormalizeSigns(t *testing.T) {
	cases := map[string]string{
		"bal −$335.90":             "bal -$335.90",             // U+2212 -> ASCII minus
		"debit ($800.00)":          "debit -$800.00",           // paren negative -> leading minus
		"amt (−$12.00)":            "amt -$12.00",              // both together
		"ACME (WEST) DES:PURCHASE": "ACME (WEST) DES:PURCHASE", // non-money parens untouched
	}
	for in, want := range cases {
		if got := normalizeSigns(in); got != want {
			t.Fatalf("normalizeSigns(%q) = %q, want %q", in, got, want)
		}
	}
}
