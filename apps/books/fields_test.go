package books

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// the store seals the free text of a ledger under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func rawOf(t *testing.T, st *store, q string, args ...any) string {
	t.Helper()
	var v string
	if err := st.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func sealedAndPlainless(t *testing.T, where, stored, plain string) {
	t.Helper()
	if !strings.HasPrefix(stored, "seal:") || strings.Contains(stored, plain) || strings.Contains(stored, plain[:5]) {
		t.Errorf("%s is stored as %.30q: not sealed", where, stored)
	}
}

// The free text of a ledger names who was paid and who paid: it is ciphertext in the
// database on both engines, every read gives it back, and the figures stay plain.
func TestTheFreeTextOfALedgerIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := newBookStore(t, "books")

		const memo = "Wire from Ada Lovelace for invoice 4471"
		v := Voucher{SourceKind: "manual", SourceID: "m1", PostingAt: "2026-07-01T10:00:00Z", Description: memo, Legs: []Leg{
			{Account: "1010", Debit: 5000}, {Account: "4000", Credit: 5000}}}
		if posted, err := st.post(ctx, v, 0); err != nil || !posted {
			t.Fatalf("post = %v, %v", posted, err)
		}
		sealedAndPlainless(t, "voucher.description", rawOf(t, st, `SELECT description FROM voucher WHERE source_id = 'm1'`), memo)
		sealedAndPlainless(t, "gl_entry.remarks", rawOf(t, st, `SELECT remarks FROM gl_entry WHERE source_id = 'm1' AND account = '1010'`), memo)
		if rawOf(t, st, `SELECT CAST(debit AS TEXT) FROM gl_entry WHERE source_id = 'm1' AND account = '1010'`) != "5000" {
			t.Error("an amount is not plain")
		}
		gl, err := st.listGL(ctx, 10)
		if err != nil || len(gl) != 2 || gl[0].Remarks != memo || gl[1].Remarks != memo {
			t.Fatalf("listGL = %+v, %v", gl, err)
		}

		bt := BankTxn{Connector: "plaid", ExternalID: "tx9", PostedAt: "2026-07-02", AmountCents: 1234, Currency: "usd", Direction: Outflow,
			Description: "CHECK 1182 ADA LOVELACE", Merchant: "Ada Lovelace"}
		if err := st.insertBankTxn(ctx, bt, `{"counterparty":"Ada Lovelace","iban":"GB82WEST12345698765432"}`, "", statusUnmatched); err != nil {
			t.Fatal(err)
		}
		sealedAndPlainless(t, "bank_txn.description", rawOf(t, st, `SELECT description FROM bank_txn WHERE external_id = 'tx9'`), bt.Description)
		sealedAndPlainless(t, "bank_txn.merchant", rawOf(t, st, `SELECT merchant FROM bank_txn WHERE external_id = 'tx9'`), bt.Merchant)
		sealedAndPlainless(t, "bank_txn.raw", rawOf(t, st, `SELECT raw FROM bank_txn WHERE external_id = 'tx9'`), "GB82WEST")
		un, err := st.listUnreconciled(ctx)
		if err != nil || len(un) != 1 || un[0].Description != bt.Description || un[0].Merchant != bt.Merchant {
			t.Fatalf("listUnreconciled = %+v, %v", un, err)
		}
		if m, err := st.bankMerchants(ctx); err != nil || m["plaid:tx9"] != "Ada Lovelace" {
			t.Fatalf("bankMerchants = %v, %v", m, err)
		}

		const prompt = "Ada Lovelace sent $12.34. What was it for?"
		if err := st.raiseQuestion(ctx, "plaid", "tx9", prompt, "2026-07-02T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		sealedAndPlainless(t, "bank_question.prompt", rawOf(t, st, `SELECT prompt FROM bank_question WHERE external_id = 'tx9'`), prompt)
		if qs, err := st.listQuestions(ctx); err != nil || len(qs) != 1 || qs[0].Prompt != prompt {
			t.Fatalf("listQuestions = %+v, %v", qs, err)
		}

		if err := st.insertInboxUnsorted(ctx, "hash1", "ada-lovelace-invoice.pdf"); err != nil {
			t.Fatal(err)
		}
		sealedAndPlainless(t, "books_inbox.filename", rawOf(t, st, `SELECT filename FROM books_inbox WHERE hash = 'hash1'`), "ada-lovelace-invoice.pdf")
		if err := st.upsertInboxDraft(ctx, "hash1", "ada-lovelace-invoice.pdf", ScanDraft{Vendor: "Ada Lovelace Consulting", Category: "services", Confidence: "high"}); err != nil {
			t.Fatal(err)
		}
		sealedAndPlainless(t, "books_inbox.extracted", rawOf(t, st, `SELECT extracted FROM books_inbox WHERE hash = 'hash1'`), "Ada Lovelace Consulting")
		item, err := st.inboxItem(ctx, "hash1")
		if err != nil || item.Filename != "ada-lovelace-invoice.pdf" || item.Vendor != "Ada Lovelace Consulting" {
			t.Fatalf("inboxItem = %+v, %v", item, err)
		}
		if items, err := st.listInboxOpen(ctx); err != nil || len(items) != 1 || items[0].Filename != "ada-lovelace-invoice.pdf" {
			t.Fatalf("listInboxOpen = %+v, %v", items, err)
		}
	})
}

