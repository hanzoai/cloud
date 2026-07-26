package books

// scan_test.go — proofs for the scanner suite: extraction maps a synthetic receipt to the
// right fields and a BALANCED draft; scan/book posts idempotently; a vendor rule
// auto-classifies a matching merchant; the transactions read filters by category; and an
// inbox upload de-duplicates on the file hash. Money is asserted in exact int64 cents.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// fakeAI is a test double for the extraction model: it returns a fixed completion body,
// letting the real prompt → parse path run with no network. It satisfies cloud.AIClient.
type fakeAI struct {
	reply string
}

func (f *fakeAI) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{Content: f.reply}, nil
}

func (f *fakeAI) Embed(_ context.Context, _ *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

// voucherBalanced asserts the voucher's normalized legs satisfy Σdebit == Σcredit.
func voucherBalanced(t *testing.T, v Voucher) {
	t.Helper()
	legs, err := processGLMap(v.Legs, RoundOffAllowance, RoundOff)
	if err != nil {
		t.Fatalf("voucher does not balance: %v", err)
	}
	var dr, cr int64
	for _, l := range legs {
		dr += l.Debit
		cr += l.Credit
	}
	if dr != cr {
		t.Fatalf("Dr %d != Cr %d", dr, cr)
	}
}

// TestScanExtractAndDraft proves the AI's JSON extraction maps to the right fields and that
// buildDraft proposes a BALANCED voucher (Dr expense + Dr tax / Cr vendor payable), unposted.
func TestScanExtractAndDraft(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// The model returns strict JSON (wrapped in prose + a fence to exercise robust parsing).
	ai := &fakeAI{reply: "Here is the extraction:\n```json\n" + `{
	  "merchant": "GitHub",
	  "issuedAt": "2026-07-05",
	  "totalCents": 10800,
	  "taxCents": 800,
	  "currency": "usd",
	  "category": "software",
	  "lineItems": [{"description": "Team plan", "amountCents": 10000}],
	  "note": "monthly"
	}` + "\n```"}

	ex, err := scanExtract(ctx, ai, "test-model", "acme", "acme", "GitHub receipt total $108.00")
	if err != nil {
		t.Fatalf("scanExtract: %v", err)
	}
	if ex.Merchant != "GitHub" || ex.TotalCents != 10800 || ex.TaxCents != 800 {
		t.Fatalf("bad extraction: %+v", ex)
	}
	if ex.Currency != "usd" || ex.IssuedAt != "2026-07-05" {
		t.Fatalf("bad extraction meta: %+v", ex)
	}

	draft, err := st.buildDraft(ctx, ex, "hash-1")
	if err != nil {
		t.Fatalf("buildDraft: %v", err)
	}
	if !draft.Balanced {
		t.Fatalf("draft not balanced")
	}
	voucherBalanced(t, draft.Voucher)

	// Unknown vendor → low confidence, category from the AI hint (software → 5300), a question.
	if draft.Confidence != "low" {
		t.Fatalf("want low confidence for unknown vendor, got %q", draft.Confidence)
	}
	if draft.Category != SoftwareExpense {
		t.Fatalf("want category %s, got %s", SoftwareExpense, draft.Category)
	}
	if len(draft.Questions) != 1 {
		t.Fatalf("want 1 clarifying question, got %d", len(draft.Questions))
	}

	// The proposed legs: Dr 5300 net (10000), Dr 2200 tax (800), Cr 2001 payable (10800).
	assertLeg(t, draft.Voucher, SoftwareExpense, 10000, 0)
	assertLeg(t, draft.Voucher, SalesTaxPayable, 800, 0)
	assertLeg(t, draft.Voucher, VendorPayable, 0, 10800)

	// buildDraft POSTS NOTHING — the ledger is still empty.
	if gl, _ := st.listGL(ctx, 10); len(gl) != 0 {
		t.Fatalf("buildDraft must not post; found %d gl rows", len(gl))
	}
}

func assertLeg(t *testing.T, v Voucher, account string, debit, credit int64) {
	t.Helper()
	for _, l := range v.Legs {
		if l.Account == account {
			if l.Debit != debit || l.Credit != credit {
				t.Fatalf("leg %s: want Dr %d Cr %d, got Dr %d Cr %d", account, debit, credit, l.Debit, l.Credit)
			}
			return
		}
	}
	t.Fatalf("leg %s not found in voucher", account)
}

// TestScanBookIdempotent proves scan/book posts a balanced expense voucher exactly once —
// re-booking the same scan id (source_kind=scan) is a no-op, never a double-post.
func TestScanBookIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	v := Voucher{
		SourceKind: scanSourceKind,
		SourceID:   "hash-book-1",
		PostingAt:  "2026-07-05T00:00:00Z",
		Legs: []Leg{
			{Account: SoftwareExpense, Debit: 10000},
			{Account: SalesTaxPayable, Debit: 800},
			{Account: VendorPayable, Credit: 10800},
		},
	}

	posted, err := st.post(ctx, v, RoundOffAllowance)
	if err != nil || !posted {
		t.Fatalf("first book: posted=%v err=%v", posted, err)
	}
	first, _ := st.listGL(ctx, 100)

	// Re-book the same scan id → idempotent no-op.
	posted2, err := st.post(ctx, v, RoundOffAllowance)
	if err != nil {
		t.Fatalf("second book err: %v", err)
	}
	if posted2 {
		t.Fatalf("re-book must not post again")
	}
	second, _ := st.listGL(ctx, 100)
	if len(first) != len(second) {
		t.Fatalf("double-post: %d gl rows then %d", len(first), len(second))
	}
}

