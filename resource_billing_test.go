package cloud

// Tests for the shared per-org resource gate+meter (ResourceMeter). They drive
// the real metering client against a fake commerce server that RECORDS the
// X-Org-Id tenant header and request bodies, so the multitenancy contract is
// proven end-to-end over HTTP — no mock of the metering client itself. The fake
// is built with a metering DEFAULT org of "hanzo"; every assertion that the
// caller "acme" is billed (not "hanzo") proves the per-call org override is what
// scopes the ledger — i.e. a caller can never be billed to a default or another
// tenant.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// recCommerce answers the metering client's balance + usage calls and records,
// per endpoint, the X-Org-Id tenant header it saw plus the usage body — the
// evidence for the per-org / cross-tenant assertions.
type recCommerce struct {
	balanceAvailable int64 // returned as {"available":N} on GET /v1/billing/balance
	balanceStatus    int   // 0 => 200

	mu           sync.Mutex
	balanceOrg   string // last X-Org-Id on a balance call
	balanceCalls int32
	usageOrg     string // last X-Org-Id on a usage call
	usageCount   int32
	usageBody    []byte
}

func (f *recCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.balanceCalls, 1)
		f.mu.Lock()
		f.balanceOrg = r.Header.Get("X-Org-Id")
		f.mu.Unlock()
		status := f.balanceStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"available": f.balanceAvailable})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.usageCount, 1)
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.usageOrg = r.Header.Get("X-Org-Id")
		f.usageBody = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *recCommerce) usages() int32   { return atomic.LoadInt32(&f.usageCount) }
func (f *recCommerce) balances() int32 { return atomic.LoadInt32(&f.balanceCalls) }
func (f *recCommerce) lastBalanceOrg() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balanceOrg
}
func (f *recCommerce) lastUsage() (string, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usageOrg, f.usageBody
}

// meterFor builds a ResourceMeter whose metering client has DEFAULT org "hanzo"
// (so a caller-org assertion proves the per-call override), at the given env.
func meterFor(t *testing.T, baseURL, env string, failOpen bool) *ResourceMeter {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: baseURL, Token: "svc-token", Org: "hanzo", FailOpen: failOpen})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return NewResourceMeter(Deps{Logger: luxlog.New("test"), Metering: m, Env: env}, "provisioning")
}

// Funded org (balance>0), priced kind → Gate allows, and the balance check
// targeted the CALLER org, not the client default.
func TestResourceMeter_GateAllowsFundedCallerOrg(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "acme", "", "sql", 100); err != nil {
		t.Fatalf("Gate(funded) = %v, want nil", err)
	}
	if got := fc.lastBalanceOrg(); got != "acme" {
		t.Fatalf("balance checked org %q, want caller %q (per-call org must override the client default 'hanzo')", got, "acme")
	}
}

// Out of funds (balance<=0) → Gate denies with ErrInsufficientBalance (→ 402).
func TestResourceMeter_GateRefusesAtZero(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 0}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "acme", "", "sql", 100); err != metering.ErrInsufficientBalance {
		t.Fatalf("Gate(zero balance) = %v, want ErrInsufficientBalance", err)
	}
}

// A free kind (cost 0) is un-gated AND never calls commerce (mirrors the edge
// gate's price==0 short-circuit).
func TestResourceMeter_GateFreeKindNoCommerceCall(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 0}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "acme", "", "sql", 0); err != nil {
		t.Fatalf("Gate(free kind) = %v, want nil", err)
	}
	if n := fc.balances(); n != 0 {
		t.Fatalf("commerce balance called %d times for a free kind, want 0", n)
	}
}

