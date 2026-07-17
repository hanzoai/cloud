package payout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDepositPostsGrant proves Deposit posts POST /v1/billing/deposit with the
// service token + X-Org-Id namespace and the body the commerce ledger expects,
// and returns the transactionId. This is the ONE money-in path the three credit
// programs share.
func TestDepositPostsGrant(t *testing.T) {
	var gotOrg, gotAuth, gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotOrg = r.Header.Get("X-Org-Id")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"transactionId": "txn_123"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "svc-tok")
	if !c.Configured() {
		t.Fatal("client with base+token must be Configured")
	}
	txn, err := c.Deposit(context.Background(), "acme", "acme", 500, "", "welcome", "grant:referral")
	if err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	if txn != "txn_123" {
		t.Fatalf("txn = %q, want txn_123", txn)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/billing/deposit" {
		t.Fatalf("request = %s %s, want POST /v1/billing/deposit", gotMethod, gotPath)
	}
	if gotOrg != "acme" {
		t.Fatalf("X-Org-Id = %q, want acme", gotOrg)
	}
	if gotAuth != "Bearer svc-tok" {
		t.Fatalf("Authorization = %q, want Bearer svc-tok", gotAuth)
	}
	// currency defaults to usd; amount is the cents int; tag carries the program's grant class.
	if gotBody["currency"] != "usd" || gotBody["amount"].(float64) != 500 || gotBody["tags"] != "grant:referral" {
		t.Fatalf("body = %v, want currency=usd amount=500 tags=grant:referral", gotBody)
	}
}

// TestSpendCentsReadsRollup proves SpendCents reads GET /v1/billing/usage-rollup
// (the accrual base) and returns consumedCents.
func TestSpendCentsReadsRollup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/billing/usage-rollup" {
			t.Errorf("request = %s %s, want GET /v1/billing/usage-rollup", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("user") != "acme" {
			t.Errorf("user query = %q, want acme", r.URL.Query().Get("user"))
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"consumedCents": 4200})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "svc-tok")
	got, err := c.SpendCents(context.Background(), "acme", "acme")
	if err != nil {
		t.Fatalf("SpendCents: %v", err)
	}
	if got != 4200 {
		t.Fatalf("consumedCents = %d, want 4200", got)
	}
}

// TestUnconfigured proves the fail-soft contract: an unwired client returns
// ErrUnconfigured from Deposit (an honest failure, never a phantom grant) and 0
// from SpendCents (degrades to "no spend yet", not a 5xx).
func TestUnconfigured(t *testing.T) {
	c := NewClient("", "") // no base, no token
	if c.Configured() {
		t.Fatal("client with empty base/token must NOT be Configured")
	}
	if _, err := c.Deposit(context.Background(), "acme", "acme", 100, "usd", "", "grant:author"); err != ErrUnconfigured {
		t.Fatalf("Deposit err = %v, want ErrUnconfigured", err)
	}
	got, err := c.SpendCents(context.Background(), "acme", "acme")
	if err != nil || got != 0 {
		t.Fatalf("SpendCents = (%d, %v), want (0, nil)", got, err)
	}
}

// TestNon2xxIsError proves a commerce 5xx surfaces as an error (the caller then
// records a failed payout / retries), never a silent success.
func TestNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "svc-tok")
	if _, err := c.Deposit(context.Background(), "acme", "acme", 100, "usd", "", "grant:affiliate"); err == nil {
		t.Fatal("Deposit against a 502 must return an error")
	}
}
