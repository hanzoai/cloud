package ledger_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk/meter"
)

// fakeCommerce records the last request and replies with a canned status+body.
type fakeCommerce struct {
	mu      sync.Mutex
	method  string
	path    string
	query   url.Values
	auth    string
	org     string
	testHdr string
	ctype   string
	body    []byte
	status  int
	reply   string
}

func (f *fakeCommerce) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		// Authorize also consults the per-scope spend-cap endpoint after funds pass
		// (issue #70). These tests pin the FUNDS contract (balance/tier/usage), so
		// answer the scope call with the canned body but don't record it as the
		// asserted request.
		if strings.HasPrefix(r.URL.Path, "/v1/billing/alerts") {
			if f.status != 0 {
				w.WriteHeader(f.status)
			}
			_, _ = io.WriteString(w, f.reply)
			return
		}
		f.method = r.Method
		f.path = r.URL.Path
		f.query = r.URL.Query()
		f.auth = r.Header.Get("Authorization")
		f.org = r.Header.Get("X-Org-Id")
		f.testHdr = r.Header.Get("X-Hanzo-Test")
		f.ctype = r.Header.Get("Content-Type")
		f.body, _ = io.ReadAll(r.Body)
		if f.status == 0 {
			f.status = 200
		}
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.reply)
	}
}

