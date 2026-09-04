package cloud

// Tests for the shared per-org resource gate+meter (Meter). They drive
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

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/internal/planetest"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/cloud/money"
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
func (f *recCommerce) lastUsage() (string, client.RecordIn) {
	d, _ := f.debits.last()
	return d.Org, d.In
}

// meterFor builds a Meter whose metering client has DEFAULT org "hanzo"
// (so a caller-org assertion proves the per-call override), at the given env.
func meterFor(t *testing.T, baseURL, env string, failOpen bool) *Meter {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: baseURL, Org: "hanzo", FailOpen: failOpen})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	t.Setenv("CLOUD_ENV", env)
	return NewMeter(Deps{Metering: m}, "provisioning")
}

// Funded org (balance>0), priced kind → Authorize allows, and the balance check
// targeted the CALLER org, not the client default.
func TestMeter_AuthorizeAllowsFundedCallerOrg(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
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

// TestMeter_AuthorizeKeysOnPayerForMasqueradingAdmin (LOW-1 fast-follow): the ml
// + provisioning create-handlers pass principal.Ledger(c) (the HOME org) to the
// pre-create balance Gate, matching the paired debit. So a masquerading SuperAdmin
// (home=admin via X-User-Owner, acting in a victim org via X-Org-Id) is balance-gated
// on the ADMIN's funds — never the victim's. Before the fix these two Gates keyed on
// the effective org (the debit already keyed on home), so a masquerade was gated on
// the victim's balance while its spend landed on admin's ledger — the gate/debit
// asymmetry this closes (gate + debit both key on home; data scope stays effective).
func TestMeter_AuthorizeKeysOnPayerForMasqueradingAdmin(t *testing.T) {
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
	if err := rm.Authorize(context.Background(), account.PayerOf("", payer), "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(payer=admin, funded) = %v, want nil", err)
	}
	if got := fc.lastBalanceOrg(); got != "admin" {
		t.Fatalf("balance check keyed on X-Org-Id=%q, want admin — a masquerading admin must be gated on their OWN funds (home), not the acted-on org (victim)", got)
	}
}

// Out of funds (balance<=0) → Gate denies with ErrInsufficientBalance (→ 402).
func TestMeter_AuthorizeRefusesAtZero(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 0}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != metering.ErrInsufficientBalance {
		t.Fatalf("Gate(zero balance) = %v, want ErrInsufficientBalance", err)
	}
}

// TestGate_RefusesAnEmptyLedgerAsIdentityNotAsMoney is the ordering gate for
// EVERY priced surface: the caller is resolved before the money plane is asked.
//
// An empty org reaches Gate from any handler whose tenant check and whose
// principal.Ledger disagree — provisioning's tenantOf admits an admin with no
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
// Mutation proof: delete the `org == ""` guard from [Meter.Authorize] and the
// tuple becomes (503, balance_unavailable); delete the [ErrNoLedger] case from
// [denial] and it becomes (503, balance_unavailable) too.
func TestGate_RefusesAnEmptyLedgerAsIdentityNotAsMoney(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000} // funded: a refusal can never be poverty
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	err := rm.Authorize(t.Context(), account.PayerOf("", ""), "", false, "sql", 100)
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
	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(named caller, fail-open, biller down) = %v, want nil", err)
	}
	// A nameless one is still refused, because that was never a money question.
	if err := rm.Authorize(t.Context(), account.PayerOf("", ""), "", false, "sql", 100); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("Gate(empty org, fail-open) = %v, want ErrNoLedger — fail-open is not an identity", err)
	}
}

// A free kind (cost 0) is un-gated AND never calls commerce (mirrors the edge
// gate's price==0 short-circuit).
func TestMeter_AuthorizeFreeKindNoCommerceCall(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 0}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 0); err != nil {
		t.Fatalf("Gate(free kind) = %v, want nil", err)
	}
	if n := fc.balances(); n != 0 {
		t.Fatalf("commerce balance called %d times for a free kind, want 0", n)
	}
}

// Commerce unreachable/5xx + fail-CLOSED (default) → Gate denies with a non-
// ErrInsufficientBalance error (→ 503). The whole margin-protection point: a
// billing outage must NOT yield free provisioning.
func TestMeter_AuthorizeFailClosedOnCommerceError(t *testing.T) {
	fc := &recCommerce{balanceStatus: http.StatusInternalServerError}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100)
	if err == nil {
		t.Fatal("Gate(commerce 5xx, fail-closed) = nil, want a deny error (no free provisioning on outage)")
	}
	if err == metering.ErrInsufficientBalance {
		t.Fatal("Gate(commerce 5xx) returned ErrInsufficientBalance, want a balance-unknown error (→ 503, not 402)")
	}
}

