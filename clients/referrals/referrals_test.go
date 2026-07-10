package referrals

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce is an in-memory commerce ledger: it records deposits per org (the
// wallet balance) and lets a test SET a referee's metered spend (the qualify
// signal). It is the money-seam stand-in that lets the tests PROVE both balances
// move on a qualifying referral — without a live commerce deployment.
type fakeCommerce struct {
	mu       sync.Mutex
	balance  map[string]int64 // org → deposited cents (the wallet)
	spend    map[string]int64 // org → metered spend cents (qualify signal)
	deposits int              // total deposit calls (idempotency proof)
	failDep  bool             // when true, deposit errors (to exercise the at-most-once log path)
	seq      int
}

func newFakeCommerce() *fakeCommerce {
	return &fakeCommerce{balance: map[string]int64{}, spend: map[string]int64{}}
}

func (f *fakeCommerce) configured() bool { return true }

func (f *fakeCommerce) deposit(_ context.Context, org, _ string, amountCents int64, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDep {
		return "", errUnconfigured
	}
	f.balance[org] += amountCents
	f.deposits++
	f.seq++
	return "txn_test_" + org + "_" + strconv.Itoa(f.seq), nil
}

func (f *fakeCommerce) spendCents(_ context.Context, org, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spend[org], nil
}

func (f *fakeCommerce) setSpend(org string, cents int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spend[org] = cents
}

func (f *fakeCommerce) bal(org string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balance[org]
}

func (f *fakeCommerce) depositCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deposits
}

// mount builds a referrals app backed by a fresh store + the injected fake
// commerce, returning the app and the fake for assertions.
func mount(t *testing.T) (*zip.App, *cloud.Service[state], *fakeCommerce) {
	t.Helper()
	store, err := openStore(t.TempDir() + "/referrals.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fc := newFakeCommerce()
	s := &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}, "referrals"),
		State: state{
			store:    store,
			commerce: fc,
			linkBase: "https://hanzo.ai",
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return app, s, fc
}

// req drives one HTTP request. org sets a VALIDATED principal (X-Org-Id +
// X-User-Id, the tenant() gate); admin additionally sets X-User-IsAdmin.
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