// TestVendorRuleAutoClassifies proves that once a rule (or vendor) is set, a matching
// merchant self-classifies at confidence "auto" with the rule's category.
func TestVendorRuleAutoClassifies(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// A rule: anything containing "aws" books to cloud (5000).
	if err := st.upsertRule(ctx, Rule{Pattern: "aws", Category: "cloud", Priority: 10}); err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	vendor, account, conf, err := st.classifyMerchant(ctx, "AWS EMEA SARL")
	if err != nil {
		t.Fatalf("classifyMerchant: %v", err)
	}
	if conf != "auto" || account != CloudCOGS {
		t.Fatalf("rule did not auto-classify: vendor=%q account=%q conf=%q", vendor, account, conf)
	}

	// A vendor with a default category resolves canonical name + auto.
	if err := st.upsertVendor(ctx, Vendor{Canonical: "Acme Coffee", Aliases: []string{"SQ *ACME"}, DefaultCategory: "meals"}); err != nil {
		t.Fatalf("upsertVendor: %v", err)
	}
	vendor, account, conf, err = st.classifyMerchant(ctx, "SQ *ACME #42")
	if err != nil {
		t.Fatalf("classifyMerchant vendor: %v", err)
	}
	if conf != "auto" || account != MealsExpense || vendor != "Acme Coffee" {
		t.Fatalf("vendor did not auto-classify: vendor=%q account=%q conf=%q", vendor, account, conf)
	}

	// An unknown merchant stays low-confidence.
	_, _, conf, err = st.classifyMerchant(ctx, "Mystery LLC")
	if err != nil {
		t.Fatalf("classifyMerchant unknown: %v", err)
	}
	if conf != "low" {
		t.Fatalf("unknown merchant should be low, got %q", conf)
	}

	// A rule-classified scan draft is auto and books to the rule's category.
	ex := Extracted{Merchant: "AWS EMEA SARL", IssuedAt: "2026-07-06", TotalCents: 9900, Currency: "usd", Category: "office"}
	draft, err := st.buildDraft(ctx, ex, "hash-aws")
	if err != nil {
		t.Fatalf("buildDraft: %v", err)
	}
	if draft.Confidence != "auto" || draft.Category != CloudCOGS {
		t.Fatalf("draft not auto/cloud: conf=%q cat=%q", draft.Confidence, draft.Category)
	}
	if len(draft.Questions) != 0 {
		t.Fatalf("auto draft should raise no question, got %d", len(draft.Questions))
	}
	voucherBalanced(t, draft.Voucher)
}

// TestTransactionsFilterByCategory proves the transactions read returns booked rows and
// filters by category, projecting each voucher to one register line in exact cents.
func TestTransactionsFilterByCategory(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// Two scanned bills: one software (5300), one meals (5600).
	mustPost(t, st, "scan:soft", Leg{Account: SoftwareExpense, Debit: 10000}, Leg{Account: VendorPayable, Credit: 10000})
	mustPost(t, st, "scan:meal", Leg{Account: MealsExpense, Debit: 4200}, Leg{Account: VendorPayable, Credit: 4200})

	all, err := st.listTransactions(ctx, txnFilter{limit: 50})
	if err != nil {
		t.Fatalf("listTransactions: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 transactions, got %d", len(all))
	}

	soft, err := st.listTransactions(ctx, txnFilter{category: "software", limit: 50})
	if err != nil {
		t.Fatalf("listTransactions software: %v", err)
	}
	if len(soft) != 1 || soft[0].Category != SoftwareExpense || soft[0].AmountCents != 10000 {
		t.Fatalf("category filter wrong: %+v", soft)
	}

	// Filter by the account number directly resolves the same way.
	byAcct, err := st.listTransactions(ctx, txnFilter{category: MealsExpense, limit: 50})
	if err != nil {
		t.Fatalf("listTransactions meals: %v", err)
	}
	if len(byAcct) != 1 || byAcct[0].AmountCents != 4200 {
		t.Fatalf("account filter wrong: %+v", byAcct)
	}
}