// Commerce unreachable/5xx + fail-OPEN → Authorize allows (availability over billing).
func TestMeter_AuthorizeFailOpenOnCommerceError(t *testing.T) {
	fc := &recCommerce{balanceStatus: http.StatusInternalServerError}
	rm := meterFor(t, fc.server(t).URL, "mainnet", true /* fail-open */)

	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(commerce 5xx, fail-open) = %v, want nil", err)
	}
}

// Meter debits the CALLER org: usage POST fires once, carries X-Org-Id:acme
// (not the default 'hanzo'), body user=="acme", amount==cost. This is the
// per-org / anti-cross-org debit proof.
func TestMeter_RecordDebitsCallerOrg(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	rm.Record(account.PayerOf("", "acme"), "sql", metering.Usage{
		Model:       "sql",
		AmountCents: 250,
		Project:     "",
		RequestID:   "req-1",
		ClientIP:    "203.0.113.7",
	})

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
func TestMeter_AuthorizeIsolatesTenants(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	if err := rm.Authorize(t.Context(), account.PayerOf("", "globex"), "", false, "vector", 100); err != nil {
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
// through Meter.Authorize into AuthInput.ProjectValidated: a claim-bound project
// forwards pv=1 and 402s on the cap, while a forgeable/absent one forwards no pv and
// is allowed, so a spoofed X-Project-Id can neither hard-stop nor evade a cap.
func TestMeter_AuthorizeProjectValidatedHardens(t *testing.T) {
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
	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "acme-prod", true, "sql", 100); err != metering.ErrSpendCapExceeded {
		t.Fatalf("Gate(validated named project) = %v, want ErrSpendCapExceeded (project cap must HARD-enforce)", err)
	}
	// Default/unvalidated project → no pv → the SAME cap degrades to soft (allow).
	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
		t.Fatalf("Gate(default/unvalidated) = %v, want nil (unvalidated project cap must stay soft)", err)
	}
	// A NAMED project the caller did not prove (validated=false) also stays soft —
	// a forgeable label can neither hard-stop nor be weaponised to evade a cap.
	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "acme-prod", false, "sql", 100); err != nil {
		t.Fatalf("Gate(unvalidated named project) = %v, want nil (forgeable label must not hard-enforce)", err)
	}
}

// Env never bypasses billing: on testnet AND devnet, a zero balance still
// refuses. test/dev are sandbox-but-billed, not free.
func TestMeter_EnvNeverBypassesTheGate(t *testing.T) {
	for _, env := range []string{"testnet", "devnet"} {
		fc := &recCommerce{balanceAvailable: 0}
		rm := meterFor(t, fc.server(t).URL, env, false)
		if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != metering.ErrInsufficientBalance {
			t.Fatalf("env=%s: Gate(zero) = %v, want ErrInsufficientBalance (test/dev must still bill)", env, err)
		}
	}
}

// A meter whose client holds no local ledger asks the peer, and the two answers
// it can get are DIFFERENT facts:
//
//	no commerce anywhere      -> nobody bills here: allow, record nothing
//	commerce that cannot answer -> UNKNOWN: refuse, fail-closed
//
// Reading the second as the first is what turns every priced act free the moment
// an app is split out, silently. Both are asserted together because the defect
// was precisely that they were one branch.
func TestMeter_UnconfiguredIsNoop(t *testing.T) {
	m, _ := metering.New(metering.Config{}) // no BaseURL
	rm := NewMeter(Deps{Metering: m}, "provisioning")
	if m.Enabled() {
		t.Fatal("fixture broken: an empty commerce URL must leave the client without a local ledger")
	}

	t.Run("no commerce in this deployment", func(t *testing.T) {
		t.Setenv(runDirEnv, planetest.Dir(t)) // no commerce socket, no router
		if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
			t.Fatalf("Authorize = %v, want nil — there is no biller to refuse on behalf of", err)
		}
		rm.Record(account.PayerOf("", "acme"), "sql", metering.Usage{
			Model:       "sql",
			AmountCents: 100,
			RequestID:   "r",
		}) // must not panic
	})

	t.Run("commerce deployed and unable to answer", func(t *testing.T) {
		// A commerce peer that serves the debit and NOT the gate: the socket answers,
		// the op does not. That is an outage, whatever its shape, and never permission.
		planetest.Serve(t)
		err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100)
		if err == nil {
			t.Fatal("Authorize allowed while the biller could not answer — that is free work")
		}
		if !strings.Contains(err.Error(), "commerce") {
			t.Fatalf("Authorize = %v, want an error naming the biller that did not answer", err)
		}
	})
}