// Commerce unreachable/5xx + fail-CLOSED (default) → Gate denies with a non-
// ErrInsufficientBalance error (→ 503). The whole margin-protection point: a
// billing outage must NOT yield free provisioning.
func TestResourceMeter_GateFailClosedOnCommerceError(t *testing.T) {
	fc := &recCommerce{balanceStatus: http.StatusInternalServerError}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	err := rm.Gate(t.Context(), "acme", "", "sql", 100)
	if err == nil {
		t.Fatal("Gate(commerce 5xx, fail-closed) = nil, want a deny error (no free provisioning on outage)")
	}
	if err == metering.ErrInsufficientBalance {
		t.Fatal("Gate(commerce 5xx) returned ErrInsufficientBalance, want a balance-unknown error (→ 503, not 402)")
	}
}

// Commerce unreachable/5xx + fail-OPEN → Gate allows (availability over billing).
func TestResourceMeter_GateFailOpenOnCommerceError(t *testing.T) {
	fc := &recCommerce{balanceStatus: http.StatusInternalServerError}
	rm := meterFor(t, fc.server(t).URL, "mainnet", true /* fail-open */)

	if err := rm.Gate(t.Context(), "acme", "", "sql", 100); err != nil {
		t.Fatalf("Gate(commerce 5xx, fail-open) = %v, want nil", err)
	}
}

// Meter debits the CALLER org: usage POST fires once, carries X-Org-Id:acme
// (not the default 'hanzo'), body user=="acme", amount==cost. This is the
// per-org / anti-cross-tenant debit proof.
func TestResourceMeter_MeterDebitsCallerOrg(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	rm.Meter("acme", "", "sql", 250, "req-1", "203.0.113.7")

	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("usage records = %d, want 1 (Meter must debit on success)", fc.usages())
	}
	org, body := fc.lastUsage()
	if org != "acme" {
		t.Fatalf("usage billed org %q, want caller %q (must override client default 'hanzo')", org, "acme")
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("usage body decode: %v (body=%s)", err, body)
	}
	if u.User != "acme" {
		t.Fatalf("usage user = %q, want caller org %q (per-org billing keys on the org slug)", u.User, "acme")
	}
	if u.Amount != 250 {
		t.Fatalf("usage amount = %d, want 250", u.Amount)
	}
	if u.Provider != "provisioning" {
		t.Fatalf("usage provider = %q, want %q", u.Provider, "provisioning")
	}
}

// A second tenant draws on ITS OWN ledger: gating "globex" must check globex,
// never "acme" or the default. Proves no cross-tenant fold in the gate path.
func TestResourceMeter_GateIsolatesTenants(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "globex", "", "vector", 100); err != nil {
		t.Fatalf("Gate(globex) = %v, want nil", err)
	}
	if got := fc.lastBalanceOrg(); got != "globex" {
		t.Fatalf("balance checked org %q, want %q — cross-tenant billing leak", got, "globex")
	}
}

// Env never bypasses billing: on testnet AND devnet, a zero balance still
// refuses. test/dev are sandbox-but-billed, not free.
func TestResourceMeter_EnvNeverBypassesGate(t *testing.T) {
	for _, env := range []string{"testnet", "devnet"} {
		fc := &recCommerce{balanceAvailable: 0}
		rm := meterFor(t, fc.server(t).URL, env, false)
		if err := rm.Gate(t.Context(), "acme", "", "sql", 100); err != metering.ErrInsufficientBalance {
			t.Fatalf("env=%s: Gate(zero) = %v, want ErrInsufficientBalance (test/dev must still bill)", env, err)
		}
	}
}

// An unconfigured metering client (no commerce URL) makes the gate a no-op: Gate
// allows, Meter does nothing, Enabled() is false.
func TestResourceMeter_UnconfiguredIsNoop(t *testing.T) {
	m, _ := metering.New(metering.Config{}) // no BaseURL
	rm := NewResourceMeter(Deps{Logger: luxlog.New("test"), Metering: m}, "provisioning")
	if rm.Enabled() {
		t.Fatal("ResourceMeter with empty commerce URL must not be Enabled()")
	}
	if err := rm.Gate(t.Context(), "acme", "", "sql", 100); err != nil {
		t.Fatalf("Gate(unconfigured) = %v, want nil (no-op)", err)
	}
	rm.Meter("acme", "", "sql", 100, "r", "") // must not panic
}

