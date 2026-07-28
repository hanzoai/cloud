package books

// plaid_test.go — the Plaid connector against a MOCKED Plaid HTTP layer (httptest). No real
// Plaid is contacted. These lock the money-critical invariants: exact decimal-dollars → int64
// cents with the correct Direction, cursor advance + paging, idempotent re-sync (no
// double-post), and that the access_token is sealed into KMS and NEVER written to books.db.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// plaidKMS is an in-memory KMS. It records every Put so a test can prove the access_token went
// to KMS and can scan the ledger to prove it did NOT go to books.db.
type plaidKMS struct{ m map[string][]byte }

func newPlaidKMS() *plaidKMS { return &plaidKMS{m: map[string][]byte{}} }

func (k *plaidKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	v, ok := k.m[ref]
	if !ok {
		return nil, io.EOF
	}
	return append([]byte(nil), v...), nil
}
func (k *plaidKMS) PutSecret(_ context.Context, ref string, value []byte) error {
	k.m[ref] = append([]byte(nil), value...)
	return nil
}
func (k *plaidKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) { return nil, nil }

// plaidEnv wires the two credential references to the fake KMS-sealed values.
func plaidEnv(m map[string]string) func(string) string {
	return func(name string) string { return m[name] }
}

// newTestPlaid builds a connector pointed at a mock server with credentials pre-sealed in KMS.
func newTestPlaid(t *testing.T, base string, k *plaidKMS) *plaidConn {
	t.Helper()
	// Seal the client credentials at their KMS references (env holds refs, never values).
	k.m["kms/plaid/client_id"] = []byte("test-client-id")
	k.m["kms/plaid/secret"] = []byte("test-secret")
	return &plaidConn{
		base: base,
		http: &http.Client{},
		kms:  k,
		env: plaidEnv(map[string]string{
			"CLOUD_PLAID_CLIENT_ID": "kms/plaid/client_id",
			"CLOUD_PLAID_SECRET":    "kms/plaid/secret",
		}),
	}
}

// readReq decodes a request body into a generic map for assertions.
func readReq(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode request %s: %v", r.URL.Path, err)
	}
	return m
}

// TestPlaidFetchCentsAndDirection proves exact decimal→cents conversion and sign→Direction:
// a positive Plaid amount is an Outflow, a negative one an Inflow, both as exact magnitudes.
func TestPlaidFetchCentsAndDirection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transactions/sync" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		req := readReq(t, r)
		if req["access_token"] != "access-BofA" {
			t.Fatalf("sync must use the sealed access_token, got %v", req["access_token"])
		}
		_, _ = io.WriteString(w, `{
			"added":[
				{"transaction_id":"t-out","amount":12.34,"iso_currency_code":"USD","name":"AWS","merchant_name":"Amazon Web Services","date":"2026-07-05","pending":false},
				{"transaction_id":"t-in","amount":-1203.50,"iso_currency_code":"USD","name":"Customer wire","date":"2026-07-06","pending":false},
				{"transaction_id":"t-pending","amount":9.99,"name":"Pending coffee","date":"2026-07-06","pending":true}
			],
			"modified":[],"removed":[],
			"next_cursor":"cursor-1","has_more":false
		}`)
	}))
	defer srv.Close()

	k := newPlaidKMS()
	k.m[itemIndexRef("acme")] = []byte(`["item-1"]`)
	k.m[itemTokenRef("acme", "item-1")] = []byte("access-BofA")
	pc := newTestPlaid(t, srv.URL, k)

	txns, next, err := pc.Fetch(context.Background(), "acme", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("pending row must be skipped: want 2 settled txns, got %d", len(txns))
	}

	byID := map[string]BankTxn{}
	for _, tx := range txns {
		byID[tx.ExternalID] = tx
	}
	out := byID["t-out"]
	if out.Direction != Outflow || out.AmountCents != 1234 {
		t.Fatalf("positive amount must be an Outflow of 1234c, got dir=%s cents=%d", out.Direction, out.AmountCents)
	}
	if out.Connector != "plaid" || out.Merchant != "Amazon Web Services" {
		t.Fatalf("outflow must carry connector=plaid + merchant, got %+v", out)
	}
	if out.PostedAt != "2026-07-05T00:00:00Z" {
		t.Fatalf("date-only must normalize to RFC3339 midnight UTC, got %q", out.PostedAt)
	}
	in := byID["t-in"]
	if in.Direction != Inflow || in.AmountCents != 120350 {
		t.Fatalf("negative amount must be an Inflow of 120350c, got dir=%s cents=%d", in.Direction, in.AmountCents)
	}
	if next != `{"item-1":"cursor-1"}` {
		t.Fatalf("cursor must advance to the item map, got %q", next)
	}
}