// A nil Meter and a meter with a nil client are safe no-ops (defensive:
// deps.Metering should never be nil, but the gate must never panic).
func TestMeter_NilSafe(t *testing.T) {
	var rm *Meter
	if err := rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
		t.Fatalf("nil Gate = %v, want nil", err)
	}
	rm.Record(account.PayerOf("", "acme"), "sql", metering.Usage{
		Model:       "sql",
		AmountCents: 100,
		Project:     "",
		RequestID:   "r",
		ClientIP:    "",
	}) // must not panic

	rm2 := NewMeter(Deps{}, "provisioning") // nil metering
	if err := rm2.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100); err != nil {
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
// them. Record used to ask its own version of the question, reading two of the
// three (`AmountCents <= 0 && AmountMicros <= 0`), and returned early on a Usage
// whose cost was exact. That is the shape a per-token 18-dp caller sends, so the
// money was dropped before Record could bill it: no error, no log, no row, and a
// status-code test would see nothing wrong because there is no request to fail.
//
// $0.0025 is deliberately sub-cent: it survives only because the amount is exact,
// which is the whole reason the typed field exists.
func TestMeter_RecordBillsAnExactAmount(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	exact, err := money.ParseUSD("0.0025") // sub-cent: survives only because it is exact.
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm.Record(account.PayerOf("", "acme"), "zen", metering.Usage{
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
// client.Money is a decimal string precisely so an amount survives the process
// boundary unrounded, and the receiving side honors it (apps/commerce
// meter_rpc.go parses the decimal and debits it verbatim). A sender that folded
// to cents first would land a usage priced only as a typed money.Amount — the
// shape every per-token caller sends — as $0.00. This is the test that the exact
// figure survives the crossing whole.
//
// The capture op stands where commerce's /finance/record stands, on the same
// plane socket, receiving the same RecordIn — what it sees is what commerce
// would have debited.
func TestMeterPeer_CarriesTheExactDebit(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	ResetPlane()

	got := make(chan client.RecordIn, 1)
	zip.Post[client.RecordIn, client.Recorded](Plane(), "/finance/record",
		func(_ context.Context, in *client.RecordIn) (*client.Recorded, error) {
			got <- *in
			return &client.Recorded{Amount: in.Amount}, nil
		},
		zip.WithOperationID(client.FinanceRecord),
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
	rm := NewMeter(Deps{Metering: m}, "provisioning")
	if m.Enabled() {
		t.Fatal("fixture broken: the client must hold no local ledger so the debit takes the peer path")
	}

	exact, err := money.ParseUSD("0.0025") // sub-cent: survives ONLY if the wire is exact
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm.Record(account.PayerOf("", "acme"), "zen", metering.Usage{User: "acme", Amount: exact, Model: "zen-1"})

	var in client.RecordIn
	select {
	case in = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("no debit crossed the plane — the debit was dropped or never sent")
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

func (l *oneLedger) observe(_ string, in client.RecordIn) {
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
// the formation and its charge are one thing under one name; Record documents the
// debit as exactly-once on it. A crossing that drops the ref makes the receiver mint a
// new name per arrival, so a re-driven formation charges the customer twice for one
// company.
//
// The two topologies are asserted TOGETHER: the act is sealed above the transport
// choice, so neither path can name it differently from the other.
func TestRecord_OneActIsChargedOnceInEitherTopology(t *testing.T) {
	// The formation's own name, as apps/company mints it. Re-driven under the same name
	// — a retried job, a replayed queue entry — it is still ONE act.
	const formationRef = "pay_7f3ac2e1"

	drive := func(rm *Meter) {
		for range 2 {
			rm.Record(account.PayerOf("", "acme"), "company-formation", metering.Usage{
				User:        "acme",
				AmountCents: 12900,
				Model:       "company-formation",
				Ref:         formationRef,
			})
		}
	}

	// THE SHIPPED TOPOLOGY: the ledger is in another process, so the debit crosses the
	// plane. No BaseURL is the state of every process that does not hold the ledger.
	t.Run("split deploy", func(t *testing.T) {
		led := &oneLedger{}
		peer := &planeDebits{}
		peer.serveWith(t, led.observe)

		m, err := metering.New(metering.Config{Org: "hanzo"})
		if err != nil {
			t.Fatalf("metering.New: %v", err)
		}
		rm := NewMeter(Deps{Metering: m}, "company")
		if m.Enabled() {
			t.Fatal("fixture broken: the client must hold no local ledger so the debit takes the peer path")
		}

		drive(rm)

		if !waitFor(func() bool { return peer.count() == 2 }, 5*time.Second) {
			t.Fatalf("crossings = %d, want 2 — both drives must reach the peer before the "+
				"count means anything", peer.count())
		}
		if got, _ := peer.last(); got.In.Usage.Ref != formationRef {
			t.Errorf("the act crossed as ref %q, want %q — the crossing must carry the name the "+
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

		// A BaseURL is what ENABLES the client; Record never reads it (only the
		// balance gate does), so it is never dialled.
		m, err := metering.New(metering.Config{BaseURL: "http://127.0.0.1:1", Org: "hanzo"})
		if err != nil {
			t.Fatalf("metering.New: %v", err)
		}
		rm := NewMeter(Deps{Metering: m}, "company")
		if !m.Enabled() {
			t.Fatal("fixture broken: the client must hold the local ledger so the debit takes the local path")
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
func TestRecord_TwoUnnamedActsAreTwoCharges(t *testing.T) {
	led := &oneLedger{}
	peer := &planeDebits{}
	peer.serveWith(t, led.observe)

	m, err := metering.New(metering.Config{Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	rm := NewMeter(Deps{Metering: m}, "company")
	if m.Enabled() {
		t.Fatal("fixture broken: the client must hold no local ledger so the debit takes the peer path")
	}

	// No Ref: two ordinary metered calls, which are two acts however identical they look.
	for range 2 {
		rm.Record(account.PayerOf("", "acme"), "zen", metering.Usage{User: "acme", AmountCents: 100, Model: "zen-1"})
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
// header reads that alias fasthttp's buffer — and Record records on a
// background goroutine. The buffer is handed to the NEXT request on the same
// connection the instant the handler returns, and connections are reused across
// tenants, so the retained string does not merely go stale: it becomes somebody
// else's request id, on this caller's row. No crash, no error, a wrong record.
//
// This reproduces it deterministically rather than waiting for -race to catch
// it: the arena is a byte slice, the header read is the same unsafe view fiber
// hands out, and the overwrite is the next request landing.
//
// Mutation proof: drop the u.Clone() in Record and the recorded requestId
// below is the overwriting caller's.
func TestMeter_RecordOwnsTheStringsItRetains(t *testing.T) {
	fc := &recCommerce{balanceAvailable: 5000}
	rm := meterFor(t, fc.server(t).URL, "mainnet", false)

	arena := []byte("req-mine")
	borrowed := unsafe.String(&arena[0], len(arena)) // exactly what c.RequestID() returns

	rm.Record(account.PayerOf("", "acme"), "sql", metering.Usage{AmountCents: 250, RequestID: borrowed})
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

// A SANDBOX LEASE IS RECORDED ONLY ONCE IT HAS A PRICE, and the test says so
// because the deployment cannot.
//
// apps/sandbox meters every lease that reaches "running", passing the fee its
// class resolves to. That fee defaults to FREE — a sandbox costs nothing until
// somebody prices it — and meterUsage drops a zero amount on the floor, for the
// good reason that a zero debit is not a debit. Compose the two and an unpriced
// fleet leases sandboxes all day while /v1/billing/usage stays empty, which
// reads exactly like metering is broken. It is not: there is nothing to bill.
//
// The lever is a price, and the price is config (SANDBOX_FEE_CENTS[_CLASS]), so
// what is pinned here is the mechanism either side of it: unpriced records
// nothing, priced records the amount asked for. Whoever wonders why usage is
// empty should find this test before they go looking for the bug.
func TestRecord_ASandboxLeaseIsRecordedOnlyOnceItIsPriced(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cents int64
		want  int32
	}{
		{"unpriced — free, so nothing to record", 0, 0},
		{"priced — the lease is billed", 250, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := &recCommerce{balanceAvailable: 100000}
			rm := meterFor(t, fc.server(t).URL, "mainnet", false)

			rm.Record(account.PayerOf("", "acme"), "sandbox", metering.Usage{
				User:        "acme",
				Model:       "exec/kata-fc",
				AmountCents: tc.cents,
			})

			got := waitFor(func() bool { return fc.usages() == tc.want }, time.Second)
			if !got && tc.want > 0 {
				t.Fatalf("usage records = %d, want %d — a priced lease must reach the ledger",
					fc.usages(), tc.want)
			}
			if fc.usages() != tc.want {
				t.Fatalf("usage records = %d, want %d", fc.usages(), tc.want)
			}
		})
	}
}
