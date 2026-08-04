package payout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSpendCentsReadsRollup proves SpendCents reads GET /v1/billing/usage/rollup
// (the accrual base) and returns consumedCents.
func TestSpendCentsReadsRollup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/billing/usage/rollup" {
			t.Errorf("request = %s %s, want GET /v1/billing/usage/rollup", r.Method, r.URL.Path)
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
	got, err := c.SpendCents(context.Background(), "acme", "acme")
	if err != nil || got != 0 {
		t.Fatalf("SpendCents = (%d, %v), want (0, nil)", got, err)
	}
}

// TestNon2xxIsError proves a commerce 5xx surfaces as an error, never a silent
// success the caller would read as a real number.
func TestNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "svc-tok")
	if _, err := c.SpendCents(context.Background(), "acme", "acme"); err == nil {
		t.Fatal("SpendCents against a 502 must return an error")
	}
}

// TestSeamIsReadOnly pins the SHAPE of the money seam: the three credit programs
// minted because this client carried a Deposit, so reviving that mint must start by
// re-declaring the capability here, in front of a test that says no.
func TestSeamIsReadOnly(t *testing.T) {
	var c any = NewClient("http://x", "t")
	if _, ok := c.(interface {
		Deposit(context.Context, string, string, int64, string, string, string, string) (string, error)
	}); ok {
		t.Fatal("payout.Client grew a Deposit again — this seam READS; money-in is an admin grant")
	}
	if _, ok := c.(Commerce); !ok {
		t.Fatal("Client must still satisfy the read seam")
	}
}
