package cloud

// Tests for the shared per-org resource gate+meter (ResourceMeter). They drive
// the real metering client against a fake commerce server that RECORDS the
// X-Org-Id org header and request bodies, so the multitenancy contract is
// proven end-to-end over HTTP — no mock of the metering client itself. The fake
// is built with a metering DEFAULT org of "hanzo"; every assertion that the
// caller "acme" is billed (not "hanzo") proves the per-call org override is what
// scopes the ledger — i.e. a caller can never be billed to a default or another
// org.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/hanzoai/cloud/internal/planetest"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// recCommerce is the money peer for these tests: it answers the balance READ over HTTP
// and receives the usage DEBIT over the plane, recording the org each side acted for —
// the evidence for the per-org / cross-org assertions.
type recCommerce struct {
	balanceAvailable int64 // returned as {"available":N} on GET /v1/billing/balance
	balanceStatus    int   // 0 => 200

	debits planeDebits

	mu           sync.Mutex
	balanceOrg   string // last X-Org-Id on a balance call
	balanceCalls atomic.Int32
}

func (f *recCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.debits.serve(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		f.balanceCalls.Add(1)
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
	// NO /v1/billing/usage route: the debit crosses the plane now, and a debit that
	// still went over HTTP would 404 here rather than quietly counting.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *recCommerce) usages() int32   { return f.debits.count() }
func (f *recCommerce) balances() int32 { return f.balanceCalls.Load() }
func (f *recCommerce) lastBalanceOrg() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balanceOrg
}

// lastUsage is the org the debit was written to and the debit itself.
func (f *recCommerce) lastUsage() (string, plane.RecordIn) {
	d, _ := f.debits.last()
	return d.Org, d.In
}

// meterFor builds a ResourceMeter whose metering client has DEFAULT org "hanzo"
// (so a caller-org assertion proves the per-call override), at the given env.
func meterFor(t *testing.T, baseURL, env string, failOpen bool) *ResourceMeter {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: baseURL, Token: "svc-token", Org: "hanzo", FailOpen: failOpen})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return NewResourceMeter(Deps{Metering: m, Env: env}, "provisioning")
}

// Funded org (balance>0), priced kind → Gate allows, and the balance check
// targeted the CALLER org, not the client default.
func TestResourceMeter_GateAllowsFundedCallerOrg(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(funded) = %v, want nil", err)
	}
	if got := fc.lastBalanceOrg(); got != "acme" {
		t.Fatalf("balance checked org %q, want caller %q (per-call org must override the client default 'hanzo')", got, "acme")
	}
}

// payerFor resolves principal.Ledger(c) — the HOME org that PAYS — from a request's
// identity headers, exactly as a create-handler does before passing it to Gate.
func payerFor(t *testing.T, headers map[string]string) string {
	t.Helper()
	var payer string
	done := make(chan struct{})
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error {
		payer = principal.Ledger(c)
		close(done)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/provisioning/create", nil)
	for h, v := range headers {
		req.Header.Set(h, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("payerFor: %v", err)
	}
	<-done
	return payer
}

// TestResourceMeter_GateKeysOnPayerForMasqueradingAdmin (LOW-1 fast-follow): the ml
// + provisioning create-handlers pass principal.Ledger(c) (the HOME org) to the
// pre-create balance Gate, matching the paired debit. So a masquerading SuperAdmin
// (home=admin via X-User-Owner, acting in a victim org via X-Org-Id) is balance-gated
// on the ADMIN's funds — never the victim's. Before the fix these two Gates keyed on
// the effective org (the debit already keyed on home), so a masquerade was gated on
// the victim's balance while its spend landed on admin's ledger — the gate/debit
// asymmetry this closes (gate + debit both key on home; data scope stays effective).
func TestResourceMeter_GateKeysOnPayerForMasqueradingAdmin(t *testing.T) {
	// A create-handler resolves Payer(c) from the request; for a masquerade it is home.
	payer := payerFor(t, map[string]string{
		"X-User-Id":      "u_admin", // validated principal
		"X-Org-Id":       "victim",  // EFFECTIVE — the org being acted on
		"X-User-Owner":   "admin",   // HOME — the identity + billing anchor
		"X-User-IsAdmin": "true",    // platform sudo — what makes this a MASQUERADE
	})
	if payer != "admin" {
		t.Fatalf("principal.Payer for a masquerade = %q, want admin (HOME org)", payer)
	}

	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	// Gate keyed on the payer (home) → the balance check must hit admin's ledger.
	if err := rm.Gate(context.Background(), payer, "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(payer=admin, funded) = %v, want nil", err)
	}
	if got := fc.lastBalanceOrg(); got != "admin" {
		t.Fatalf("balance check keyed on X-Org-Id=%q, want admin — a masquerading admin must be gated on their OWN funds (home), not the acted-on org (victim)", got)
	}
}

// Out of funds (balance<=0) → Gate denies with ErrInsufficientBalance (→ 402).
func TestResourceMeter_GateRefusesAtZero(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 0}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != metering.ErrInsufficientBalance {
		t.Fatalf("Gate(zero balance) = %v, want ErrInsufficientBalance", err)
	}
}