// TestDeriveCodeDeterministic: the code is a STABLE, deterministic function of the
// org (same org ⇒ same code, always), distinct orgs ⇒ distinct codes, 8-char
// uppercase base32.
func TestDeriveCodeDeterministic(t *testing.T) {
	a1 := deriveCode("maxpower", 0)
	a2 := deriveCode("maxpower", 0)
	b := deriveCode("acme", 0)
	if a1 != a2 {
		t.Fatalf("deriveCode not deterministic: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("distinct orgs collided: both %q", a1)
	}
	if len(a1) != 8 {
		t.Fatalf("code length want 8, got %d (%q)", len(a1), a1)
	}
	for _, r := range a1 {
		if !((r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7')) {
			t.Fatalf("code %q has non-base32 char %q", a1, r)
		}
	}
	// The salt (collision escape hatch) yields a DIFFERENT code for the same org.
	if deriveCode("maxpower", 1) == a1 {
		t.Fatalf("salted code equals unsalted")
	}
}

// TestEnsureCodeStableAndReversible: EnsureCode is idempotent per org and
// OrgForCode reverses it (case-insensitively).
func TestEnsureCodeStableAndReversible(t *testing.T) {
	_, s, _ := mount(t)
	ctx := context.Background()

	c1, err := s.State.store.EnsureCode(ctx, "maxpower")
	if err != nil {
		t.Fatalf("EnsureCode: %v", err)
	}
	c2, _ := s.State.store.EnsureCode(ctx, "maxpower")
	if c1 != c2 {
		t.Fatalf("EnsureCode not stable: %q != %q", c1, c2)
	}
	if c1 != deriveCode("maxpower", 0) {
		t.Fatalf("EnsureCode %q != deriveCode %q", c1, deriveCode("maxpower", 0))
	}
	org, err := s.State.store.OrgForCode(ctx, "  "+lower(c1)+"  ") // whitespace + wrong case
	if err != nil {
		t.Fatalf("OrgForCode: %v", err)
	}
	if org != "maxpower" {
		t.Fatalf("OrgForCode want maxpower, got %q", org)
	}
	if _, err := s.State.store.OrgForCode(ctx, "ZZZZZZZZ"); err != errUnknownCode {
		t.Fatalf("unknown code want errUnknownCode, got %v", err)
	}
}

// TestClaimSelfAndIdempotent: self-referral is blocked; a repeat claim (same or
// different code) is idempotent and returns the FIRST edge (first-touch wins).
func TestClaimSelfAndIdempotent(t *testing.T) {
	app, s, _ := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	cCode, _ := s.State.store.EnsureCode(ctx, "orgC")

	// No principal → 403.
	if code, _ := req(t, app, http.MethodPost, "/v1/referrals/claim", "", false, map[string]any{"code": aCode}); code != http.StatusForbidden {
		t.Fatalf("no-principal claim want 403, got %d", code)
	}
	// Unknown code → 404.
	if code, _ := req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": "ZZZZZZZZ"}); code != http.StatusNotFound {
		t.Fatalf("unknown-code claim want 404, got %d", code)
	}
	// Self-referral (orgA claims orgA's own code) → 400.
	if code, _ := req(t, app, http.MethodPost, "/v1/referrals/claim", "orgA", false, map[string]any{"code": aCode}); code != http.StatusBadRequest {
		t.Fatalf("self-referral want 400, got %d", code)
	}
	// orgB claims orgA's code → 201 created.
	code, body := req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": aCode})
	if code != http.StatusCreated {
		t.Fatalf("first claim want 201, got %d (%s)", code, body)
	}
	// Re-claim (same code) → 200, not created (idempotent).
	code, body = req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": aCode})
	if code != http.StatusOK {
		t.Fatalf("re-claim want 200, got %d (%s)", code, body)
	}
	var re struct {
		Created bool   `json:"created"`
		Code    string `json:"code"`
	}
	_ = json.Unmarshal(body, &re)
	if re.Created {
		t.Fatalf("re-claim reported created=true")
	}
	// orgB tries a DIFFERENT code (orgC's) → still bound to the FIRST (orgA), first-touch.
	req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": cCode})
	ref, err := s.State.store.getByReferee(ctx, "orgB")
	if err != nil {
		t.Fatalf("getByReferee: %v", err)
	}
	if ref.ReferrerOrg != "orgA" || ref.Code != aCode {
		t.Fatalf("first-touch broken: referrer=%q code=%q (want orgA/%s)", ref.ReferrerOrg, ref.Code, aCode)
	}
}

// TestQualifyGrantsBothSidesOnceAndBalancesMove is the CORE proof: a referee that
// qualifies (metered spend) triggers a DOUBLE grant — referrer +$10, referee +$5 —
// and the grant is at-most-once (a re-sweep never double-pays). Balances are
// asserted through the (fake) commerce ledger, exactly the balance API a live
// proof reads.
func TestQualifyGrantsBothSidesOnceAndBalancesMove(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")

	// orgB signs up via orgA's code.
	if code, _ := req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": aCode}); code != http.StatusCreated {
		t.Fatalf("claim want 201, got %d", code)
	}

	// Not yet qualified (no spend): an admin sweep grants NOTHING; balances stay 0.
	code, body := req(t, app, http.MethodPost, "/v1/admin/referrals/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("sweep want 200, got %d (%s)", code, body)
	}
	if got := credited(body); got != 0 {
		t.Fatalf("pre-qualify sweep credited=%d, want 0", got)
	}
	if fc.bal("orgA") != 0 || fc.bal("orgB") != 0 || fc.depositCount() != 0 {
		t.Fatalf("pre-qualify balances moved: A=%d B=%d deposits=%d", fc.bal("orgA"), fc.bal("orgB"), fc.depositCount())
	}

	// orgB makes metered spend → now qualifies.
	fc.setSpend("orgB", 42)

	// Sweep → BOTH balances move: A +$10 (1000c), B +$5 (500c). Exactly 2 deposits.
	code, body = req(t, app, http.MethodPost, "/v1/admin/referrals/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("qualify sweep want 200, got %d (%s)", code, body)
	}
	if got := credited(body); got != 1 {
		t.Fatalf("qualify sweep credited=%d, want 1", got)
	}
	if fc.bal("orgA") != referrerBonusCents {
		t.Fatalf("referrer balance = %d, want %d", fc.bal("orgA"), referrerBonusCents)
	}
	if fc.bal("orgB") != refereeBonusCents {
		t.Fatalf("referee balance = %d, want %d", fc.bal("orgB"), refereeBonusCents)
	}
	if fc.depositCount() != 2 {
		t.Fatalf("deposit count = %d, want 2 (one per side)", fc.depositCount())
	}

	// IDEMPOTENT: a re-sweep + a referrer page load must NOT grant again.
	req(t, app, http.MethodPost, "/v1/admin/referrals/sweep", "admin", true, nil)
	req(t, app, http.MethodGet, "/v1/referrals", "orgA", false, nil) // lazy check re-runs
	if fc.bal("orgA") != referrerBonusCents || fc.bal("orgB") != refereeBonusCents {
		t.Fatalf("double-grant! A=%d B=%d", fc.bal("orgA"), fc.bal("orgB"))
	}
	if fc.depositCount() != 2 {
		t.Fatalf("idempotency broken: deposit count = %d, want 2", fc.depositCount())
	}
}

