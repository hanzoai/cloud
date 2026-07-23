package metering_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/clients/metering"
)

// commerceStub serves balance (GET) and records usage (POST), capturing the
// recorded amount so the test can assert post-request metering happened.
type commerceStub struct {
	available int64

	mu          sync.Mutex
	recordedAmt int64
	recordedCnt int
	recordedUsr string
}

func (s *commerceStub) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.mu.Lock()
			defer s.mu.Unlock()
			body, _ := io.ReadAll(r.Body)
			// crude parse: amount + user
			s.recordedCnt++
			s.recordedAmt = extractInt(body, `"amount":`)
			s.recordedUsr = extractStr(body, `"user":"`)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"transactionId":"tx","type":"withdraw"}`)
			return
		}
		// balance
		s.mu.Lock()
		avail := s.available
		s.mu.Unlock()
		_, _ = io.WriteString(w, `{"available":`+itoa(avail)+`}`)
	}))
}

func (s *commerceStub) records() (int, int64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordedCnt, s.recordedAmt, s.recordedUsr
}

// setAvailable simulates a pre-pay deposit landing (or exhaustion): it moves the
// spendable balance the gate reads before the next request.
func (s *commerceStub) setAvailable(v int64) {
	s.mu.Lock()
	s.available = v
	s.mu.Unlock()
}

func gatewayReq() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/search?q=foo", nil)
	r.Header.Set(metering.HeaderOrgID, "hanzo")
	r.Header.Set(metering.HeaderUserID, "alice")
	return r
}

// TestMiddleware_PrePayLifecycle is the pre-pay business model end to end: a freshly
// provisioned org starts at a ZERO balance, so a metered request is REFUSED (402, no
// free floor, nothing recorded); after a pre-pay deposit lands, the SAME request is
// SERVED and DEBITS the balance; when the balance is later exhausted, it is refused
// again. No trial credit anywhere — usage is pre-paid.
func TestMiddleware_PrePayLifecycle(t *testing.T) {
	stub := &commerceStub{available: 0} // fresh provisioned org: zero balance
	srv := stub.server()
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})
	served := 0
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "enso",
		Price: func(_ *http.Request, status int, _ metering.AuthInput) int64 {
			if status == http.StatusOK {
				return 7
			}
			return 0
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))

	// 1) ZERO balance → REFUSED (402); handler never runs; nothing recorded.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("zero-balance request: status=%d, want 402", rr.Code)
	}
	if served != 0 {
		t.Fatalf("zero-balance request must NOT be served (no free floor)")
	}
	if n, _, _ := stub.records(); n != 0 {
		t.Fatalf("refused request must not debit, got %d records", n)
	}

	// 2) A pre-pay deposit lands → positive balance → SERVED + DEBITS.
	stub.setAvailable(5000)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())
	if rr.Code != 200 || served != 1 {
		t.Fatalf("after pre-pay: status=%d served=%d, want 200 served=1", rr.Code, served)
	}
	waitFor(t, func() bool { n, _, _ := stub.records(); return n == 1 })
	if _, amt, _ := stub.records(); amt != 7 {
		t.Fatalf("served request must debit the balance, got amount=%d want 7", amt)
	}

	// 3) Balance exhausted → refused again (no free floor after the credit runs out).
	stub.setAvailable(0)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("exhausted-balance request: status=%d, want 402", rr.Code)
	}
	if served != 1 {
		t.Fatalf("exhausted request must NOT be served, served=%d want 1", served)
	}
}

func TestMiddleware_GatesAndRecords(t *testing.T) {
	stub := &commerceStub{available: 5000}
	srv := stub.server()
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})

	handlerHit := false
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "search",
		Price: func(_ *http.Request, status int, _ metering.AuthInput) int64 {
			if status == http.StatusOK {
				return 7 // 7 cents per successful search
			}
			return 0
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerHit = true
		w.WriteHeader(200)
		_, _ = w.Write([]byte("results"))
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())

	if !handlerHit {
		t.Fatal("handler should have run (balance positive)")
	}
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// Record happens async; poll briefly.
	waitFor(t, func() bool { n, _, _ := stub.records(); return n == 1 })
	n, amt, usr := stub.records()
	if n != 1 || amt != 7 {
		t.Fatalf("recorded (count=%d amount=%d), want (1, 7)", n, amt)
	}
	// gatewayReq is a person in the SHARED SIGNUP org, who holds their own account:
	// its members are strangers, not a team, so a shared org is not a shared wallet.
	// This asserted "hanzo" — the org pool — while ai's gate debited "hanzo/alice",
	// which is the funded-pool-then-402 split. The account is whatever the one rule
	// says, so assert against the rule rather than restating a premise it disproved.
	want := account.Payer(account.Credential{Owner: "hanzo", Name: "alice"}).Subject()
	if usr != want {
		t.Errorf("recorded user = %q, want %q (the account the shared rule resolves)", usr, want)
	}
}

func TestMiddleware_Denies402_WhenNoBalance(t *testing.T) {
	stub := &commerceStub{available: 0}
	srv := stub.server()
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})
	handlerHit := false
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "search",
		Price:    func(*http.Request, int, metering.AuthInput) int64 { return 7 },
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerHit = true }))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())

	if handlerHit {
		t.Fatal("handler must NOT run when balance is zero")
	}
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rr.Code)
	}
	if n, _, _ := stub.records(); n != 0 {
		t.Fatalf("denied request must not record usage, got %d records", n)
	}
}

// A FUNDED caller over a per-scope spend cap must get a DISTINCT 402
// spend_cap_exceeded (the balance is fine; the tenant's own ceiling is not) — NOT
// the 503 the pre-fix defaultOnDenied fell through to, and NOT insufficient_balance.
func TestMiddleware_Denies402_SpendCap_WhenFundedButOverCap(t *testing.T) {
	// Balance is healthy; the authorize endpoint returns the spend_cap verdict.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing/spend-alerts/authorize" {
			_, _ = io.WriteString(w, `{"allow":false,"reason":"spend_cap","capCents":100,"spentCents":100}`)
			return
		}
		_, _ = io.WriteString(w, `{"available":100000}`) // funded
	}))
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})
	handlerHit := false
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "search",
		Price:    func(*http.Request, int, metering.AuthInput) int64 { return 7 },
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerHit = true }))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())

	if handlerHit {
		t.Fatal("handler must NOT run when over the spend cap")
	}
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (spend_cap is a 402, not a 503)", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "spend_cap_exceeded") {
		t.Fatalf("body = %q, want spend_cap_exceeded code (distinct from insufficient_balance)", body)
	}
}

func TestMiddleware_FailClosed503_WhenCommerceDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "search",
		Price:    func(*http.Request, int, metering.AuthInput) int64 { return 7 },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when balance is unknown (fail-closed)")
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail-closed)", rr.Code)
	}
}

func TestMiddleware_Skip_Bypasses(t *testing.T) {
	stub := &commerceStub{available: 0} // would deny if gated
	srv := stub.server()
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})
	handlerHit := false
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "search",
		Price:    func(*http.Request, int, metering.AuthInput) int64 { return 7 },
		Skip:     func(r *http.Request) bool { return r.URL.Path == "/healthz" },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerHit = true
		w.WriteHeader(200)
	}))

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(rr, r)

	if !handlerHit {
		t.Fatal("skipped path must run handler without gating")
	}
	if n, _, _ := stub.records(); n != 0 {
		t.Fatal("skipped path must not record usage")
	}
}

func TestMiddleware_OnlyChargesSuccess(t *testing.T) {
	stub := &commerceStub{available: 5000}
	srv := stub.server()
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})
	h := c.Middleware(metering.MiddlewareConfig{
		Provider: "search",
		Price: func(_ *http.Request, status int, _ metering.AuthInput) int64 {
			if status == http.StatusOK {
				return 7
			}
			return 0 // don't charge for failures
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // handler failed
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, gatewayReq())
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	// Give any (erroneous) async record a chance, then assert none happened.
	time.Sleep(50 * time.Millisecond)
	if n, _, _ := stub.records(); n != 0 {
		t.Fatalf("failed request must not be charged, got %d records", n)
	}
}

func TestIdentityFromGatewayHeaders_PerOrgBillingKey(t *testing.T) {
	// Prepaid billing is per-org: the billing key (User) is the ORG slug, the
	// full org/sub is recorded as Actor for audit. Keying per-user would gate
	// an empty per-user ledger and deny a funded org (the bug this guards).
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(metering.HeaderOrgID, "zoo")
	r.Header.Set(metering.HeaderUserID, "bob")
	in := metering.IdentityFromGatewayHeaders(r)
	if in.User != "zoo" {
		t.Errorf("User (billing key) = %q, want zoo (org slug, per-org billing)", in.User)
	}
	if in.Actor != "zoo/bob" {
		t.Errorf("Actor (audit) = %q, want zoo/bob", in.Actor)
	}
	if in.Org != "zoo" {
		t.Errorf("Org = %q, want zoo", in.Org)
	}
}

func TestIdentityFromGatewayHeaders_OrgLessFallback(t *testing.T) {
	// No org header (org-less token): fall back to the bare sub so a per-user
	// balance can still gate.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(metering.HeaderUserID, "solo")
	in := metering.IdentityFromGatewayHeaders(r)
	if in.User != "solo" {
		t.Errorf("org-less User = %q, want solo", in.User)
	}
	if in.Actor != "solo" {
		t.Errorf("org-less Actor = %q, want solo", in.Actor)
	}
}

// ---- tiny helpers (avoid pulling strconv/fmt into hot asserts) ----

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func extractInt(body []byte, key string) int64 {
	s := string(body)
	idx := indexOf(s, key)
	if idx < 0 {
		return 0
	}
	idx += len(key)
	var n int64
	for idx < len(s) && (s[idx] == ' ') {
		idx++
	}
	neg := false
	if idx < len(s) && s[idx] == '-' {
		neg = true
		idx++
	}
	for idx < len(s) && s[idx] >= '0' && s[idx] <= '9' {
		n = n*10 + int64(s[idx]-'0')
		idx++
	}
	if neg {
		return -n
	}
	return n
}

func extractStr(body []byte, key string) string {
	s := string(body)
	idx := indexOf(s, key)
	if idx < 0 {
		return ""
	}
	idx += len(key)
	end := idx
	for end < len(s) && s[end] != '"' {
		end++
	}
	return s[idx:end]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var _ = context.Background