func newClient(t *testing.T, srv *httptest.Server, cfg meter.Config) *meter.Client {
	t.Helper()
	cfg.BaseURL = srv.URL
	c, err := meter.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestAuthorize_Allows_WhenAvailablePositive(t *testing.T) {
	fc := &fakeCommerce{reply: `{"user":"hanzo/alice","currency":"usd","balance":5000,"holds":0,"available":5000}`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := newClient(t, srv, meter.Config{})
	if err := c.Authorize(context.Background(), meter.AuthInput{User: "hanzo/alice", Org: "hanzo"}); err != nil {
		t.Fatalf("Authorize allowed should be nil, got %v", err)
	}

	// Verify the exact commerce contract.
	if fc.method != http.MethodGet {
		t.Errorf("method = %s, want GET", fc.method)
	}
	if fc.path != "/v1/billing/balance" {
		t.Errorf("path = %s, want /v1/billing/balance", fc.path)
	}
	if got := fc.query.Get("user"); got != "hanzo/alice" {
		t.Errorf("user query = %q, want hanzo/alice", got)
	}
	if got := fc.query.Get("currency"); got != "usd" {
		t.Errorf("currency query = %q, want usd", got)
	}
	if fc.auth != "" {
		t.Errorf("auth = %q, want no bearer — identity rides the transport, not a secret", fc.auth)
	}
	if fc.org != "hanzo" {
		t.Errorf("X-Org-Id = %q, want hanzo", fc.org)
	}
}

func TestAuthorize_Denies_WhenAvailableZero(t *testing.T) {
	fc := &fakeCommerce{reply: `{"available":0}`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := newClient(t, srv, meter.Config{})
	err := c.Authorize(context.Background(), meter.AuthInput{User: "hanzo/alice", Org: "hanzo"})
	if err != meter.ErrInsufficientBalance {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
}

func TestAuthorize_FailClosed_OnCommerceError(t *testing.T) {
	fc := &fakeCommerce{status: 500, reply: `boom`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := newClient(t, srv, meter.Config{})
	err := c.Authorize(context.Background(), meter.AuthInput{User: "hanzo/alice", Org: "hanzo"})
	if err == nil {
		t.Fatal("fail-closed: commerce 500 must deny, got nil")
	}
	if err == meter.ErrInsufficientBalance {
		t.Fatal("a 500 is 'unknown', not 'insufficient' — must be a connectivity error")
	}
}

func TestAuthorize_NotConfigured_Allows(t *testing.T) {
	c, err := meter.New(meter.Config{}) // no BaseURL
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Enabled() {
		t.Fatal("client with no BaseURL should report Enabled()=false")
	}
	if err := c.Authorize(context.Background(), meter.AuthInput{User: "hanzo/alice", Org: "hanzo"}); err != nil {
		t.Fatalf("not-configured Authorize must allow, got %v", err)
	}
}

func TestLiveMode_OmitsTestHeader(t *testing.T) {
	fc := &fakeCommerce{reply: `{"available":1}`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := newClient(t, srv, meter.Config{}) // no client writes the sandbox books
	if err := c.Authorize(context.Background(), meter.AuthInput{User: "hanzo/alice", Org: "hanzo"}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if fc.testHdr != "" {
		t.Errorf("Live mode must NOT send X-Hanzo-Test (would write the wrong ledger), got %q", fc.testHdr)
	}
}

func TestAuthorize_PerCallOrgOverride(t *testing.T) {
	fc := &fakeCommerce{reply: `{"available":1}`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := newClient(t, srv, meter.Config{}) // default org hanzo
	_ = c.Authorize(context.Background(), meter.AuthInput{User: "zoo/bob", Org: "zoo"})
	if fc.org != "zoo" {
		t.Errorf("per-call org override: X-Org-Id = %q, want zoo", fc.org)
	}
}

// THE SPLIT-DEPLOY DEBIT CROSSES THE PLANE, AND THE ACT'S NAME CROSSES WITH IT.
//
// THE BUG. This debit used to be a JSON POST to commerce's /v1/billing/usage, and
// [meter.Usage.Ref] — the ledger's idempotency key — is tagged `json:"-"`, deliberately,
// so that no request body anywhere can set it. json.Marshal therefore dropped it and every
// debit arrived at the ledger ANONYMOUS. The contract [meter.Usage.Seal] states is that
// a caller which must survive its own retry seals the act first and re-sends the SAME
// value; over HTTP that value never left the process, so the retry was a second charge to
// a real customer. The co-resident path held the contract and the split-deploy path
// silently did not.
//
// THE PROPERTY. The debit goes to the process that owns the ledger over the internal
// plane, where the act's name is a field of its own (client.Usage.Ref) — so the same act
// re-sent carries the same key, and the billed ORG still comes from the caller rather than
// the argument. Everything the HTTP contract asserted is asserted here on the crossing
// that replaced it.
//
// MUTATION PROOF: drop `Ref: u.Ref` from the client.Usage that Record builds and the sealed
// act crosses nameless — exactly the hole the HTTP body had — and this test fails on it.
func TestRecord_CrossesThePlaneCarryingTheAct(t *testing.T) {
	peer := (&planeCommerce{}).serve(t)

	c := newClient(t, httptest.NewServer(http.NotFoundHandler()), meter.Config{})
	act := meter.Usage{
		User:        "hanzo/alice",
		Org:         "hanzo",
		AmountCents: 250,
		Provider:    "search",
		Model:       "zen",
		Project:     "p1",
		Service:     "search",
		RequestID:   "req-9",
		ClientIP:    "203.0.113.7",
		Status:      "success",
	}.Seal()
	if act.Ref == "" {
		t.Fatal("Seal minted no act name")
	}

	res, err := c.Record(context.Background(), act)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if res == nil || res.Amount != 250 || res.User != "hanzo/alice" || res.Type != "withdraw" {
		t.Fatalf("unexpected RecordResult: %+v", res)
	}

	got := peer.last(t)
	// The billed ORG rides the caller, never the argument — the same rule the header
	// carried, on the transport that replaced it.
	if got.org != "hanzo" {
		t.Errorf("billed org = %q, want hanzo", got.org)
	}
	if got.in.Subject != "hanzo/alice" {
		t.Errorf("subject = %q, want hanzo/alice", got.in.Subject)
	}
	if cents, cerr := got.in.Amount.Minor(); cerr != nil || cents != 250 {
		t.Errorf("amount = %+v (%v¢, err %v), want 250¢", got.in.Amount, cents, cerr)
	}
	if got.in.Amount.Currency != "USD" {
		t.Errorf("currency = %q, want USD (defaulted)", got.in.Amount.Currency)
	}
	if got.in.Usage.Provider != "search" || got.in.Usage.Model != "zen" ||
		got.in.Usage.Project != "p1" || got.in.Usage.Service != "search" {
		t.Errorf("attribution did not survive: %+v", got.in.Usage)
	}
	// THE KEY, and the correlation id, as two different things.
	if got.in.Usage.Ref != act.Ref {
		t.Errorf("the act's name did not cross: ref = %q, want %q", got.in.Usage.Ref, act.Ref)
	}
	if got.in.Usage.RequestID != "req-9" {
		t.Errorf("requestId = %q, want req-9", got.in.Usage.RequestID)
	}
	if got.in.Usage.ClientIP != "203.0.113.7" {
		t.Errorf("clientIp = %q", got.in.Usage.ClientIP)
	}
}

// The retry the sealed key exists for: ONE act re-sent three times crosses three times
// under ONE name, so the ledger at the far end debits it once. Over the old HTTP body the
// three crossings were anonymous and the far end had no way to tell them apart.
func TestRecord_ASealedActKeepsItsNameAcrossItsOwnRetry(t *testing.T) {
	peer := (&planeCommerce{}).serve(t)
	c := newClient(t, httptest.NewServer(http.NotFoundHandler()), meter.Config{})

	act := meter.Usage{User: "hanzo/alice", Org: "hanzo", AmountCents: 30, Model: "zen"}.Seal()
	for attempt := range 3 {
		if _, err := c.Record(context.Background(), act); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	calls := peer.all()
	if len(calls) != 3 {
		t.Fatalf("crossings = %d, want 3", len(calls))
	}
	for i, call := range calls {
		if call.in.Usage.Ref != act.Ref {
			t.Fatalf("crossing %d carried ref %q, want the act's own %q — the far end cannot "+
				"dedup what it cannot name", i, call.in.Usage.Ref, act.Ref)
		}
	}

	// And two DIFFERENT acts are two names, so they bill twice however identical the rest.
	seen := map[string]bool{}
	for range 2 {
		if _, err := c.Record(context.Background(), meter.Usage{
			User: "hanzo/alice", Org: "hanzo", AmountCents: 30, Model: "zen",
		}); err != nil {
			t.Fatalf("distinct act: %v", err)
		}
	}
	for _, call := range peer.all()[3:] {
		if call.in.Usage.Ref == "" || call.in.Usage.Ref == act.Ref || seen[call.in.Usage.Ref] {
			t.Fatalf("a distinct act reused a name: %q", call.in.Usage.Ref)
		}
		seen[call.in.Usage.Ref] = true
	}
}

func TestRecord_ZeroAmount_IsNoOp(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	c := newClient(t, srv, meter.Config{})
	res, err := c.Record(context.Background(), meter.Usage{User: "hanzo/alice", Org: "hanzo", AmountCents: 0})
	if err != nil || res != nil {
		t.Fatalf("zero-amount Record should be (nil,nil), got (%v,%v)", res, err)
	}
	if called {
		t.Fatal("zero-amount Record must not call commerce")
	}
}

func TestRecord_NotConfigured_IsNoOp(t *testing.T) {
	c, _ := meter.New(meter.Config{})
	res, err := c.Record(context.Background(), meter.Usage{User: "hanzo/alice", Org: "hanzo", AmountCents: 100})
	if err != nil || res != nil {
		t.Fatalf("not-configured Record should be (nil,nil), got (%v,%v)", res, err)
	}
}

func TestNew_RejectsBadURL(t *testing.T) {
	if _, err := meter.New(meter.Config{BaseURL: "://bad"}); err == nil {
		t.Fatal("expected error for unparseable BaseURL")
	}
}

// TestContractMatchesGateway pins the wire format the gateway already uses, so
// this client and the gateway gate on the identical balance source.
func TestContractMatchesGateway(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture ONLY the balance request. Authorize also consults the per-scope
		// spend-cap endpoint after funds pass (issue #70); this test pins the
		// balance contract, so ignore the follow-on /alerts/authorize call.
		if strings.HasPrefix(r.URL.Path, "/v1/billing/balance") {
			gotURL = r.URL.String()
		}
		_, _ = io.WriteString(w, `{"available":1}`)
	}))
	defer srv.Close()

	c := newClient(t, srv, meter.Config{})
	_ = c.Authorize(context.Background(), meter.AuthInput{User: "hanzo/alice", Org: "hanzo"})

	// Gateway: GET {base}/v1/billing/balance?user=hanzo%2Falice&currency=usd
	if !strings.HasPrefix(gotURL, "/v1/billing/balance?") {
		t.Fatalf("URL %q must start with /v1/billing/balance?", gotURL)
	}
	if !strings.Contains(gotURL, "user=hanzo%2Falice") {
		t.Errorf("URL %q must url-encode the user as hanzo%%2Falice", gotURL)
	}
	if !strings.Contains(gotURL, "currency=usd") {
		t.Errorf("URL %q must carry currency=usd", gotURL)
	}
}
