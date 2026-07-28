package books

// teller_test.go — the Teller connector's proof. It exercises Fetch against a MOCKED Teller
// HTTP layer (httptest) with an in-memory KMS double, asserting the money-critical
// properties: decimal-string → exact int64 cents, sign → Direction, pending rows skipped,
// idempotency across re-sync, the token living ONLY in KMS (never books.db, never the URL),
// and the connector failing closed when no bank is linked.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
)

// fakeKMS is an in-memory cloud.KMSClient for tests: the ONE credential store, holding the
// Teller access_token so the connector reads it through KMS exactly as production does.
type fakeKMS struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeKMS() *fakeKMS { return &fakeKMS{data: map[string][]byte{}} }

func (k *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.data[ref]
	if !ok {
		return nil, fmt.Errorf("kms: no secret %q", ref)
	}
	return append([]byte(nil), v...), nil
}

func (k *fakeKMS) PutSecret(_ context.Context, ref string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.data[ref] = append([]byte(nil), value...)
	return nil
}

func (k *fakeKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("kms: sign not supported in test")
}

const tellerTestToken = "test_token_SECRET_do_not_persist_9f3a"

// tellerServer stands up a mocked Teller API. It asserts every request carries the
// access_token as the Basic-auth username (proving the token is used for auth, in the header
// only), lists one BoA depository account, and serves a fixed transaction page: a posted
// outflow, a posted inflow, and a pending row that must be skipped.
func tellerServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var calls int
	mux := http.NewServeMux()

	mux.HandleFunc("/accounts", func(w http.ResponseWriter, r *http.Request) {
		calls++
		assertTellerAuth(t, r)
		writeJSON(t, w, []tellerAccount{{ID: "acc_boa_1", Currency: "USD", Type: "depository"}})
	})

	mux.HandleFunc("/accounts/acc_boa_1/transactions", func(w http.ResponseWriter, r *http.Request) {
		calls++
		assertTellerAuth(t, r)
		// A second page (from_id set) is the end of history: empty.
		if r.URL.Query().Get("from_id") != "" {
			writeJSON(t, w, []tellerTxn{})
			return
		}
		writeJSON(t, w, []tellerTxn{
			tellerRow("txn_3", "-12.34", "2026-07-05", "TRADER JOES", "TRADER JOE'S #123", "posted"),
			tellerRow("txn_2", "100.00", "2026-07-04", "", "REFUND ACH", "posted"),
			tellerRow("txn_1", "-9.99", "2026-07-03", "PENDING CO", "PENDING CHARGE", "pending"),
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func tellerRow(id, amount, date, counterparty, desc, status string) tellerTxn {
	var tx tellerTxn
	tx.ID, tx.AccountID, tx.Amount, tx.Date, tx.Description, tx.Status =
		id, "acc_boa_1", amount, date, desc, status
	tx.Details.Counterparty.Name = counterparty
	return tx
}

func assertTellerAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, _, ok := r.BasicAuth()
	if !ok || user != tellerTestToken {
		t.Fatalf("teller request missing/wrong access_token basic-auth username: ok=%v user=%q", ok, user)
	}
	if strings.Contains(r.URL.RawQuery, tellerTestToken) || strings.Contains(r.URL.Path, tellerTestToken) {
		t.Fatalf("access_token must never appear in the URL: %s", r.URL)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode mock response: %v", err)
	}
}

// newTellerConn wires a connector to the mock server + fake KMS with the token pre-stored via
// the exchange plumbing — the exact path a linked org takes.
func newTellerConn(t *testing.T, srv *httptest.Server) (*tellerConn, *fakeKMS) {
	t.Helper()
	kms := newFakeKMS()
	tc := &tellerConn{
		kms:  func() cloud.KMSClient { return kms },
		doer: srv.Client(),
		base: srv.URL,
	}
	if err := tc.exchange(context.Background(), "acme", tellerTestToken); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	return tc, kms
}

// TestTellerFetchCentsAndDirection proves the money math: decimal strings become exact int64
// cents, the sign selects Direction, pending rows are skipped, and rows normalize to the
// BankTxn contract (connector, RFC3339 posting time, merchant, currency).
func TestTellerFetchCentsAndDirection(t *testing.T) {
	ctx := context.Background()
	srv, _ := tellerServer(t)
	tc, _ := newTellerConn(t, srv)

	txns, next, err := tc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("expected 2 posted txns (pending skipped), got %d: %+v", len(txns), txns)
	}
	byID := map[string]BankTxn{}
	for _, x := range txns {
		byID[x.ExternalID] = x
	}

	out, ok := byID["txn_3"]
	if !ok {
		t.Fatalf("missing outflow txn_3")
	}
	if out.AmountCents != 1234 || out.Direction != Outflow {
		t.Fatalf("outflow -12.34 must be 1234 cents Outflow, got %d %s", out.AmountCents, out.Direction)
	}
	if out.Connector != "teller" || out.Currency != "usd" {
		t.Fatalf("outflow connector/currency wrong: %+v", out)
	}
	if out.PostedAt != "2026-07-05T00:00:00Z" {
		t.Fatalf("outflow posted_at must be RFC3339 UTC midnight, got %q", out.PostedAt)
	}
	if out.Merchant != "TRADER JOES" {
		t.Fatalf("outflow merchant must prefer counterparty name, got %q", out.Merchant)
	}

	in, ok := byID["txn_2"]
	if !ok {
		t.Fatalf("missing inflow txn_2")
	}
	if in.AmountCents != 10000 || in.Direction != Inflow {
		t.Fatalf("inflow 100.00 must be 10000 cents Inflow, got %d %s", in.AmountCents, in.Direction)
	}
	if in.Merchant != "REFUND ACH" {
		t.Fatalf("inflow with no counterparty must fall back to description, got %q", in.Merchant)
	}

	if _, found := byID["txn_1"]; found {
		t.Fatalf("pending txn_1 must be skipped, but it was returned")
	}
	if next == "" {
		t.Fatalf("cursor must advance after new txns")
	}
	// The advanced cursor anchors the newest transaction id per account.
	cur := decodeTellerCursor(next)
	if cur["acc_boa_1"] != "txn_3" {
		t.Fatalf("cursor must anchor newest id txn_3, got %q", cur["acc_boa_1"])
	}
}

// TestTellerFetchExactCentsNoFloat locks a decimal that a float64 would corrupt: 0.10 + a
// long-tail amount. Each parses to exact cents with no rounding.
func TestTellerFetchExactCentsNoFloat(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []tellerAccount{{ID: "acc_boa_1", Currency: "USD"}})
	})
	mux.HandleFunc("/accounts/acc_boa_1/transactions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from_id") != "" {
			writeJSON(t, w, []tellerTxn{})
			return
		}
		writeJSON(t, w, []tellerTxn{
			tellerRow("p_1", "-0.10", "2026-07-05", "X", "dime out", "posted"),
			tellerRow("p_2", "1234567.89", "2026-07-05", "Y", "big in", "posted"),
			tellerRow("p_3", "-0.07", "2026-07-05", "Z", "cents out", "posted"),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	kms := newFakeKMS()
	tc := &tellerConn{kms: func() cloud.KMSClient { return kms }, doer: srv.Client(), base: srv.URL}
	if err := tc.exchange(ctx, "acme", tellerTestToken); err != nil {
		t.Fatalf("exchange: %v", err)
	}

	txns, _, err := tc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	want := map[string]struct {
		cents int64
		dir   Direction
	}{
		"p_1": {10, Outflow},
		"p_2": {123456789, Inflow},
		"p_3": {7, Outflow},
	}
	if len(txns) != len(want) {
		t.Fatalf("want %d txns, got %d", len(want), len(txns))
	}
	for _, x := range txns {
		w := want[x.ExternalID]
		if x.AmountCents != w.cents || x.Direction != w.dir {
			t.Fatalf("%s: want %d %s, got %d %s", x.ExternalID, w.cents, w.dir, x.AmountCents, x.Direction)
		}
	}
}

// TestTellerIdempotentThroughEngine proves fetched Teller rows post exactly once: mapping the
// batch, then re-mapping the identical batch, changes nothing (every row skipped) and leaves
// the trial balance untouched. A re-Fetch at the advanced cursor returns no new rows.
func TestTellerIdempotentThroughEngine(t *testing.T) {
	ctx := context.Background()
	srv, _ := tellerServer(t)
	tc, _ := newTellerConn(t, srv)
	st := newBookStore(t, "books")

	txns, next, err := tc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, bt := range txns {
		if _, err := mapAndPost(ctx, st, bt); err != nil {
			t.Fatalf("map %s: %v", bt.ExternalID, err)
		}
	}
	tb1, _ := trialBalance(ctx, st, "", "")
	rows1, _ := st.listGL(ctx, 1000)

	// Re-map the same batch — every row must be an idempotent no-op.
	for _, bt := range txns {
		res, err := mapAndPost(ctx, st, bt)
		if err != nil || !res.Skipped {
			t.Fatalf("re-map %s must skip, got %+v err=%v", bt.ExternalID, res, err)
		}
	}
	tb2, _ := trialBalance(ctx, st, "", "")
	rows2, _ := st.listGL(ctx, 1000)
	if tb2.TotalDebit != tb1.TotalDebit || tb2.TotalCredit != tb1.TotalCredit || len(rows2) != len(rows1) {
		t.Fatalf("idempotent re-map changed the books: tb %d/%d→%d/%d rows %d→%d",
			tb1.TotalDebit, tb1.TotalCredit, tb2.TotalDebit, tb2.TotalCredit, len(rows1), len(rows2))
	}
	if !tb2.Balanced {
		t.Fatalf("books must stay balanced")
	}

	// Re-Fetch at the advanced cursor: the newest ids are known, so nothing new comes back
	// and the cursor does not move.
	again, next2, err := tc.Fetch(ctx, "acme", next)
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-fetch at cursor must return no new rows, got %d", len(again))
	}
	if next2 != next {
		t.Fatalf("re-fetch with nothing new must not advance the cursor: %q → %q", next, next2)
	}
}