// A nil ResourceMeter and a meter with a nil client are safe no-ops (defensive:
// deps.Metering should never be nil, but the gate must never panic).
func TestResourceMeter_NilSafe(t *testing.T) {
	var rm *ResourceMeter
	if rm.Enabled() {
		t.Fatal("nil ResourceMeter must report !Enabled()")
	}
	if err := rm.Gate(t.Context(), "acme", "", "sql", 100); err != nil {
		t.Fatalf("nil Gate = %v, want nil", err)
	}
	rm.Meter("acme", "", "sql", 100, "r", "") // must not panic

	rm2 := NewResourceMeter(Deps{Logger: luxlog.New("test")}, "provisioning") // nil metering
	if rm2.Enabled() {
		t.Fatal("ResourceMeter with nil client must report !Enabled()")
	}
	if err := rm2.Gate(t.Context(), "acme", "", "sql", 100); err != nil {
		t.Fatalf("nil-client Gate = %v, want nil", err)
	}
}

func TestResourceFeeCents(t *testing.T) {
	// Default when nothing set.
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "sql"); got != DefaultResourceFeeCents {
		t.Fatalf("default fee = %d, want %d", got, DefaultResourceFeeCents)
	}
	// Global override applies to every kind.
	t.Setenv("CLOUD_PROVISION_FEE_CENTS", "300")
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "sql"); got != 300 {
		t.Fatalf("global override fee = %d, want 300", got)
	}
	// Per-kind override wins over global.
	t.Setenv("CLOUD_PROVISION_FEE_CENTS_SQL", "500")
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "sql"); got != 500 {
		t.Fatalf("per-kind override fee = %d, want 500", got)
	}
	// Other kinds still see the global override, not the sql one.
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "vector"); got != 300 {
		t.Fatalf("vector fee = %d, want global 300", got)
	}
	// Zero is honored (free kind).
	t.Setenv("CLOUD_PROVISION_FEE_CENTS_KV", "0")
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "kv"); got != 0 {
		t.Fatalf("explicit-zero fee = %d, want 0", got)
	}
	// Invalid/negative is ignored (falls through to global), so a typo can never
	// silently make a paid resource free.
	t.Setenv("CLOUD_PROVISION_FEE_CENTS_S3", "-5")
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "s3"); got != 300 {
		t.Fatalf("negative override fee = %d, want fallthrough 300", got)
	}
	t.Setenv("CLOUD_PROVISION_FEE_CENTS_DOCDB", "abc")
	if got := ResourceFeeCents("CLOUD_PROVISION_FEE_CENTS", "docdb"); got != 300 {
		t.Fatalf("garbage override fee = %d, want fallthrough 300", got)
	}
}

// DenyResource renders the two outcomes as the SAME contract the edge gate uses.
func TestDenyResource(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Get("/insufficient", func(c *zip.Ctx) error { return DenyResource(c, metering.ErrInsufficientBalance) })
	app.Get("/unknown", func(c *zip.Ctx) error { return DenyResource(c, io.ErrUnexpectedEOF) })

	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/insufficient", http.StatusPaymentRequired, `"code":"insufficient_balance"`},
		{"/unknown", http.StatusServiceUnavailable, `"code":"balance_unavailable"`},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test(%s): %v", tc.path, err)
		}
		if resp.StatusCode != tc.code {
			t.Fatalf("%s status = %d, want %d", tc.path, resp.StatusCode, tc.code)
		}
		body, _ := io.ReadAll(resp.Body)
		if !containsSub(string(body), tc.body) {
			t.Fatalf("%s body %q missing %q", tc.path, body, tc.body)
		}
	}
}