// TestPlaidCursorPagingAndAdvance proves has_more paging drains in one Fetch, the cursor
// advances across pages, and a subsequent sync at the current cursor that returns nothing
// leaves the cursor UNCHANGED (the sync loop then persists nothing).
func TestPlaidCursorPagingAndAdvance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cursor"] {
		case nil, "": // first page
			_, _ = io.WriteString(w, `{"added":[{"transaction_id":"p1","amount":1.00,"date":"2026-07-01","pending":false}],"next_cursor":"c1","has_more":true}`)
		case "c1": // second page
			_, _ = io.WriteString(w, `{"added":[{"transaction_id":"p2","amount":2.00,"date":"2026-07-02","pending":false}],"next_cursor":"c2","has_more":false}`)
		case "c2": // steady state — nothing new
			_, _ = io.WriteString(w, `{"added":[],"modified":[],"next_cursor":"c2","has_more":false}`)
		default:
			t.Fatalf("unexpected cursor %v", req["cursor"])
		}
	}))
	defer srv.Close()

	k := newPlaidKMS()
	k.m[itemIndexRef("acme")] = []byte(`["item-1"]`)
	k.m[itemTokenRef("acme", "item-1")] = []byte("access-BofA")
	pc := newTestPlaid(t, srv.URL, k)

	txns, next, err := pc.Fetch(context.Background(), "acme", "")
	if err != nil {
		t.Fatalf("fetch page 1+2: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("paging must drain both pages in one Fetch, got %d txns", len(txns))
	}
	if next != `{"item-1":"c2"}` {
		t.Fatalf("cursor must advance to c2 after paging, got %q", next)
	}

	// A second sync from the advanced cursor sees nothing → cursor unchanged (no false advance).
	txns2, next2, err := pc.Fetch(context.Background(), "acme", next)
	if err != nil {
		t.Fatalf("steady-state fetch: %v", err)
	}
	if len(txns2) != 0 {
		t.Fatalf("steady state must return no new txns, got %d", len(txns2))
	}
	if next2 != next {
		t.Fatalf("a no-new-data sync must NOT advance the cursor: %q → %q", next, next2)
	}
}

// TestPlaidExchangeSealsTokenNotDB proves Exchange seals the access_token into KMS (keyed per
// org+item, indexed) and that a full Fetch→map→post cycle NEVER writes the token into books.db.
func TestPlaidExchangeSealsTokenNotDB(t *testing.T) {
	const accessToken = "access-SECRET-BofA-do-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/item/public_token/exchange":
			req := readReq(t, r)
			if req["public_token"] != "public-xyz" {
				t.Fatalf("exchange must forward the public_token, got %v", req["public_token"])
			}
			if req["client_id"] != "test-client-id" || req["secret"] != "test-secret" {
				t.Fatalf("exchange must carry KMS-resolved credentials in the body")
			}
			_, _ = io.WriteString(w, `{"access_token":"`+accessToken+`","item_id":"item-boa"}`)
		case "/transactions/sync":
			_, _ = io.WriteString(w, `{"added":[{"transaction_id":"s1","amount":50.00,"iso_currency_code":"USD","name":"Vendor","date":"2026-07-07","pending":false}],"next_cursor":"cz","has_more":false}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	k := newPlaidKMS()
	pc := newTestPlaid(t, srv.URL, k)

	itemID, err := pc.Exchange(ctx, "acme", "public-xyz")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if itemID != "item-boa" {
		t.Fatalf("exchange must return the item id, got %q", itemID)
	}
	// The token is sealed in KMS at the per-org+item ref, and the item is indexed.
	sealed, ok := k.m[itemTokenRef("acme", "item-boa")]
	if !ok || string(sealed) != accessToken {
		t.Fatalf("access_token must be sealed into KMS at the item ref, got ok=%v", ok)
	}
	if idx := string(k.m[itemIndexRef("acme")]); !strings.Contains(idx, "item-boa") {
		t.Fatalf("item must be recorded in the KMS index, got %q", idx)
	}

	// Now pull + post into a REAL per-org books.db, then prove the token is nowhere in it.
	st := newBookStore(t, "books")
	txns, _, err := pc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("fetch after exchange: %v", err)
	}
	if len(txns) != 1 {
		t.Fatalf("want 1 settled txn, got %d", len(txns))
	}
	for _, tx := range txns {
		if strings.Contains(rawJSON(tx), accessToken) {
			t.Fatalf("a BankTxn must never carry the access_token")
		}
		if _, err := mapAndPost(ctx, st, tx); err != nil {
			t.Fatalf("map+post: %v", err)
		}
	}
	assertTokenAbsentFromDB(t, st, accessToken)
}

// TestPlaidIdempotentReSync proves re-syncing the SAME transactions posts nothing the second
// time — the (connector, external_id) guard makes a re-pull a no-op, no double-count.
func TestPlaidIdempotentReSync(t *testing.T) {
	page := `{"added":[{"transaction_id":"dup-1","amount":40.00,"iso_currency_code":"USD","name":"Supplies","date":"2026-07-08","pending":false}],"next_cursor":"c1","has_more":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, page)
	}))
	defer srv.Close()

	ctx := context.Background()
	k := newPlaidKMS()
	k.m[itemIndexRef("acme")] = []byte(`["item-1"]`)
	k.m[itemTokenRef("acme", "item-1")] = []byte("access-BofA")
	pc := newTestPlaid(t, srv.URL, k)
	st := newBookStore(t, "books")

	txns, _, err := pc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	for _, tx := range txns {
		res, err := mapAndPost(ctx, st, tx)
		if err != nil || res.Skipped {
			t.Fatalf("first post must not skip: %+v err=%v", res, err)
		}
	}
	tb1, _ := trialBalance(ctx, st, "", "")

	// Re-pull the identical rows and re-map — every one must skip.
	txns2, _, err := pc.Fetch(ctx, "acme", "")
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	for _, tx := range txns2 {
		res, err := mapAndPost(ctx, st, tx)
		if err != nil || !res.Skipped {
			t.Fatalf("re-mapping the same txn must skip, got %+v err=%v", res, err)
		}
	}
	tb2, _ := trialBalance(ctx, st, "", "")
	if tb2.TotalDebit != tb1.TotalDebit || tb2.TotalCredit != tb1.TotalCredit {
		t.Fatalf("idempotent re-sync changed the books: %d/%d vs %d/%d",
			tb2.TotalDebit, tb2.TotalCredit, tb1.TotalDebit, tb1.TotalCredit)
	}
}

