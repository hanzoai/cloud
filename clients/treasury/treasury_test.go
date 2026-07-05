package treasury

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/treasury/ledger"
	"github.com/hanzoai/cloud/clients/treasury/ledger/sqlstore"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mount builds a treasury app on a fresh store and installs it as the process
// singleton (so the Reserve helper resolves it), cleaning both up.
func mount(t *testing.T) (*zip.App, *svc) {
	t.Helper()
	store, err := sqlstore.Open(t.TempDir() + "/treasury.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := luxlog.New("test")
	s := &svc{
		store:  store,
		record: ledger.New(store),
		log:    log,
		anchor: newAnchorer(cloud.Deps{}, log),
	}
	mounted = s
	t.Cleanup(func() { mounted = nil })
	app := zip.New(zip.Config{Logger: log})
	app.Get("/v1/finance/treasury", s.myTreasury)
	app.Get("/v1/finance/accounts", s.myAccounts)
	app.Get("/v1/admin/treasury", s.adminReport)
	app.Post("/v1/admin/treasury/policy", s.adminSetPolicy)
	app.Post("/v1/admin/treasury/sweep", s.adminSweep)
	app.Post("/v1/admin/treasury/seed", s.adminSeed)
	app.Post("/v1/admin/treasury/anchor", s.adminAnchor)
	return app, s
}

// req drives one HTTP request. org sets a VALIDATED principal; admin adds the
// global-admin header.
func req(t *testing.T, app *zip.App, method, path, org string, admin bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	if admin {
		hr.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// unwrap returns the `data` object of the { status, msg, data } admin envelope.
func unwrap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env struct {
		Status string         `json:"status"`
		Data   map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if env.Status != "ok" {
		t.Fatalf("envelope status = %q", env.Status)
	}
	return env.Data
}

// ── auth gates ───────────────────────────────────────────────────────────────

func TestTreasury_RequiresAuth(t *testing.T) {
	app, _ := mount(t)
	if code, _ := req(t, app, http.MethodGet, "/v1/finance/treasury", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("GET /v1/finance/treasury unauth = %d, want 403", code)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/finance/treasury", "acme", false, nil); code != http.StatusOK {
		t.Fatalf("GET /v1/finance/treasury as org = %d, want 200", code)
	}
}

func TestAdmin_RequiresAdmin(t *testing.T) {
	app, _ := mount(t)
	for _, p := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/admin/treasury"},
		{http.MethodPost, "/v1/admin/treasury/policy"},
		{http.MethodPost, "/v1/admin/treasury/sweep"},
		{http.MethodPost, "/v1/admin/treasury/seed"},
		{http.MethodPost, "/v1/admin/treasury/anchor"},
	} {
		// A non-admin org caller must be refused.
		if code, _ := req(t, app, p.method, p.path, "acme", false, map[string]any{}); code != http.StatusForbidden {
			t.Fatalf("%s %s as non-admin = %d, want 403", p.method, p.path, code)
		}
	}
}

// ── policy + sweep + seed + report ───────────────────────────────────────────

func TestPolicy_SetAndRead(t *testing.T) {
	app, _ := mount(t)
	code, body := req(t, app, http.MethodPost, "/v1/admin/treasury/policy", "root", true, policyRequest{RevenueShareBps: 500})
	if code != http.StatusOK {
		t.Fatalf("set policy = %d (%s)", code, body)
	}
	// customer read reflects the policy
	_, cb := req(t, app, http.MethodGet, "/v1/finance/treasury", "acme", true, nil)
	var rep ledger.Report
	if err := json.Unmarshal(cb, &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if rep.Policy.RevenueShareBps != 500 {
		t.Fatalf("policy bps = %d, want 500", rep.Policy.RevenueShareBps)
	}
}

func TestSeed_FundsReserve(t *testing.T) {
	app, s := mount(t)
	code, body := req(t, app, http.MethodPost, "/v1/admin/treasury/seed", "root", true, seedRequest{AmountCents: 10_000, Memo: "bootstrap"})
	if code != http.StatusOK {
		t.Fatalf("seed = %d (%s)", code, body)
	}
	if bal, _ := s.record.ReserveCents(context.Background()); bal != 10_000 {
		t.Fatalf("reserve = %d, want 10000", bal)
	}
}

func TestSweep_AccruesRevenueShare_Idempotent(t *testing.T) {
	app, s := mount(t)
	// 10% policy.
	req(t, app, http.MethodPost, "/v1/admin/treasury/policy", "root", true, policyRequest{RevenueShareBps: 1000})
	// Sweep $2000 revenue for 2026-07 → $200 accrual.
	_, body := req(t, app, http.MethodPost, "/v1/admin/treasury/sweep", "root", true, sweepRequest{Period: "2026-07", RevenueCents: 200_000})
	d := unwrap(t, body)
	if d["accruedCents"].(float64) != 20_000 {
		t.Fatalf("accrued = %v, want 20000", d["accruedCents"])
	}
	// Re-sweep the SAME period → no double accrual.
	_, body2 := req(t, app, http.MethodPost, "/v1/admin/treasury/sweep", "root", true, sweepRequest{Period: "2026-07", RevenueCents: 200_000})
	d2 := unwrap(t, body2)
	if d2["created"].(bool) {
		t.Fatal("re-sweep same period must be idempotent (created=false)")
	}
	if bal, _ := s.record.ReserveCents(context.Background()); bal != 20_000 {
		t.Fatalf("reserve double-accrued: %d, want 20000", bal)
	}
}

func TestAdminReport_Shape(t *testing.T) {
	app, _ := mount(t)
	req(t, app, http.MethodPost, "/v1/admin/treasury/seed", "root", true, seedRequest{AmountCents: 5_000})
	_, body := req(t, app, http.MethodGet, "/v1/admin/treasury", "root", true, nil)
	d := unwrap(t, body)
	if _, ok := d["report"]; !ok {
		t.Fatal("report missing")
	}
	if _, ok := d["journal"]; !ok {
		t.Fatal("journal missing")
	}
	anchor, ok := d["anchor"].(map[string]any)
	if !ok {
		t.Fatal("anchor status missing")
	}
	if anchor["status"] != "pending" {
		t.Fatalf("anchor status = %v, want pending (unconfigured)", anchor["status"])
	}
	if anchor["chainId"].(float64) != float64(defaultHanzoChainID) {
		t.Fatalf("anchor chainId = %v, want %d", anchor["chainId"], defaultHanzoChainID)
	}
	// The root is a real 32-byte commitment (0x + 64 hex).
	if root, _ := anchor["currentRoot"].(string); len(root) != 66 {
		t.Fatalf("currentRoot = %q, want 0x+64hex", root)
	}
}

// ── scope-aware accounts (tenant isolation) ──────────────────────────────────

func TestAccounts_ScopeIsolation(t *testing.T) {
	app, _ := mount(t)
	req(t, app, http.MethodPost, "/v1/admin/treasury/seed", "root", true, seedRequest{AmountCents: 5_000})

	// A per-org caller sees ONLY its own tenant prefix (empty here — honest) and
	// NEVER the house fund:reserve account.
	_, body := req(t, app, http.MethodGet, "/v1/finance/accounts", "acme", false, nil)
	var org struct {
		Scope    string        `json:"scope"`
		Tenant   string        `json:"tenant"`
		Accounts []accountView `json:"accounts"`
	}
	if err := json.Unmarshal(body, &org); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if org.Scope != "org" || org.Tenant != "acme" {
		t.Fatalf("scope/tenant = %q/%q, want org/acme", org.Scope, org.Tenant)
	}
	for _, a := range org.Accounts {
		if a.Address == "fund:reserve" {
			t.Fatal("per-org caller leaked the house reserve account")
		}
	}
	// Unauth → 403.
	if code, _ := req(t, app, http.MethodGet, "/v1/finance/accounts", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("unauth accounts = %d, want 403", code)
	}
	// Global-admin ?scope=house sees the seeded reserve.
	_, hb := req(t, app, http.MethodGet, "/v1/finance/accounts?scope=house", "root", true, nil)
	var house struct {
		Scope    string        `json:"scope"`
		Accounts []accountView `json:"accounts"`
	}
	_ = json.Unmarshal(hb, &house)
	if house.Scope != "house" {
		t.Fatalf("admin house scope = %q", house.Scope)
	}
	var reserve int64
	for _, a := range house.Accounts {
		if a.Address == "fund:reserve" {
			reserve = a.BalanceCents
		}
	}
	if reserve != 5_000 {
		t.Fatalf("house fund:reserve = %d, want 5000", reserve)
	}
}

// ── the Reserve seam (backed payouts) ────────────────────────────────────────

func TestReserve_Passthrough_WhenUnmounted(t *testing.T) {
	mounted = nil // simulate the subsystem not being linked/enabled
	backed, id, err := Reserve(context.Background(), ProgramReferral, "ref:1", "bonus", 1_000)
	if err != nil || !backed || id != "" {
		t.Fatalf("passthrough: backed=%v id=%q err=%v (want true,\"\",nil)", backed, id, err)
	}
}

func TestReserve_EnforcedAgainstFund(t *testing.T) {
	_, s := mount(t) // installs mounted
	ctx := context.Background()
	// Fund $50 via seed.
	if _, _, err := s.record.Seed(ctx, "seed:test", "cap", 5_000, 1); err != nil {
		t.Fatal(err)
	}
	// Backed: $30 payout debits the fund.
	backed, id, err := Reserve(ctx, ProgramAuthor, "pay:1", "royalty", 3_000)
	if err != nil || !backed || id == "" {
		t.Fatalf("backed reserve: backed=%v id=%q err=%v", backed, id, err)
	}
	if bal, _ := s.record.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("reserve after backed payout = %d, want 2000", bal)
	}
	// Blocked: $50 > remaining $20 → not backed, fund intact.
	backed2, _, err := Reserve(ctx, ProgramAuthor, "pay:2", "royalty", 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if backed2 {
		t.Fatal("insufficient reserve must NOT back the payout")
	}
	if bal, _ := s.record.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("blocked payout altered fund: %d, want 2000", bal)
	}
	// Idempotent replay of the backed ref: no second debit.
	backed3, _, err := Reserve(ctx, ProgramAuthor, "pay:1", "royalty", 3_000)
	if err != nil || !backed3 {
		t.Fatalf("replay: backed=%v err=%v", backed3, err)
	}
	if bal, _ := s.record.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("replay double-charged fund: %d, want 2000", bal)
	}
}

// ── finance TreasurySummary shape ────────────────────────────────────────────

// TestTreasury_FinanceSummaryShape proves GET /v1/finance/treasury emits the exact
// @hanzo/finance-ui TreasurySummary shape (USD cents): reserve == available (no
// pending-payout holds), committed 0, and the honest Hanzo L1 anchor (chainId 36963,
// not-yet-anchored → no fabricated block).
func TestTreasury_FinanceSummaryShape(t *testing.T) {
	app, _ := mount(t)
	// Seed $250.00 into the reserve so reserve/available are non-zero.
	if code, body := req(t, app, http.MethodPost, "/v1/admin/treasury/seed", "root", true, seedRequest{AmountCents: 25_000, Memo: "bootstrap"}); code != http.StatusOK {
		t.Fatalf("seed = %d (%s)", code, body)
	}
	_, body := req(t, app, http.MethodGet, "/v1/finance/treasury", "acme", false, nil)
	var got struct {
		Currency       string `json:"currency"`
		ReserveCents   int64  `json:"reserveCents"`
		CommittedCents int64  `json:"committedCents"`
		AvailableCents int64  `json:"availableCents"`
		Anchor         *struct {
			ChainID    int64  `json:"chainId"`
			Address    string `json:"address"`
			Block      uint64 `json:"block"`
			AnchoredAt string `json:"anchoredAt"`
		} `json:"anchor"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode summary: %v (%s)", err, body)
	}
	if got.Currency != "usd" {
		t.Fatalf("currency = %q, want usd", got.Currency)
	}
	if got.ReserveCents != 25_000 {
		t.Fatalf("reserveCents = %d, want 25000", got.ReserveCents)
	}
	if got.CommittedCents != 0 {
		t.Fatalf("committedCents = %d, want 0 (no pending-payout holds)", got.CommittedCents)
	}
	if got.AvailableCents != 25_000 {
		t.Fatalf("availableCents = %d, want 25000 (== reserve)", got.AvailableCents)
	}
	if got.Anchor == nil || got.Anchor.ChainID != defaultHanzoChainID {
		t.Fatalf("anchor chainId = %+v, want %d", got.Anchor, defaultHanzoChainID)
	}
	// Honest: nothing anchored yet → no fabricated block/time.
	if got.Anchor.Block != 0 || got.Anchor.AnchoredAt != "" {
		t.Fatalf("anchor must be honest-empty until committed: block=%d anchoredAt=%q", got.Anchor.Block, got.Anchor.AnchoredAt)
	}
}