// TestGate_RefusesAnEmptyLedgerAsIdentityNotAsMoney is the ordering gate for
// EVERY priced surface: the caller is resolved before the money plane is asked.
//
// An empty org reaches Gate from any handler whose tenant check and whose
// principal.Ledger disagree — provisioning's tenant() admits an admin with no
// org and Ledger answers "" for that same request — and before this guard both
// of Gate's branches answered a question about IDENTITY in the vocabulary of
// MONEY: 503 "Billing temporarily unavailable" co-resident, and over the peer
// plane 400 `field "subject" is required`, naming a field no caller can send.
//
// The assertion is the DENIAL TUPLE rather than a count of commerce calls,
// deliberately. The obvious companion — "an unidentified caller makes no call to
// the money plane" — is NOT falsifiable here: the metering client refuses an
// empty org inside Authorize before it issues any HTTP, so fc.balances() reads 0
// whether the guard ran or not. It would be a test that cannot fail. What the
// caller is TOLD does change, and that is what this pins.
//
// Mutation proof: delete the `org == ""` guard from [ResourceMeter.Gate] and the
// tuple becomes (503, balance_unavailable); delete the [ErrNoLedger] case from
// [denial] and it becomes (503, balance_unavailable) too.
func TestGate_RefusesAnEmptyLedgerAsIdentityNotAsMoney(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000} // funded: a refusal can never be poverty
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	err := rm.Gate(t.Context(), "", "", false, "sql", 100)
	if !errors.Is(err, ErrNoLedger) {
		t.Fatalf("Gate(empty org) = %v, want ErrNoLedger — an empty ledger is an identity refusal", err)
	}
	status, code, msg := denial(err)
	if status != http.StatusForbidden {
		t.Errorf("denial(ErrNoLedger) status = %d, want 403 — not a fault of the biller", status)
	}
	if code != "forbidden" || msg != "no validated principal" {
		t.Errorf("denial(ErrNoLedger) = (%q, %q), want (\"forbidden\", \"no validated principal\") — "+
			"the tenant gate's own sentence, so one condition has one answer", code, msg)
	}
}

// TestGate_FailOpenNeverMakesAnUnidentifiedCallerFree keeps the guard ABOVE the
// money policy. Fail-open decides what to do when the LEDGER is unreachable; it
// must never decide WHO the caller is. If the guard is ever moved below the
// fail-open branch, an unidentified caller silently provisions for free — the
// exact silent-degradation shape this surface must not have.
func TestGate_FailOpenNeverMakesAnUnidentifiedCallerFree(t *testing.T) {
	fc := &recCommerce{balanceStatus: http.StatusInternalServerError}
	rm := meterFor(t, fc.server(t).URL, "mainnet", true) // fail-OPEN

	// A named caller is let through by fail-open — that is the policy working.
	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(named caller, fail-open, biller down) = %v, want nil", err)
	}
	// A nameless one is still refused, because that was never a money question.
	if err := rm.Gate(t.Context(), "", "", false, "sql", 100); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("Gate(empty org, fail-open) = %v, want ErrNoLedger — fail-open is not an identity", err)
	}
}