// A memo lifted into another voucher, or planted in the clear, is refused.
func TestAMemoIsBoundToItsVoucher(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := newBookStore(t, "books")
		for _, id := range []string{"a", "b"} {
			v := Voucher{SourceKind: "manual", SourceID: id, PostingAt: "2026-07-01T10:00:00Z", Description: "memo " + id, Legs: []Leg{
				{Account: "1010", Debit: 100}, {Account: "4000", Credit: 100}}}
			if _, err := st.post(ctx, v, 0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.db.Exec(`UPDATE gl_entry SET remarks = $1 WHERE source_id = 'b'`, rawOf(t, st, `SELECT remarks FROM gl_entry WHERE source_id = 'a' AND account = '1010'`)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.listGL(ctx, 10); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("one voucher's memo opened on another: %v", err)
		}
		if _, err := st.db.Exec(`UPDATE gl_entry SET remarks = 'planted' WHERE source_id = 'b'`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.listGL(ctx, 10); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
	})
}

// A ledger is bound to its org: the same store file opened for another org's namespace
// holds ciphertext that does not open.
func TestALedgerBelongsToItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		ctx := context.Background()
		stores := cloud.NewOrgStoreAt[*store](cloud.Base{DataDir: dir}, "books", openStore(testMaster, "books"))
		t.Cleanup(func() { _ = stores.CloseAll() })
		acme, err := stores.For(principal.MustOrgNamespace("acme", ""))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := acme.post(ctx, Voucher{SourceKind: "manual", SourceID: "m", PostingAt: "2026-07-01T10:00:00Z", Description: "private", Legs: []Leg{
			{Account: "1010", Debit: 1}, {Account: "4000", Credit: 1}}}, 0); err != nil {
			t.Fatal(err)
		}
		globex, err := stores.For(principal.MustOrgNamespace("globex", ""))
		if err != nil {
			t.Fatal(err)
		}
		// A role that copies acme's sealed text into globex's book.
		sealed := rawOf(t, acme, `SELECT remarks FROM gl_entry WHERE account = '1010'`)
		if _, err := globex.post(ctx, Voucher{SourceKind: "manual", SourceID: "m", PostingAt: "2026-07-01T10:00:00Z", Description: "x", Legs: []Leg{
			{Account: "1010", Debit: 1}, {Account: "4000", Credit: 1}}}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := globex.db.Exec(`UPDATE gl_entry SET remarks = $1 WHERE account = '1010'`, sealed); err != nil {
			t.Fatal(err)
		}
		if _, err := globex.listGL(ctx, 10); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("acme's memo opened in globex's book: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			stores := cloud.NewOrgStoreAt[*store](cloud.Base{DataDir: t.TempDir()}, "books", openStore(master, "books"))
			if _, err := stores.For(principal.MustOrgNamespace("acme", "")); err == nil {
				t.Fatalf("opened with a %d-byte master", len(master))
			}
			_ = stores.CloseAll()
		}
	})
}

// A SQLite file written before the columns were sealed holds plaintext in them. It is
// built by hand (the rows are plaintext because the code that wrote them was), so this
// stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	ns := principal.MustOrgNamespace("acme", "")
	open := func() *store {
		t.Helper()
		db, err := cloud.OrgDB(dir, ns, "books")
		if err != nil {
			t.Fatal(err)
		}
		st, err := openStore(testMaster, "books")(ns, db)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return st
	}
	st := open()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO voucher (source_kind, source_id, posting_at, description) VALUES ('manual', 'old', '2026-01-01T00:00:00Z', 'old memo')`,
		`INSERT INTO gl_entry (voucher_id, posting_at, account, debit, credit, against, source_kind, source_id, remarks) VALUES (1, '2026-01-01T00:00:00Z', '1010', 5, 0, '4000', 'manual', 'old', 'old memo')`,
		`INSERT INTO bank_txn (connector, external_id, posted_at, amount_cents, direction, description, merchant, raw, status) VALUES ('plaid', 'old', '2026-01-01', 5, 'outflow', 'old desc', 'old merchant', '{"old":1}', 'unmatched')`,
		`INSERT INTO bank_question (connector, external_id, prompt, created_at) VALUES ('plaid', 'old', 'old prompt', '2026-01-01')`,
		`INSERT INTO books_inbox (filename, hash, status, extracted, created_at) VALUES ('old.pdf', 'oldhash', 'unsorted', '', '2026-01-01')`,
	} {
		if _, err := st.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		st = open()
		if gl, err := st.listGL(ctx, 10); err != nil || len(gl) != 1 || gl[0].Remarks != "old memo" {
			t.Fatalf("pass %d: a legacy entry = %+v, %v", pass, gl, err)
		}
		if un, err := st.listUnreconciled(ctx); err != nil || len(un) != 1 || un[0].Description != "old desc" || un[0].Merchant != "old merchant" {
			t.Fatalf("pass %d: a legacy bank line = %+v, %v", pass, un, err)
		}
		if qs, err := st.listQuestions(ctx); err != nil || len(qs) != 1 || qs[0].Prompt != "old prompt" {
			t.Fatalf("pass %d: a legacy question = %+v, %v", pass, qs, err)
		}
		if it, err := st.inboxItem(ctx, "oldhash"); err != nil || it.Filename != "old.pdf" {
			t.Fatalf("pass %d: a legacy inbox item = %+v, %v", pass, it, err)
		}
		for _, c := range []string{
			`SELECT description FROM voucher`, `SELECT remarks FROM gl_entry`, `SELECT description FROM bank_txn`,
			`SELECT merchant FROM bank_txn`, `SELECT raw FROM bank_txn`, `SELECT prompt FROM bank_question`, `SELECT filename FROM books_inbox`,
		} {
			if v := rawOf(t, st, c); !strings.HasPrefix(v, "seal:") {
				t.Errorf("pass %d: %s is still %.20q after the upgrade", pass, c, v)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