// TestTellerTokenNeverInBooksDB proves the access_token lives ONLY in KMS: after fetching and
// posting a full batch, no persisted bank row (any text column, incl. the raw audit blob)
// contains the token, while KMS still holds it under the org-scoped ref.
func TestTellerTokenNeverInBooksDB(t *testing.T) {
	ctx := context.Background()
	srv, _ := tellerServer(t)
	tc, kms := newTellerConn(t, srv)
	st := newBookStore(t, "books")

	txns, _, err := tc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, bt := range txns {
		if _, err := mapAndPost(ctx, st, bt); err != nil {
			t.Fatalf("map %s: %v", bt.ExternalID, err)
		}
	}

	// Scan every text column of every persisted bank_txn (raw included) for the token.
	rows, err := st.db.QueryContext(ctx,
		`SELECT connector, external_id, posted_at, currency, direction, description, merchant, raw, matched_voucher, status FROM bank_txn`)
	if err != nil {
		t.Fatalf("scan bank_txn: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		cols := make([]string, 10)
		ptrs := make([]any, 10)
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		seen++
		for _, c := range cols {
			if strings.Contains(c, tellerTestToken) {
				t.Fatalf("access_token leaked into books.db column: %q", c)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("expected persisted bank rows to scan")
	}

	// KMS remains the token's only home.
	got, err := kms.GetSecret(ctx, tellerTokenRef("acme"))
	if err != nil {
		t.Fatalf("token must remain in KMS: %v", err)
	}
	if string(got) != tellerTestToken {
		t.Fatalf("KMS token mismatch: %q", string(got))
	}
}

// TestTellerFailsClosedWhenNotLinked proves an org with no stored token (and a nil KMS)
// yields no data and no error — the pre-link state the sync loop steps over, never a
// plaintext fallback.
func TestTellerFailsClosedWhenNotLinked(t *testing.T) {
	ctx := context.Background()
	srv, calls := tellerServer(t)

	// KMS wired but this org never linked → no token → nothing pulled, no HTTP call.
	kms := newFakeKMS()
	tc := &tellerConn{kms: func() cloud.KMSClient { return kms }, doer: srv.Client(), base: srv.URL}
	txns, next, err := tc.Fetch(ctx, "acme", "cursor-x")
	if err != nil {
		t.Fatalf("unlinked org must not error, got %v", err)
	}
	if len(txns) != 0 || next != "cursor-x" {
		t.Fatalf("unlinked org must return no rows and an unchanged cursor, got %d rows next=%q", len(txns), next)
	}
	if *calls != 0 {
		t.Fatalf("unlinked org must make no Teller HTTP call, got %d", *calls)
	}

	// No KMS at all → fail closed identically.
	tcNil := &tellerConn{kms: func() cloud.KMSClient { return nil }, doer: srv.Client(), base: srv.URL}
	txns, next, err = tcNil.Fetch(ctx, "acme", "cursor-y")
	if err != nil || len(txns) != 0 || next != "cursor-y" {
		t.Fatalf("nil KMS must fail closed: rows=%d next=%q err=%v", len(txns), next, err)
	}
}