// A free kind (cost 0) is un-gated AND never calls commerce (mirrors the edge
// gate's price==0 short-circuit).
func TestResourceMeter_GateFreeKindNoCommerceCall(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 0}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 0); err != nil {
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

	err := rm.Gate(t.Context(), "acme", "", false, "sql", 100)
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

	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(commerce 5xx, fail-open) = %v, want nil", err)
	}
}

// Meter debits the CALLER org: usage POST fires once, carries X-Org-Id:acme
// (not the default 'hanzo'), body user=="acme", amount==cost. This is the
// per-org / anti-cross-org debit proof.
func TestResourceMeter_MeterDebitsCallerOrg(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	rm.Meter("acme", "", "sql", 250, "req-1", "203.0.113.7")

	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("usage records = %d, want 1 (Meter must debit on success)", fc.usages())
	}
	org, in := fc.lastUsage()
	if org != "acme" {
		t.Fatalf("usage billed org %q, want caller %q (must override client default 'hanzo')", org, "acme")
	}
	if in.Subject != "acme" {
		t.Fatalf("usage subject = %q, want caller org %q (per-org billing keys on the org slug)", in.Subject, "acme")
	}
	if cents, err := in.Amount.Minor(); err != nil || cents != 250 {
		t.Fatalf("usage amount = %+v (%d¢, err %v), want 250¢", in.Amount, cents, err)
	}
	if in.Usage.Provider != "provisioning" {
		t.Fatalf("usage provider = %q, want %q", in.Usage.Provider, "provisioning")
	}
}

// A second org draws on ITS OWN ledger: gating "globex" must check globex,
// never "acme" or the default. Proves no cross-org fold in the gate path.
func TestResourceMeter_GateIsolatesTenants(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Gate(t.Context(), "globex", "", false, "vector", 100); err != nil {
		t.Fatalf("Gate(globex) = %v, want nil", err)
	}
	if got := fc.lastBalanceOrg(); got != "globex" {
		t.Fatalf("balance checked org %q, want %q — cross-org billing leak", got, "globex")
	}
}

// A VALIDATED named project makes resource creation HARD; a default/unvalidated
// project stays SOFT — the resource-side mirror of the edge BillingGate's project
// hardening (issue #70). The fake commerce is funded (so gating reaches the scope
// cap) and enforces the project cap ONLY when it sees pv=1, exactly modelling
// commerce's project-spoof degrade. This proves principal.ValidatedProject threads
// through ResourceMeter.Gate into AuthInput.ProjectValidated: a claim-bound project
// forwards pv=1 and 402s on the cap, while a forgeable/absent one forwards no pv and
// is allowed, so a spoofed X-Project-Id can neither hard-stop nor evade a cap.
func TestResourceMeter_GateProjectValidatedHardens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing/balance":
			_, _ = io.WriteString(w, `{"available":100000}`) // funded — proceed to the scope cap.
		case "/v1/billing/alerts/authorize":
			if r.URL.Query().Get("pv") == "1" { // validated project → cap HARD-enforces.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"allow": false, "reason": "spend_cap", "capCents": 100, "spentCents": 100,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"allow": true}) // unvalidated → soft (degraded).
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	rm := meterFor(t, srv.URL, "mainnet", false)

	// Validated NAMED project → pv=1 → the project-scoped cap HARD-enforces (402).
	if err := rm.Gate(t.Context(), "acme", "acme-prod", true, "sql", 100); err != metering.ErrSpendCapExceeded {
		t.Fatalf("Gate(validated named project) = %v, want ErrSpendCapExceeded (project cap must HARD-enforce)", err)
	}
	// Default/unvalidated project → no pv → the SAME cap degrades to soft (allow).
	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(default/unvalidated) = %v, want nil (unvalidated project cap must stay soft)", err)
	}
	// A NAMED project the caller did not prove (validated=false) also stays soft —
	// a forgeable label can neither hard-stop nor be weaponised to evade a cap.
	if err := rm.Gate(t.Context(), "acme", "acme-prod", false, "sql", 100); err != nil {
		t.Fatalf("Gate(unvalidated named project) = %v, want nil (forgeable label must not hard-enforce)", err)
	}
}