// TestPlaidLinkToken proves link-token creation forwards the org as client_user_id, requests
// the transactions product, and returns the token from a mocked /link/token/create.
func TestPlaidLinkToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/link/token/create" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		req := readReq(t, r)
		if req["client_id"] != "test-client-id" || req["secret"] != "test-secret" {
			t.Fatalf("link-token must carry KMS-resolved credentials")
		}
		user, _ := req["user"].(map[string]any)
		if user["client_user_id"] != "acme" {
			t.Fatalf("link-token must scope to the org, got %v", user["client_user_id"])
		}
		_, _ = io.WriteString(w, `{"link_token":"link-sandbox-123","expiration":"2026-07-21T00:00:00Z"}`)
	}))
	defer srv.Close()

	k := newPlaidKMS()
	pc := newTestPlaid(t, srv.URL, k)
	tok, exp, err := pc.LinkToken(context.Background(), "acme")
	if err != nil {
		t.Fatalf("link token: %v", err)
	}
	if tok != "link-sandbox-123" || exp == "" {
		t.Fatalf("link-token must be returned, got tok=%q exp=%q", tok, exp)
	}
}

// TestPlaidUnlinkedOrgPullsNothing proves an org that has never linked a bank pulls nothing and
// does not error (the sync loop tolerates it), and never contacts Plaid.
func TestPlaidUnlinkedOrgPullsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Fatalf("an unlinked org must never call Plaid, but hit %s", r.URL.Path)
	}))
	defer srv.Close()

	pc := newTestPlaid(t, srv.URL, newPlaidKMS())
	txns, next, err := pc.Fetch(context.Background(), "acme", "")
	if err != nil || len(txns) != 0 || next != "" {
		t.Fatalf("unlinked org must pull nothing cleanly, got txns=%d next=%q err=%v", len(txns), next, err)
	}
}

// assertTokenAbsentFromDB scans every text column of bank_txn (including the raw audit blob)
// and proves the access_token appears nowhere in the org's books.db.
func assertTokenAbsentFromDB(t *testing.T, st *store, token string) {
	t.Helper()
	rows, err := st.db.QueryContext(context.Background(),
		`SELECT connector, external_id, description, merchant, raw FROM bank_txn`)
	if err != nil {
		t.Fatalf("scan bank_txn: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var connector, ext, desc, merch, raw string
		if err := rows.Scan(&connector, &ext, &desc, &merch, &raw); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		for _, col := range []string{connector, ext, desc, merch, raw} {
			if strings.Contains(col, token) {
				t.Fatalf("access_token leaked into books.db column: %q", col)
			}
		}
	}
}
