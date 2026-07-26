package books

import (
	"context"
	"testing"
)

// ofxSGML is a synthetic OFX 1.x (SGML) statement: header block, uppercase leaf tags with NO
// leaf close tags — the value runs to the next tag. Two rows: a debit (Outflow) and a credit
// (Inflow), each with a bank-assigned FITID.
const ofxSGML = `OFXHEADER:100
DATA:OFXSGML
VERSION:102
SECURITY:NONE
ENCODING:USASCII

<OFX>
<BANKMSGSRSV1>
<STMTTRNRS>
<STMTRS>
<CURDEF>USD
<BANKTRANLIST>
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20260705120000.000[-5:EST]
<TRNAMT>-71.20
<FITID>SGML-0001
<NAME>AWS CLOUD
<MEMO>DES:AWS SVC
</STMTTRN>
<STMTTRN>
<TRNTYPE>CREDIT
<DTPOSTED>20260706
<TRNAMT>1,203.50
<FITID>SGML-0002
<NAME>SQUARE INC DEPOSIT
</STMTTRN>
</BANKTRANLIST>
</STMTRS>
</STMTTRNRS>
</BANKMSGSRSV1>
</OFX>`

// ofxXML is the SAME two transactions as a well-formed OFX 2.x (XML) document: closed leaf
// tags. The single ofx parser must read it identically to the SGML form.
const ofxXML = `<?xml version="1.0" encoding="UTF-8"?>
<?OFX OFXHEADER="200" VERSION="220" SECURITY="NONE"?>
<OFX>
  <BANKMSGSRSV1>
    <STMTTRNRS>
      <STMTRS>
        <CURDEF>USD</CURDEF>
        <BANKTRANLIST>
          <STMTTRN>
            <TRNTYPE>DEBIT</TRNTYPE>
            <DTPOSTED>20260705120000.000[-5:EST]</DTPOSTED>
            <TRNAMT>-71.20</TRNAMT>
            <FITID>XML-0001</FITID>
            <NAME>AWS CLOUD</NAME>
            <MEMO>DES:AWS SVC</MEMO>
          </STMTTRN>
          <STMTTRN>
            <TRNTYPE>CREDIT</TRNTYPE>
            <DTPOSTED>20260706</DTPOSTED>
            <TRNAMT>1,203.50</TRNAMT>
            <FITID>XML-0002</FITID>
            <NAME>SQUARE INC DEPOSIT</NAME>
          </STMTTRN>
        </BANKTRANLIST>
      </STMTRS>
    </STMTTRNRS>
  </BANKMSGSRSV1>
</OFX>`

// boaCSV is a synthetic Bank of America CSV export: the summary preamble, a blank spacer,
// then the "Date,Description,Amount,Running Bal." table with a debit and a credit.
const boaCSV = `Description,,Summary Amt.
Beginning balance as of 07/01/2026,,"1,000.00"
Total credits,,"1,203.50"
Total debits,,"-71.20"

Date,Description,Amount,Running Bal.
07/05/2026,"CHECKCARD 0705 AWS CLOUD DES:AWS SVC",-71.20,"928.80"
07/06/2026,"SQUARE INC DES:DEPOSIT","1,203.50","2,132.30"
`

// find returns the parsed txn with the given ExternalID, failing the test if absent.
func find(t *testing.T, txns []BankTxn, id string) BankTxn {
	t.Helper()
	for _, bt := range txns {
		if bt.ExternalID == id {
			return bt
		}
	}
	t.Fatalf("no txn with external id %q in %+v", id, txns)
	return BankTxn{}
}

// assertPair asserts the shared shape of the two-row fixture: a $71.20 Outflow and a
// $1,203.50 Inflow, exact cents, with the given FITID-derived external ids.
func assertPair(t *testing.T, txns []BankTxn, debitID, creditID string) {
	t.Helper()
	if len(txns) != 2 {
		t.Fatalf("want 2 txns, got %d: %+v", len(txns), txns)
	}
	debit := find(t, txns, debitID)
	if debit.Direction != Outflow || debit.AmountCents != 7120 {
		t.Fatalf("debit must be Outflow 7120 cents, got %s %d", debit.Direction, debit.AmountCents)
	}
	if debit.PostedAt != "2026-07-05T12:00:00Z" {
		t.Fatalf("debit postedAt = %q, want 2026-07-05T12:00:00Z", debit.PostedAt)
	}
	credit := find(t, txns, creditID)
	if credit.Direction != Inflow || credit.AmountCents != 120350 {
		t.Fatalf("credit must be Inflow 120350 cents, got %s %d", credit.Direction, credit.AmountCents)
	}
	if credit.PostedAt != "2026-07-06T00:00:00Z" {
		t.Fatalf("credit postedAt = %q, want 2026-07-06T00:00:00Z (midnight for a bare date)", credit.PostedAt)
	}
	for _, bt := range txns {
		if bt.Connector != "import" || bt.Currency != "usd" {
			t.Fatalf("connector/currency must be import/usd, got %s/%s", bt.Connector, bt.Currency)
		}
	}
}