// Env never bypasses billing: on testnet AND devnet, a zero balance still
// refuses. test/dev are sandbox-but-billed, not free.
func TestResourceMeter_EnvNeverBypassesGate(t *testing.T) {
	for _, env := range []string{"testnet", "devnet"} {
		fc := &recCommerce{balanceAvailable: 0}
		rm := meterFor(t, fc.server(t).URL, env, false)
		if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != metering.ErrInsufficientBalance {
			t.Fatalf("env=%s: Gate(zero) = %v, want ErrInsufficientBalance (test/dev must still bill)", env, err)
		}
	}
}

// An unconfigured metering client (no commerce URL) makes the gate a no-op: Gate
// allows, Meter does nothing, Enabled() is false.
func TestResourceMeter_UnconfiguredIsNoop(t *testing.T) {
	m, _ := metering.New(metering.Config{}) // no BaseURL
	rm := NewResourceMeter(Deps{Metering: m}, "provisioning")
	if rm.Enabled() {
		t.Fatal("ResourceMeter with empty commerce URL must not be Enabled()")
	}
	// !Enabled no longer means "nothing bills". Once apps are their own binaries
	// the ledger has ONE writer and it lives with commerce, so a meter without a
	// local URL asks it — and a biller it cannot reach is UNKNOWN, never allowed.
	// Allowing would turn every priced act free the moment an app is split out,
	// silently. The gate fires identically in every deployment; nothing bypasses
	// it (see the env-awareness note in resource_billing.go).
	t.Setenv(runDirEnv, t.TempDir()) // no commerce socket here
	err := rm.Gate(t.Context(), "acme", "", false, "sql", 100)
	if err == nil {
		t.Fatal("Gate with no local ledger and no reachable biller must not allow")
	}
	if !strings.Contains(err.Error(), "commerce") {
		t.Fatalf("Gate = %v, want an error naming the biller it could not reach", err)
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
	if err := rm.Gate(t.Context(), "acme", "", false, "sql", 100); err != nil {
		t.Fatalf("nil Gate = %v, want nil", err)
	}
	rm.Meter("acme", "", "sql", 100, "r", "") // must not panic

	rm2 := NewResourceMeter(Deps{}, "provisioning") // nil metering
	if rm2.Enabled() {
		t.Fatal("ResourceMeter with nil client must report !Enabled()")
	}
	if err := rm2.Gate(t.Context(), "acme", "", false, "sql", 100); err != nil {
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
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Test(%s): %v", tc.path, err)
		}
		if resp.StatusCode != tc.code {
			t.Fatalf("%s status = %d, want %d", tc.path, resp.StatusCode, tc.code)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), tc.body) {
			t.Fatalf("%s body %q missing %q", tc.path, body, tc.body)
		}
	}
}

// A usage priced ONLY as a typed money.Amount must be billed.
//
// metering.Usage carries three amount sources with a documented precedence — the
// typed Amount wins, then micro-USD, then whole cents — and Usage.Money resolves
// them. MeterUsage used to ask its own version of the question, reading two of the
// three (`AmountCents <= 0 && AmountMicros <= 0`), and returned early on a Usage
// whose cost was exact. That is the shape a per-token 18-dp caller sends, so the
// money was dropped before Record could bill it: no error, no log, no row, and a
// status-code test would see nothing wrong because there is no request to fail.
//
// $0.0025 is deliberately sub-cent: it survives only because the amount is exact,
// which is the whole reason the typed field exists.
func TestResourceMeter_MeterUsageBillsAnExactAmount(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	exact, err := money.ParseUSD("0.0025") // sub-cent: survives only because it is exact.
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm.MeterUsage("acme", "zen", metering.Usage{
		User:   "acme",
		Amount: exact, // no cents, no micros set.
		Model:  "zen-1",
	})

	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("usage records = %d, want 1 — a Usage priced only as a typed "+
			"money.Amount was dropped before Record saw it", fc.usages())
	}
	org, _ := fc.lastUsage()
	if org != "acme" {
		t.Fatalf("usage billed org %q, want %q", org, "acme")
	}
}