// TestLazyQualifyOnReferrerRead proves the referrer's own GET /v1/referrals runs
// the qualify check (self-updating page) — no admin sweep needed — and the view
// reports the earned credit + credited status.
func TestLazyQualifyOnReferrerRead(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": aCode})
	fc.setSpend("orgB", 7) // qualifies

	// orgA loads their referrals page → the lazy check grants both sides.
	code, body := req(t, app, http.MethodGet, "/v1/referrals", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/referrals want 200, got %d (%s)", code, body)
	}
	var view struct {
		Code               string `json:"code"`
		Link               string `json:"link"`
		CreditsEarnedCents int64  `json:"creditsEarnedCents"`
		Counts             struct {
			Total, Credited int
		} `json:"counts"`
		Referrals []struct {
			Referee      string `json:"referee"`
			Status       string `json:"status"`
			CreditsCents int64  `json:"creditsCents"`
		} `json:"referrals"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if view.Code != aCode {
		t.Fatalf("code = %q, want %q", view.Code, aCode)
	}
	if view.Link != "https://hanzo.ai/?ref="+aCode {
		t.Fatalf("link = %q", view.Link)
	}
	if view.CreditsEarnedCents != referrerBonusCents {
		t.Fatalf("creditsEarned = %d, want %d", view.CreditsEarnedCents, referrerBonusCents)
	}
	if view.Counts.Credited != 1 || len(view.Referrals) != 1 || view.Referrals[0].Status != StatusCredited {
		t.Fatalf("view not credited: %+v", view)
	}
	if view.Referrals[0].CreditsCents != referrerBonusCents {
		t.Fatalf("row credits = %d, want %d", view.Referrals[0].CreditsCents, referrerBonusCents)
	}
	// Balances moved (via commerce).
	if fc.bal("orgA") != referrerBonusCents || fc.bal("orgB") != refereeBonusCents {
		t.Fatalf("balances: A=%d B=%d", fc.bal("orgA"), fc.bal("orgB"))
	}
	_ = ctx
}

// TestAdminGateAndDirectory: /v1/admin/referrals is global-admin fail-closed, and
// exposes both orgs + a summary.
func TestAdminGateAndDirectory(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	req(t, app, http.MethodPost, "/v1/referrals/claim", "orgB", false, map[string]any{"code": aCode})
	fc.setSpend("orgB", 5)
	req(t, app, http.MethodPost, "/v1/admin/referrals/sweep", "admin", true, nil)

	// A non-admin (validated tenant) is refused 403 on BOTH admin routes.
	if code, _ := req(t, app, http.MethodGet, "/v1/admin/referrals", "orgA", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin GET admin want 403, got %d", code)
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/admin/referrals/sweep", "orgA", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin sweep want 403, got %d", code)
	}

	// Global admin sees the directory with both orgs + summary.
	code, body := req(t, app, http.MethodGet, "/v1/admin/referrals", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("admin list want 200, got %d (%s)", code, body)
	}
	var env struct {
		Data struct {
			Referrals []adminReferralView `json:"referrals"`
			Summary   adminSummary        `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := env.Data
	if len(out.Referrals) != 1 {
		t.Fatalf("admin referrals len = %d, want 1", len(out.Referrals))
	}
	r0 := out.Referrals[0]
	if r0.ReferrerOrg != "orgA" || r0.RefereeOrg != "orgB" || r0.Status != StatusCredited {
		t.Fatalf("admin row wrong: %+v", r0)
	}
	if out.Summary.Total != 1 || out.Summary.Credited != 1 || out.Summary.GrantedCents != referrerBonusCents+refereeBonusCents {
		t.Fatalf("summary wrong: %+v", out.Summary)
	}
}

// credited pulls the "credited" count out of an ENVELOPED sweep response
// ({status,msg,data:{swept,credited}}).
func credited(body []byte) int {
	var out struct {
		Data struct {
			Credited int `json:"credited"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Data.Credited
}

// lower is a tiny helper (avoid importing strings just for the test).
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// TestMount exercises the real Mount wiring (store open + route registration)
// against a temp DataDir, proving the package boots as the binary loads it.
func TestMount(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	// A no-principal GET is refused 403 (proves the route is bound + gated).
	r := httptest.NewRequest(http.MethodGet, "/v1/referrals", nil)
	resp, err := app.Fiber().Test(r)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mounted GET /v1/referrals (no principal) want 403, got %d", resp.StatusCode)
	}
}