// TestImportOFXSGML proves the OFX 1.x SGML dialect parses to exact cents with FITID as the
// external id and sign resolved into Direction.
func TestImportOFXSGML(t *testing.T) {
	txns, err := newImport().Parse([]byte(ofxSGML))
	if err != nil {
		t.Fatalf("parse SGML: %v", err)
	}
	assertPair(t, txns, "SGML-0001", "SGML-0002")
}

// TestImportOFXXML proves the OFX 2.x XML dialect parses identically through the same parser.
func TestImportOFXXML(t *testing.T) {
	txns, err := newImport().Parse([]byte(ofxXML))
	if err != nil {
		t.Fatalf("parse XML: %v", err)
	}
	assertPair(t, txns, "XML-0001", "XML-0002")
}

// TestImportBoACSV proves the BoA CSV export parses past its preamble to exact cents, with a
// deterministic (stable) external id per row.
func TestImportBoACSV(t *testing.T) {
	txns, err := newImport().Parse([]byte(boaCSV))
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("want 2 CSV txns, got %d: %+v", len(txns), txns)
	}

	var debit, credit BankTxn
	for _, bt := range txns {
		switch bt.Direction {
		case Outflow:
			debit = bt
		case Inflow:
			credit = bt
		}
	}
	if debit.AmountCents != 7120 || debit.PostedAt != "2026-07-05T00:00:00Z" {
		t.Fatalf("CSV debit must be 7120 cents on 2026-07-05, got %d %s", debit.AmountCents, debit.PostedAt)
	}
	if credit.AmountCents != 120350 || credit.PostedAt != "2026-07-06T00:00:00Z" {
		t.Fatalf("CSV credit must be 120350 cents on 2026-07-06, got %d %s", credit.AmountCents, credit.PostedAt)
	}
	if debit.ExternalID == "" || credit.ExternalID == "" || debit.ExternalID == credit.ExternalID {
		t.Fatalf("CSV rows must carry distinct non-empty external ids, got %q / %q", debit.ExternalID, credit.ExternalID)
	}

	// Deterministic: re-parsing the same export reproduces the same external ids.
	again, err := newImport().Parse([]byte(boaCSV))
	if err != nil {
		t.Fatalf("re-parse CSV: %v", err)
	}
	if again[0].ExternalID != txns[0].ExternalID || again[1].ExternalID != txns[1].ExternalID {
		t.Fatalf("CSV external ids must be deterministic across re-parse")
	}
}

// TestImportIdempotentPost proves that importing the SAME statement twice posts once: the
// FITID-keyed (connector, external_id) guard makes every row on the second pass an
// idempotent skip, so the books never double-count a re-uploaded file.
func TestImportIdempotentPost(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	txns, err := newImport().Parse([]byte(ofxSGML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// First import: the debit posts an expense; the unmatched credit raises a question.
	for _, bt := range txns {
		res, err := mapAndPost(ctx, st, bt)
		if err != nil {
			t.Fatalf("first map %s: %v", bt.ExternalID, err)
		}
		if res.Skipped {
			t.Fatalf("first import of %s must not skip", bt.ExternalID)
		}
	}

	tb1, _ := trialBalance(ctx, st, "", "")
	if !tb1.Balanced {
		t.Fatalf("books not balanced after import: %d/%d", tb1.TotalDebit, tb1.TotalCredit)
	}
	if dd, _ := closingOf(tb1, CloudCOGS); dd != 7120 {
		t.Fatalf("AWS debit must land in Cloud COGS at 7120 cents, got %d", dd)
	}

	// Second import of the identical file: every row is an idempotent skip.
	for _, bt := range txns {
		res, err := mapAndPost(ctx, st, bt)
		if err != nil {
			t.Fatalf("re-map %s: %v", bt.ExternalID, err)
		}
		if !res.Skipped {
			t.Fatalf("re-import of %s must skip (idempotent), got %+v", bt.ExternalID, res)
		}
	}

	tb2, _ := trialBalance(ctx, st, "", "")
	if tb2.TotalDebit != tb1.TotalDebit || tb2.TotalCredit != tb1.TotalCredit {
		t.Fatalf("re-import changed the books: %d/%d vs %d/%d",
			tb2.TotalDebit, tb2.TotalCredit, tb1.TotalDebit, tb1.TotalCredit)
	}
}