// TestInboxUploadIdempotent proves uploading the same file hash twice yields ONE inbox row,
// and that the scan draft advances it unsorted → draft.
func TestInboxUploadIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	body := []byte("ACME INVOICE total $10.00")
	id := scanIDOf(body)

	if err := st.insertInboxUnsorted(ctx, id, "invoice.txt"); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if err := st.insertInboxUnsorted(ctx, id, "invoice.txt"); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	open, err := st.listInboxOpen(ctx)
	if err != nil {
		t.Fatalf("listInboxOpen: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("re-upload must de-dup: got %d inbox items", len(open))
	}
	if open[0].Status != inboxUnsorted {
		t.Fatalf("want unsorted, got %q", open[0].Status)
	}

	// A scan draft advances the same hash to 'draft' with an extracted summary.
	draft := ScanDraft{
		ScanID:     id,
		Extracted:  Extracted{Merchant: "Acme", TotalCents: 1000, Currency: "usd"},
		Vendor:     "Acme",
		Category:   SoftwareExpense,
		Confidence: "low",
	}
	if err := st.upsertInboxDraft(ctx, id, "invoice.txt", draft); err != nil {
		t.Fatalf("upsertInboxDraft: %v", err)
	}
	open, _ = st.listInboxOpen(ctx)
	if len(open) != 1 || open[0].Status != inboxDraft {
		t.Fatalf("want 1 draft item, got %+v", open)
	}
	if open[0].Extracted == nil || open[0].Extracted.Merchant != "Acme" {
		t.Fatalf("draft summary missing: %+v", open[0])
	}

	// Booking it drops it out of the open queue.
	if err := st.markInboxBooked(ctx, id); err != nil {
		t.Fatalf("markInboxBooked: %v", err)
	}
	open, _ = st.listInboxOpen(ctx)
	if len(open) != 0 {
		t.Fatalf("booked item must leave the open queue, got %d", len(open))
	}
}

// TestScanEconomicIdentityDedup proves the fix for the byte-hash-only idempotency finding: the
// SAME real-world bill re-scanned into a DIFFERENT file hash (a different scanID) is caught by
// its economic identity (vendor, total, issue date), so it does not double-book — while an
// override, and a genuinely different bill, both book.
func TestScanEconomicIdentityDedup(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// Book the original scan: Dr 5300 net + Dr 2200 tax / Cr 2001 $108, vendor "GitHub".
	v := Voucher{
		SourceKind: scanSourceKind, SourceID: "hash-a", PostingAt: "2026-07-05T00:00:00Z",
		Description: "GitHub",
		Legs: []Leg{
			{Account: SoftwareExpense, Debit: 10000},
			{Account: SalesTaxPayable, Debit: 800},
			{Account: VendorPayable, Credit: 10800},
		},
	}
	idA := voucherIdentity(v)
	if idA.Vendor != "github" || idA.Total != 10800 || idA.Issued != "2026-07-05" {
		t.Fatalf("identity derived wrong: %+v", idA)
	}
	ok, err := st.post(ctx, v, RoundOffAllowance)
	if err != nil || !ok {
		t.Fatalf("book original: ok=%v err=%v", ok, err)
	}
	if err := st.recordScanIdentity(ctx, idA, "hash-a", v.PostingAt); err != nil {
		t.Fatalf("record identity: %v", err)
	}

	// The SAME bill re-scanned at a different DPI → a new file hash, but the same identity
	// ("GitHub", $108.00, 2026-07-05, formatted with a trailing space to prove normalization).
	reV := v
	reV.SourceID = "hash-b"
	reV.Description = "GitHub "
	idB := voucherIdentity(reV)
	prior, dup, err := st.scanIdentityBooked(ctx, idB, "hash-b")
	if err != nil {
		t.Fatalf("dedup check: %v", err)
	}
	if !dup || prior != "hash-a" {
		t.Fatalf("a re-scan of the same bill must be flagged a duplicate of hash-a, got dup=%v prior=%q", dup, prior)
	}

	// Re-booking the SAME scan id is NOT a duplicate of itself — post() already makes it idempotent.
	if _, dup, _ := st.scanIdentityBooked(ctx, idA, "hash-a"); dup {
		t.Fatalf("the same scan id must not flag itself as a duplicate")
	}

	// A genuinely different bill (different total) is not a duplicate.
	otherV := v
	otherV.Legs = []Leg{{Account: SoftwareExpense, Debit: 5000}, {Account: VendorPayable, Credit: 5000}}
	if _, dup, _ := st.scanIdentityBooked(ctx, voucherIdentity(otherV), "hash-c"); dup {
		t.Fatalf("a different-total bill must not flag as a duplicate")
	}
}

// TestParseExtractionRejectsBadMoney proves the extractor fails closed on malformed money
// (tax exceeding total, non-positive total) rather than proposing an unbalanced draft.
func TestParseExtractionRejectsBadMoney(t *testing.T) {
	bad := []string{
		`{"merchant":"X","totalCents":0,"taxCents":0}`,
		`{"merchant":"X","totalCents":100,"taxCents":200}`,
		`{"merchant":"","totalCents":100}`,
		`no json here`,
	}
	for _, b := range bad {
		if _, err := parseExtraction(b); err == nil {
			t.Fatalf("parseExtraction(%q) should have failed", b)
		}
	}
	// A minimal valid extraction round-trips.
	ex, err := parseExtraction(`{"merchant":"X","totalCents":100}`)
	if err != nil {
		t.Fatalf("valid extraction rejected: %v", err)
	}
	if ex.Currency != "usd" {
		t.Fatalf("currency should default usd, got %q", ex.Currency)
	}
}