// A debit must cross the internal plane EXACTLY.
//
// plane.Money is a decimal string precisely so an amount survives the process
// boundary unrounded, and the receiving side honors it (apps/commerce
// meter_rpc.go parses the decimal and debits it verbatim). meterPeer defeated
// both: it built the plane amount from u.AmountCents, so a usage priced only as
// a typed money.Amount — the shape every per-token caller sends — crossed as
// $0.00. The guard fix upstream (Usage.Money) made those debits SURVIVE to this
// path; this is the test that they survive it whole.
//
// The capture op stands where commerce's /finance/record stands, on the same
// plane socket, receiving the same RecordIn — what it sees is what commerce
// would have debited.
func TestMeterPeer_CarriesTheExactDebit(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	ResetPlane()

	got := make(chan plane.RecordIn, 1)
	zip.Post[plane.RecordIn, plane.Recorded](Plane(), "/finance/record",
		func(_ context.Context, in *plane.RecordIn) (*plane.Recorded, error) {
			got <- *in
			return &plane.Recorded{Amount: in.Amount}, nil
		},
		zip.WithOperationID(plane.FinanceRecord),
		zip.WithSummary("test capture of the peer debit"))

	stop, err := ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("ServePlane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	up := false
	for range 300 {
		if c, derr := net.Dial("unix", zip.SocketPath("commerce")); derr == nil {
			_ = c.Close()
			up = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !up {
		t.Fatalf("plane socket never came up at %s", zip.SocketPath("commerce"))
	}

	// No BaseURL: metering is configured but DISABLED, which is every process
	// that does not hold the ledger — exactly the state that routes to the peer.
	m, err := metering.New(metering.Config{Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	rm := NewResourceMeter(Deps{Metering: m, Env: "mainnet"}, "provisioning")
	if rm.Enabled() {
		t.Fatal("fixture broken: metering must be disabled so the debit takes the peer path")
	}

	exact, err := money.ParseUSD("0.0025") // sub-cent: survives ONLY if the wire is exact
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm.MeterUsage("acme", "zen", metering.Usage{User: "acme", Amount: exact, Model: "zen-1"})

	var in plane.RecordIn
	select {
	case in = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("no debit crossed the plane — meterPeer dropped or never sent it")
	}
	if in.Subject != "acme" {
		t.Fatalf("debit subject = %q, want acme", in.Subject)
	}
	amt, err := in.Amount.Parse()
	if err != nil {
		t.Fatalf("the plane amount does not parse: %v", err)
	}
	crossed := money.FromDecimal(amt.Decimal())
	if crossed.IsZero() {
		t.Fatalf("the debit crossed as ZERO (wire %q %q) — the sender flattened to cents before the exact wire could carry it",
			in.Amount.Decimal, in.Amount.Currency)
	}
	if crossed.Cmp(exact) != 0 {
		t.Fatalf("debit crossed as %s, want %s (wire %q %q)", crossed, exact, in.Amount.Decimal, in.Amount.Currency)
	}
}

// oneLedger is the receiving ledger's exactly-once rule, modelled: apps/finance
// RecordUsage keys a usage debit on (wallet, act ref) and posts a replay of a key it
// already holds exactly zero more times.
//
// The part that MATTERS here is what it does with an arrival carrying NO ref: it mints a
// fresh one ("no act id → the entry's own, server-minted"), so an anonymous debit can
// never match anything and ALWAYS posts again. That is why dropping the name on the way
// across does not merely lose attribution — it converts a retry into a second charge.
type oneLedger struct {
	mu     sync.Mutex
	posted map[string]bool
	debits int
	minted int
}

func (l *oneLedger) observe(_ string, in plane.RecordIn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.posted == nil {
		l.posted = map[string]bool{}
	}
	ref := in.Usage.Ref
	if ref == "" {
		l.minted++
		ref = fmt.Sprintf("server-minted-%d", l.minted)
	}
	key := in.Subject + "\x00" + ref
	if l.posted[key] {
		return // idempotent replay — this act is already paid for
	}
	l.posted[key] = true
	l.debits++
}

func (l *oneLedger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.debits
}

// A metered act keeps the name the SERVER gave it across BOTH crossings, so one act is
// one charge whichever process holds the ledger.
//
// apps/company mints a formation ref and hands that same value to the debit precisely so
// the formation and its charge are one thing under one name; MeterUsage documents the
// debit as exactly-once on it. That promise held only while the ledger was co-resident.
// In the SHIPPED split topology the debit goes out through meterPeer, which built its
// plane.Usage from Project and Service alone — the ref never boarded — so the receiver
// minted a new name per arrival and a re-driven formation charged the customer TWICE for
// one company.
//
// The two topologies are asserted TOGETHER because the defect was precisely that they
// disagreed: the fix seals the act above the branch, so neither path can name it
// differently from the other.
func TestMeterUsage_OneActIsChargedOnceInEitherTopology(t *testing.T) {
	// The formation's own name, as apps/company mints it. Re-driven under the same name
	// — a retried job, a replayed queue entry — it is still ONE act.
	const formationRef = "pay_7f3ac2e1"

	drive := func(rm *ResourceMeter) {
		for range 2 {
			rm.MeterUsage("acme", "company-formation", metering.Usage{
				User:        "acme",
				AmountCents: 12900,
				Model:       "company-formation",
				Ref:         formationRef,
			})
		}
	}

	// THE SHIPPED TOPOLOGY: the ledger is in another process, so the debit leaves through
	// meterPeer. No BaseURL means metering is configured but DISABLED, which is the state
	// of every process that does not hold the ledger.
	t.Run("split deploy", func(t *testing.T) {
		led := &oneLedger{}
		peer := &planeDebits{}
		peer.serveWith(t, led.observe)

		m, err := metering.New(metering.Config{Org: "hanzo"})
		if err != nil {
			t.Fatalf("metering.New: %v", err)
		}
		rm := NewResourceMeter(Deps{Metering: m, Env: "mainnet"}, "company")
		if rm.Enabled() {
			t.Fatal("fixture broken: metering must be DISABLED so the debit takes the peer path")
		}

		drive(rm)

		if !waitFor(func() bool { return peer.count() == 2 }, 5*time.Second) {
			t.Fatalf("crossings = %d, want 2 — both drives must reach the peer before the "+
				"count means anything", peer.count())
		}
		if got, _ := peer.last(); got.In.Usage.Ref != formationRef {
			t.Errorf("the act crossed as ref %q, want %q — meterPeer must carry the name the "+
				"caller sealed", got.In.Usage.Ref, formationRef)
		}
		if n := led.count(); n != 1 {
			t.Errorf("one formation, re-driven, was debited %d times; want exactly 1 — the "+
				"customer is charged once per company", n)
		}
	})

	// THE CO-RESIDENT TOPOLOGY, unchanged: sealing above the branch must not move it.
	t.Run("co-resident", func(t *testing.T) {
		led := &oneLedger{}
		peer := &planeDebits{}
		peer.serveWith(t, led.observe)

		// A BaseURL is what ENABLES the client; MeterUsage never reads it (only the
		// balance gate does), so it is never dialled.
		m, err := metering.New(metering.Config{BaseURL: "http://127.0.0.1:1", Token: "svc-token", Org: "hanzo"})
		if err != nil {
			t.Fatalf("metering.New: %v", err)
		}
		rm := NewResourceMeter(Deps{Metering: m, Env: "mainnet"}, "company")
		if !rm.Enabled() {
			t.Fatal("fixture broken: metering must be ENABLED so the debit takes the local path")
		}

		drive(rm)

		if !waitFor(func() bool { return peer.count() == 2 }, 5*time.Second) {
			t.Fatalf("crossings = %d, want 2", peer.count())
		}
		if got, _ := peer.last(); got.In.Usage.Ref != formationRef {
			t.Errorf("the act crossed as ref %q, want %q", got.In.Usage.Ref, formationRef)
		}
		if n := led.count(); n != 1 {
			t.Errorf("one formation, re-driven, was debited %d times; want exactly 1", n)
		}
	})
}

// An UNNAMED act still stands alone: two calls are two acts, and sealing above the
// topology branch must not fold them into one.
//
// This is the other half of the seal's contract and the regression the fix could
// plausibly have introduced — a shared or reused ref would make two distinct inferences
// bill once, which is a revenue leak pointing the other way.
func TestMeterUsage_TwoUnnamedActsAreTwoCharges(t *testing.T) {
	led := &oneLedger{}
	peer := &planeDebits{}
	peer.serveWith(t, led.observe)

	m, err := metering.New(metering.Config{Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	rm := NewResourceMeter(Deps{Metering: m, Env: "mainnet"}, "company")
	if rm.Enabled() {
		t.Fatal("fixture broken: metering must be DISABLED so the debit takes the peer path")
	}

	// No Ref: two ordinary metered calls, which are two acts however identical they look.
	for range 2 {
		rm.MeterUsage("acme", "zen", metering.Usage{User: "acme", AmountCents: 100, Model: "zen-1"})
	}

	if !waitFor(func() bool { return peer.count() == 2 }, 5*time.Second) {
		t.Fatalf("crossings = %d, want 2", peer.count())
	}
	if n := led.count(); n != 2 {
		t.Errorf("two distinct acts were debited %d times; want 2 — sealing names an act, it "+
			"does not merge them", n)
	}
	if got, _ := peer.last(); got.In.Usage.Ref == "" {
		t.Error("an unnamed act crossed with no ref at all — the seal must mint one, so the " +
			"receiver dedups on a name the SERVER chose rather than on nothing")
	}
}

// A debit must not be able to quote ANOTHER caller's bytes.
//
// A Usage assembled in a handler carries zero-copy views into the server's
// reused request arena — c.User(), c.RequestID() and the forwarded client IP are
// header reads that alias fasthttp's buffer — and MeterUsage records on a
// background goroutine. The buffer is handed to the NEXT request on the same
// connection the instant the handler returns, and connections are reused across
// tenants, so the retained string does not merely go stale: it becomes somebody
// else's request id, on this caller's row. No crash, no error, a wrong record.
//
// This reproduces it deterministically rather than waiting for -race to catch
// it: the arena is a byte slice, the header read is the same unsafe view fiber
// hands out, and the overwrite is the next request landing.
//
// Mutation proof: drop the u.Clone() in MeterUsage and the recorded requestId
// below is the overwriting caller's.
func TestResourceMeter_MeterOwnsTheStringsItRetains(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	arena := []byte("req-mine")
	borrowed := unsafe.String(&arena[0], len(arena)) // exactly what c.RequestID() returns

	rm.MeterUsage("acme", "sql", metering.Usage{AmountCents: 250, RequestID: borrowed})
	copy(arena, "req-thrs") // the next request on this connection, same length

	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("usage records = %d, want 1", fc.usages())
	}
	_, in := fc.lastUsage()
	if in.Usage.RequestID != "req-mine" {
		t.Fatalf("the debit recorded requestId %q, want %q — the meter retained a view into the "+
			"caller's request arena, so the row quotes whoever used that connection next",
			in.Usage.RequestID, "req-mine")
	}
}
