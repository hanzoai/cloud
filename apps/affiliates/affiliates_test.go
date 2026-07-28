package affiliates

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
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// fakeCommerce is an in-memory commerce ledger: it records deposits per org (the
// wallet balance) and lets a test SET a referred org's metered spend (the accrual
// base). It is the money-seam stand-in that lets the tests PROVE commission accrues
// (spend × rate) and a credits payout moves a wallet — without a live commerce.
type fakeCommerce struct {
	mu       sync.Mutex
	balance  map[string]int64 // org → deposited cents (the wallet)
	spend    map[string]int64 // org → metered spend cents (accrual base)
	deposits int              // total deposit calls (one-grant proof)
	failDep  bool             // when true, deposit errors (at-most-pending log path)
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

// mount builds an affiliates app backed by a fresh store + the injected fake
// commerce, returning the app, the service, and the fake for assertions.
func mount(t *testing.T) (*zip.App, *cloud.Service[state], *fakeCommerce) {
	t.Helper()
	store, err := openStore(t.TempDir() + "/affiliates.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fc := newFakeCommerce()
	s := &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}, "affiliates"),
		State: state{
			store:    store,
			commerce: fc,
			clicks:   newClicks(),
			linkBase: "https://hanzo.ai",
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return app, s, fc
}

// share is the profit-share the accrual computes for a source org's spend at a level
// rate: Hanzo's margin on that spend × the rate, in the SAME two-step integer division
// as accrueSource/sweepAffiliate (margin first, then rate), so the test math matches
// the code exactly. defaultMarginBps is the margin the test mount uses.
func share(spendCents, rateBps int64) int64 {
	return marginOf(spendCents, defaultMarginBps) * rateBps / bpsDenom
}

// req drives one HTTP request. org sets a VALIDATED principal (X-Org-Id +
// X-User-Id, the Tenant() gate); admin additionally sets X-User-IsAdmin.
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
	// A generous ceiling: a correct request completes in well under 100ms, so 30s
	// never fires spuriously — it only guards a genuine hang. The fiber default is 1s,
	// which flakes under CI/machine load, not on request latency.
	resp, err := app.Fiber().Test(hr, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// envData pulls the `data` object out of an admin envelope {status,msg,data}.
func envData(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Status string                     `json:"status"`
		Data   map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	return env.Data
}

// applyAndApprove applies for `org` (optional requested code) and staff-approves it
// with `approveCode`, returning the affiliate id and its minted code.
func applyAndApprove(t *testing.T, app *zip.App, s *cloud.Service[state], org, requestedCode, approveCode string) (id, code string) {
	t.Helper()
	code2, body := req(t, app, http.MethodPost, "/v1/affiliates/apply", org, false, map[string]any{"requestedCode": requestedCode})
	if code2 != http.StatusCreated {
		t.Fatalf("apply(%s) want 201, got %d (%s)", org, code2, body)
	}
	var ar struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &ar)
	if ar.ID == "" {
		t.Fatalf("apply(%s) returned no id (%s)", org, body)
	}
	st, ab := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+ar.ID+"/approve", "admin", true, map[string]any{"code": approveCode})
	if st != http.StatusOK {
		t.Fatalf("approve(%s) want 200, got %d (%s)", org, st, ab)
	}
	a, err := s.State.store.GetByID(context.Background(), ar.ID)
	if err != nil {
		t.Fatalf("GetByID after approve: %v", err)
	}
	if a.Status != StatusApproved {
		t.Fatalf("approve(%s) status = %q, want approved", org, a.Status)
	}
	return a.ID, a.Code
}

// TestDeriveCodeAndValidCode: derived codes are stable + distinct + lowercase
// base32; validCode enforces the vanity charset.
func TestDeriveCodeAndValidCode(t *testing.T) {
	a1 := deriveCode("maxpower", 0)
	if a1 != deriveCode("maxpower", 0) {
		t.Fatalf("deriveCode not deterministic")
	}
	if a1 == deriveCode("acme", 0) {
		t.Fatalf("distinct orgs collided: %q", a1)
	}
	if deriveCode("maxpower", 1) == a1 {
		t.Fatalf("salted code equals unsalted")
	}
	for _, r := range a1 {
		if !((r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')) {
			t.Fatalf("derived code %q has non-lowercase-base32 char %q", a1, r)
		}
	}
	for _, ok := range []string{"acme", "acme-labs", "a1b2", "launch2026"} {
		if !validCode(ok) {
			t.Fatalf("validCode(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "ab", "-acme", "acme-", "ACME", "a b", "acme_labs", "acmé"} {
		if validCode(bad) {
			t.Fatalf("validCode(%q) = true, want false", bad)
		}
	}
}

// TestApplyIdempotent: an org applies once → applied; a second apply is a no-op
// returning the FIRST record (first apply wins). No principal → 403.
func TestApplyIdempotent(t *testing.T) {
	app, s, _ := mount(t)

	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/apply", "", false, map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("no-principal apply want 403, got %d", code)
	}
	// Malformed vanity code → 400.
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/apply", "orgA", false, map[string]any{"requestedCode": "x"}); code != http.StatusBadRequest {
		t.Fatalf("bad vanity code want 400, got %d", code)
	}
	// First apply → 201 applied, default rate.
	code, body := req(t, app, http.MethodPost, "/v1/affiliates/apply", "orgA", false, map[string]any{"requestedCode": "acme"})
	if code != http.StatusCreated {
		t.Fatalf("first apply want 201, got %d (%s)", code, body)
	}
	var a1 struct {
		ID, Status, RequestedCode string
		RateBps                   int64
		Created                   bool
	}
	_ = json.Unmarshal(body, &a1)
	if a1.Status != StatusApplied || a1.RateBps != defaultRateBps || a1.RequestedCode != "acme" || !a1.Created {
		t.Fatalf("first apply wrong: %+v", a1)
	}
	// Re-apply → 200, not created; SAME record (first wins, requested code preserved).
	code, body = req(t, app, http.MethodPost, "/v1/affiliates/apply", "orgA", false, map[string]any{"requestedCode": "different"})
	if code != http.StatusOK {
		t.Fatalf("re-apply want 200, got %d (%s)", code, body)
	}
	var a2 struct {
		ID, RequestedCode string
		Created           bool
	}
	_ = json.Unmarshal(body, &a2)
	if a2.Created || a2.ID != a1.ID || a2.RequestedCode != "acme" {
		t.Fatalf("re-apply not idempotent-first-wins: %+v", a2)
	}
	_ = s
}

// TestApproveMintsCodeAndVanityUniqueness: approve mints the code (vanity, explicit
// override, or derived); a vanity code is uniqueness-enforced across affiliates.
func TestApproveMintsCodeAndVanityUniqueness(t *testing.T) {
	app, s, _ := mount(t)
	ctx := context.Background()

	// orgA requested "launch"; approve mints it.
	idA, codeA := applyAndApprove(t, app, s, "orgA", "launch", "")
	if codeA != "launch" {
		t.Fatalf("orgA code = %q, want launch (the requested vanity)", codeA)
	}
	// orgB requests the SAME vanity → approve is a 409 conflict.
	_, bBody := req(t, app, http.MethodPost, "/v1/affiliates/apply", "orgB", false, map[string]any{"requestedCode": "launch"})
	var b struct{ ID string }
	_ = json.Unmarshal(bBody, &b)
	if st, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+b.ID+"/approve", "admin", true, nil); st != http.StatusConflict {
		t.Fatalf("duplicate vanity approve want 409, got %d (%s)", st, body)
	}
	// Re-approve orgB with an explicit free override → 200, that code minted.
	if st, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+b.ID+"/approve", "admin", true, map[string]any{"code": "launch-b"}); st != http.StatusOK {
		t.Fatalf("override approve want 200, got %d (%s)", st, body)
	}
	bAff, _ := s.State.store.GetByID(ctx, b.ID)
	if bAff.Code != "launch-b" || bAff.Status != StatusApproved {
		t.Fatalf("orgB after override: code=%q status=%q", bAff.Code, bAff.Status)
	}
	// orgC requested nothing → approve derives a stable slug (non-empty, valid).
	_, codeC := applyAndApprove(t, app, s, "orgC", "", "")
	if codeC == "" || !validCode(codeC) || codeC == "launch" || codeC == "launch-b" {
		t.Fatalf("orgC derived code invalid/colliding: %q", codeC)
	}
	// Approve on a missing id → 404.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/aff_missing/approve", "admin", true, nil); st != http.StatusNotFound {
		t.Fatalf("approve missing want 404, got %d", st)
	}
	_ = idA
}

// TestAttributeSelfUnknownAndIdempotent: an ?aff code resolves ONLY for an approved
// affiliate; self-attribution is blocked; first-touch is idempotent.
func TestAttributeSelfUnknownAndIdempotent(t *testing.T) {
	app, s, _ := mount(t)
	ctx := context.Background()

	// An un-approved affiliate has no code → its code can't be attributed.
	req(t, app, http.MethodPost, "/v1/affiliates/apply", "orgPending", false, map[string]any{"requestedCode": "pending1"})
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgX", false, map[string]any{"code": "pending1"}); code != http.StatusNotFound {
		t.Fatalf("attribute to un-approved code want 404, got %d", code)
	}

	_, codeA := applyAndApprove(t, app, s, "orgA", "acme", "")

	// No principal → 403.
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "", false, map[string]any{"code": codeA}); code != http.StatusForbidden {
		t.Fatalf("no-principal attribute want 403, got %d", code)
	}
	// Unknown code → 404.
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": "nope-nope"}); code != http.StatusNotFound {
		t.Fatalf("unknown-code attribute want 404, got %d", code)
	}
	// Self-attribution (orgA uses its OWN code) → 400.
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgA", false, map[string]any{"code": codeA}); code != http.StatusBadRequest {
		t.Fatalf("self-attribution want 400, got %d", code)
	}
	// orgB attributes to orgA → 201.
	if code, body := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeA}); code != http.StatusCreated {
		t.Fatalf("first attribute want 201, got %d (%s)", code, body)
	}
	// Re-attribute (same code) → 200, not created (idempotent, first-touch).
	code, body := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeA})
	if code != http.StatusOK {
		t.Fatalf("re-attribute want 200, got %d (%s)", code, body)
	}
	var re struct {
		Created bool `json:"created"`
	}
	_ = json.Unmarshal(body, &re)
	if re.Created {
		t.Fatalf("re-attribute reported created=true")
	}
	// orgB tries a DIFFERENT affiliate's code → still bound to the FIRST (orgA).
	_, codeC := applyAndApprove(t, app, s, "orgC", "cee", "")
	req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeC})
	edge, err := s.State.store.getReferralByReferred(ctx, "orgB")
	if err != nil {
		t.Fatalf("getReferralByReferred: %v", err)
	}
	if edge.Code != codeA {
		t.Fatalf("first-touch broken: code=%q (want %s)", edge.Code, codeA)
	}
}

// TestSweepAccruesSpendTimesRateIdempotent is the CORE proof: after a referred org
// makes metered spend, the sweep accrues commission = spend × rate into the
// affiliate's balance, at-most-once per period (a re-sweep never double-accrues).
func TestSweepAccruesSpendTimesRateIdempotent(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	idA, codeA := applyAndApprove(t, app, s, "orgA", "acme", "")

	// orgB signs up via orgA's link.
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeA}); code != http.StatusCreated {
		t.Fatalf("attribute want 201, got %d", code)
	}

	// No spend yet: a sweep accrues NOTHING.
	code, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("pre-spend sweep want 200, got %d (%s)", code, body)
	}
	if got := sweptAccrued(t, body); got != 0 {
		t.Fatalf("pre-spend sweep accrued=%d, want 0", got)
	}

	// orgB spends $100 (10000c). Commission @20% of the 40% margin = $8 (800c).
	fc.setSpend("orgB", 10000)

	code, body = req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("accrual sweep want 200, got %d (%s)", code, body)
	}
	if got := sweptAccrued(t, body); got != 1 {
		t.Fatalf("accrual sweep accrued=%d, want 1", got)
	}
	a, _ := s.State.store.GetByID(ctx, idA)
	wantCommission := share(10000, defaultRateBps) // margin × rate
	if a.AccruedCents != wantCommission {
		t.Fatalf("accrued = %d, want %d (margin×rate)", a.AccruedCents, wantCommission)
	}
	if a.PendingCents() != wantCommission {
		t.Fatalf("pending = %d, want %d", a.PendingCents(), wantCommission)
	}

	// IDEMPOTENT: a re-sweep in the SAME period accrues nothing more.
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	a2, _ := s.State.store.GetByID(ctx, idA)
	if a2.AccruedCents != wantCommission {
		t.Fatalf("double-accrual! accrued = %d, want %d", a2.AccruedCents, wantCommission)
	}
	// No wallet moved yet (accrual is not a payout).
	if fc.depositCount() != 0 {
		t.Fatalf("accrual issued a deposit (%d) — it must not", fc.depositCount())
	}
}

// TestLazyAccrualOnAffiliateRead proves the affiliate's OWN GET /v1/affiliates runs
// the accrual sweep for its referred orgs (self-updating dashboard).
func TestLazyAccrualOnAffiliateRead(t *testing.T) {
	app, s, fc := mount(t)
	_, codeA := applyAndApprove(t, app, s, "orgA", "acme", "")
	req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeA})
	fc.setSpend("orgB", 5000) // $50 → 40% margin $20 → 20% share = 400c ($4)

	code, body := req(t, app, http.MethodGet, "/v1/affiliates", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/affiliates want 200, got %d (%s)", code, body)
	}
	var v struct {
		IsAffiliate   bool   `json:"isAffiliate"`
		Status        string `json:"status"`
		Code          string `json:"code"`
		Link          string `json:"link"`
		ReferredCount int    `json:"referredCount"`
		AccruedCents  int64  `json:"accruedCents"`
		PendingCents  int64  `json:"pendingCents"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !v.IsAffiliate || v.Status != StatusApproved || v.Code != codeA {
		t.Fatalf("dashboard head wrong: %+v", v)
	}
	if v.Link != "https://hanzo.ai/?aff="+codeA {
		t.Fatalf("link = %q", v.Link)
	}
	want := share(5000, defaultRateBps) // margin × rate
	if v.ReferredCount != 1 || v.AccruedCents != want || v.PendingCents != want {
		t.Fatalf("lazy accrual not reflected: %+v (want accrued %d)", v, want)
	}
}

// TestPayoutCreditsOneGrantCashRecordOnlyAndPendingGuard: a credits payout issues
// exactly ONE commerce grant + moves paid; a cash payout is record-only; a payout
// can never exceed pending.
func TestPayoutCreditsOneGrantCashRecordOnlyAndPendingGuard(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	idA, codeA := applyAndApprove(t, app, s, "orgA", "acme", "")
	req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeA})
	fc.setSpend("orgB", 25000) // 40% margin = 10000c; @20% share = 2000c pending
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

	// Non-admin is refused on payout.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/payout", "orgA", false, map[string]any{"amountCents": 100, "method": "credits"}); st != http.StatusForbidden {
		t.Fatalf("non-admin payout want 403, got %d", st)
	}
	// Over-pending → 400 (2000 available, ask 3000).
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/payout", "admin", true, map[string]any{"amountCents": 3000, "method": "credits"}); st != http.StatusBadRequest {
		t.Fatalf("over-pending payout want 400, got %d", st)
	}

	// Credits payout of 1200c → ONE grant into orgA's wallet, paid moves.
	st, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/payout", "admin", true, map[string]any{"amountCents": 1200, "method": "credits", "reference": "ledger-1"})
	if st != http.StatusOK {
		t.Fatalf("credits payout want 200, got %d (%s)", st, body)
	}
	if fc.bal("orgA") != 1200 {
		t.Fatalf("affiliate wallet = %d, want 1200 (the credits payout)", fc.bal("orgA"))
	}
	if fc.depositCount() != 1 {
		t.Fatalf("deposit count = %d, want 1 (one grant)", fc.depositCount())
	}
	a, _ := s.State.store.GetByID(ctx, idA)
	if a.PaidCents != 1200 || a.PendingCents() != 800 {
		t.Fatalf("after credits payout: paid=%d pending=%d (want 1200/800)", a.PaidCents, a.PendingCents())
	}
	// The payout row records the txn.
	pd := envData(t, body)
	var payout struct {
		AmountCents int64  `json:"amountCents"`
		Method      string `json:"method"`
		Txn         string `json:"txn"`
	}
	_ = json.Unmarshal(pd["payout"], &payout)
	if payout.AmountCents != 1200 || payout.Method != "credits" || payout.Txn == "" {
		t.Fatalf("payout view wrong: %+v", payout)
	}

	// Cash payout of the remaining 800c via wire → RECORD-ONLY (no new grant).
	st, body = req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/payout", "admin", true, map[string]any{"amountCents": 800, "method": "wire", "reference": "wire-xyz"})
	if st != http.StatusOK {
		t.Fatalf("cash payout want 200, got %d (%s)", st, body)
	}
	if fc.depositCount() != 1 {
		t.Fatalf("cash payout issued a grant: deposit count = %d, want 1", fc.depositCount())
	}
	if fc.bal("orgA") != 1200 {
		t.Fatalf("cash payout moved the wallet: bal = %d, want 1200", fc.bal("orgA"))
	}
	a, _ = s.State.store.GetByID(ctx, idA)
	if a.PaidCents != 2000 || a.PendingCents() != 0 {
		t.Fatalf("after cash payout: paid=%d pending=%d (want 2000/0)", a.PaidCents, a.PendingCents())
	}
	// Nothing left → any further payout is 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/payout", "admin", true, map[string]any{"amountCents": 1, "method": "credits"}); st != http.StatusBadRequest {
		t.Fatalf("drained payout want 400, got %d", st)
	}
}

// TestAdminGateAndDirectory: every /v1/admin/affiliates* route is SuperAdmin
// fail-closed, and the directory exposes orgs + a summary.
func TestAdminGateAndDirectory(t *testing.T) {
	app, s, fc := mount(t)
	idA, codeA := applyAndApprove(t, app, s, "orgA", "acme", "")
	req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgB", false, map[string]any{"code": codeA})
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

	// A non-admin tenant is refused 403 on EVERY admin route.
	for _, p := range []string{"/v1/admin/affiliates", "/v1/admin/affiliates/sweep",
		"/v1/admin/affiliates/" + idA + "/approve", "/v1/admin/affiliates/" + idA + "/suspend",
		"/v1/admin/affiliates/" + idA + "/payout"} {
		method := http.MethodPost
		if p == "/v1/admin/affiliates" {
			method = http.MethodGet
		}
		if code, _ := req(t, app, method, p, "orgA", false, map[string]any{"amountCents": 1, "method": "credits"}); code != http.StatusForbidden {
			t.Fatalf("non-admin %s want 403, got %d", p, code)
		}
	}

	// SuperAdmin sees the directory with the affiliate + a summary.
	code, body := req(t, app, http.MethodGet, "/v1/admin/affiliates", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("admin list want 200, got %d (%s)", code, body)
	}
	data := envData(t, body)
	var affs []adminAffiliateView
	if err := json.Unmarshal(data["affiliates"], &affs); err != nil {
		t.Fatalf("decode affiliates: %v", err)
	}
	if len(affs) != 1 {
		t.Fatalf("admin affiliates len = %d, want 1", len(affs))
	}
	a0 := affs[0]
	if a0.Org != "orgA" || a0.Code != codeA || a0.Status != StatusApproved || a0.ReferredCount != 1 {
		t.Fatalf("admin row wrong: %+v", a0)
	}
	wantCommission := share(10000, defaultRateBps)
	if a0.AccruedCents != wantCommission || a0.PendingCents != wantCommission {
		t.Fatalf("admin row accrual: accrued=%d pending=%d, want %d", a0.AccruedCents, a0.PendingCents, wantCommission)
	}
	var sum adminSummary
	if err := json.Unmarshal(data["summary"], &sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if sum.Total != 1 || sum.Approved != 1 || sum.AccruedCents != wantCommission || sum.PendingCents != wantCommission {
		t.Fatalf("summary wrong: %+v", sum)
	}

	// Suspend flips status; the code stops resolving for new attribution.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/suspend", "admin", true, nil); st != http.StatusOK {
		t.Fatalf("suspend want 200, got %d", st)
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgD", false, map[string]any{"code": codeA}); code != http.StatusNotFound {
		t.Fatalf("attribute to suspended code want 404, got %d", code)
	}
}

// sweptAccrued pulls the "accrued" count out of an ENVELOPED sweep response.
func sweptAccrued(t *testing.T, body []byte) int {
	t.Helper()
	var out struct {
		Data struct {
			Accrued int `json:"accrued"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Data.Accrued
}

// attributeOK records org←code (org referred with code), asserting a 2xx.
func attributeOK(t *testing.T, app *zip.App, org, code string) {
	t.Helper()
	if st, body := req(t, app, http.MethodPost, "/v1/affiliates/attribute", org, false, map[string]any{"code": code}); st/100 != 2 {
		t.Fatalf("attribute %s←%s want 2xx, got %d (%s)", org, code, st, body)
	}
}

// TestMultiLevelUplineWalk is the CORE proof of the multi-level commission: a source
// org's spend pays its referredBy chain L1 20% / L2 5% / L3 2%, depth-capped at 3 (a
// 4th-level ancestor earns nothing). Chain: W←A←B←C←D (each attributed to the one
// above); orgD spends $100.
func TestMultiLevelUplineWalk(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()

	idW, codeW := applyAndApprove(t, app, s, "orgW", "www", "")
	idA, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	idB, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	idC, codeC := applyAndApprove(t, app, s, "orgC", "ccc", "")

	// Build the chain: A referred by W, B by A, C by B, D by C (D is a plain spender).
	attributeOK(t, app, "orgA", codeW)
	attributeOK(t, app, "orgB", codeA)
	attributeOK(t, app, "orgC", codeB)
	attributeOK(t, app, "orgD", codeC)

	// Only orgD spends — $100 (10000c).
	fc.setSpend("orgD", 10000)

	if st, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil); st != http.StatusOK {
		t.Fatalf("sweep want 200, got %d (%s)", st, body)
	}

	want := map[string]struct {
		id   string
		want int64
	}{
		"orgC": {idC, share(10000, 2000)},             // L1 @ 20% of the 40% margin
		"orgB": {idB, share(10000, defaultL2RateBps)}, // L2 @ 5% of margin
		"orgA": {idA, share(10000, defaultL3RateBps)}, // L3 @ 2% of margin
		"orgW": {idW, 0},                              // L4 — beyond depth cap, earns NOTHING
	}
	for org, w := range want {
		a, err := s.State.store.GetByID(ctx, w.id)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", org, err)
		}
		if a.AccruedCents != w.want {
			t.Fatalf("%s accrued = %d, want %d (margin × level rate)", org, a.AccruedCents, w.want)
		}
	}

	// The upline walk itself is depth-capped at 3 (C, B, A — NOT W).
	up, err := s.State.store.UplineOrgs(ctx, "orgD", maxDepth)
	if err != nil {
		t.Fatalf("UplineOrgs: %v", err)
	}
	if len(up) != 3 || up[0] != "orgC" || up[1] != "orgB" || up[2] != "orgA" {
		t.Fatalf("upline = %v, want [orgC orgB orgA]", up)
	}

	// Idempotent: a re-sweep in the same period accrues nothing more.
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	for org, w := range want {
		a, _ := s.State.store.GetByID(ctx, w.id)
		if a.AccruedCents != w.want {
			t.Fatalf("double-accrual for %s: %d, want %d", org, a.AccruedCents, w.want)
		}
	}
}

// TestCycleRejection proves the referredBy edge refuses to close a loop at set time —
// both a direct 2-node loop and a longer chain loop — while a non-cyclic sibling edge
// is still allowed.
func TestCycleRejection(t *testing.T) {
	app, s, _ := mount(t)
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	_, codeC := applyAndApprove(t, app, s, "orgC", "ccc", "")

	// A←B←C (B referred by A, C referred by B).
	attributeOK(t, app, "orgB", codeA)
	attributeOK(t, app, "orgC", codeB)

	// Direct cycle: orgA referred by orgB would close A↔B (B's upline reaches A).
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgA", false, map[string]any{"code": codeB}); st != http.StatusBadRequest {
		t.Fatalf("direct cycle want 400, got %d", st)
	}
	// Long cycle: orgA referred by orgC would close A→B→C→A (C's upline reaches A).
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgA", false, map[string]any{"code": codeC}); st != http.StatusBadRequest {
		t.Fatalf("long cycle want 400, got %d", st)
	}
	// A non-cyclic edge is still fine: orgD referred by orgC.
	attributeOK(t, app, "orgD", codeC)
	if cyc, err := s.State.store.wouldCycleOrg(context.Background(), "orgA", "orgB"); err != nil || !cyc {
		t.Fatalf("wouldCycleOrg(orgA, orgB) = %v,%v — want true", cyc, err)
	}
}

// TestReferredByImmutableOrg proves the org referredBy edge is set-once: a second
// attribution with a DIFFERENT affiliate's code is a no-op that leaves the FIRST
// referrer intact (first-touch wins).
func TestReferredByImmutableOrg(t *testing.T) {
	app, s, _ := mount(t)
	ctx := context.Background()
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")

	attributeOK(t, app, "orgX", codeA) // X referred by A (first touch)
	// A second, different code is accepted as a no-op (created=false), edge unchanged.
	st, body := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgX", false, map[string]any{"code": codeB})
	if st != http.StatusOK {
		t.Fatalf("re-attribute want 200, got %d (%s)", st, body)
	}
	var re struct {
		Created bool `json:"created"`
	}
	_ = json.Unmarshal(body, &re)
	if re.Created {
		t.Fatalf("re-attribute created a new edge — referredBy must be immutable")
	}
	r, ok, err := s.State.store.referrerOrgOf(ctx, "orgX")
	if err != nil || !ok {
		t.Fatalf("referrerOrgOf: %q ok=%v err=%v", r, ok, err)
	}
	if r != "orgA" {
		t.Fatalf("referrer of orgX = %q, want orgA (first-touch, immutable)", r)
	}
}

// TestReferredByImmutableAndCycleUser proves the USER-level referredBy edge is
// set-once (immutable, first wins), rejects self, and rejects cycles at set time —
// the same invariants as the org edge, exercised directly on the store.
func TestReferredByImmutableAndCycleUser(t *testing.T) {
	_, s, _ := mount(t)
	ctx := context.Background()

	// Self-referral refused.
	if _, err := s.State.store.SetUserReferrer(ctx, "u1", "u1", ""); err != errSelfAttribution {
		t.Fatalf("self user-referral err = %v, want errSelfAttribution", err)
	}
	// First link wins.
	if created, err := s.State.store.SetUserReferrer(ctx, "u1", "u2", "code"); err != nil || !created {
		t.Fatalf("first SetUserReferrer = %v,%v — want created,nil", created, err)
	}
	// Immutable: a second referrer for u1 is a no-op (first wins), u1's referrer stays u2.
	if created, err := s.State.store.SetUserReferrer(ctx, "u1", "u3", "code"); err != nil || created {
		t.Fatalf("second SetUserReferrer = %v,%v — want not-created,nil", created, err)
	}
	if r, ok, _ := s.State.store.referrerUserOf(ctx, "u1"); !ok || r != "u2" {
		t.Fatalf("referrer of u1 = %q,%v — want u2,true", r, ok)
	}
	// Cycle: u2 referred by u1 would close u1↔u2 (u1's upline reaches u2).
	if _, err := s.State.store.SetUserReferrer(ctx, "u2", "u1", ""); err != errCycle {
		t.Fatalf("user cycle err = %v, want errCycle", err)
	}
}

// TestAffiliatesMeSurface proves GET /v1/affiliates/me returns the caller's code,
// link, per-level downline breakdown (with each level's rate), and accrued totals.
func TestAffiliatesMeSurface(t *testing.T) {
	app, s, fc := mount(t)
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")

	// orgB referred by A (L1 below A); orgC referred by B (L2 below A); orgC spends.
	attributeOK(t, app, "orgB", codeA)
	attributeOK(t, app, "orgC", codeB)
	fc.setSpend("orgC", 10000) // pays B (L1 @20%) and A (L2 @5%)

	// A non-enrolled caller sees the schedule.
	code, body := req(t, app, http.MethodGet, "/v1/affiliates/me", "orgZ", false, nil)
	if code != http.StatusOK {
		t.Fatalf("me(orgZ) want 200, got %d", code)
	}
	var nz struct {
		IsAffiliate bool `json:"isAffiliate"`
	}
	_ = json.Unmarshal(body, &nz)
	if nz.IsAffiliate {
		t.Fatalf("orgZ should not be an affiliate")
	}

	// orgA's /me: L1 downline = orgB (1), L2 downline = orgC (1); lazy sweep accrues.
	code, body = req(t, app, http.MethodGet, "/v1/affiliates/me", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("me(orgA) want 200, got %d (%s)", code, body)
	}
	var v struct {
		IsAffiliate   bool   `json:"isAffiliate"`
		Code          string `json:"code"`
		Link          string `json:"link"`
		DownlineTotal int    `json:"downlineTotal"`
		AccruedCents  int64  `json:"accruedCents"`
		Levels        []struct {
			Level         int   `json:"level"`
			RateBps       int64 `json:"rateBps"`
			DownlineCount int   `json:"downlineCount"`
		} `json:"levels"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode /me: %v (%s)", err, body)
	}
	if !v.IsAffiliate || v.Code != codeA || v.Link != "https://hanzo.ai/?aff="+codeA {
		t.Fatalf("me head wrong: %+v", v)
	}
	if v.DownlineTotal != 2 || len(v.Levels) != maxDepth {
		t.Fatalf("me downline: total=%d levels=%d", v.DownlineTotal, len(v.Levels))
	}
	if v.Levels[0].Level != 1 || v.Levels[0].RateBps != defaultRateBps || v.Levels[0].DownlineCount != 1 {
		t.Fatalf("L1 row wrong: %+v", v.Levels[0])
	}
	if v.Levels[1].Level != 2 || v.Levels[1].RateBps != defaultL2RateBps || v.Levels[1].DownlineCount != 1 {
		t.Fatalf("L2 row wrong: %+v", v.Levels[1])
	}
	// A earns L2 on orgC's $100 spend = 5% of the 40% margin (lazy sweep from the read).
	if v.AccruedCents != share(10000, defaultL2RateBps) {
		t.Fatalf("A accrued via /me = %d, want %d", v.AccruedCents, share(10000, defaultL2RateBps))
	}
}

// TestAdminReferralsAnalytics proves the unified SuperAdmin board reports top
// referrers, conversion, and accrual liability by level — and is fail-closed.
func TestAdminReferralsAnalytics(t *testing.T) {
	app, s, fc := mount(t)
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	attributeOK(t, app, "orgB", codeA) // B referred by A
	attributeOK(t, app, "orgC", codeB) // C referred by B
	fc.setSpend("orgC", 10000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

	// Non-admin is refused.
	if st, _ := req(t, app, http.MethodGet, "/v1/admin/referrals", "orgA", false, nil); st != http.StatusForbidden {
		t.Fatalf("non-admin /v1/admin/referrals want 403, got %d", st)
	}

	code, body := req(t, app, http.MethodGet, "/v1/admin/referrals", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("admin referrals want 200, got %d (%s)", code, body)
	}
	data := envData(t, body)
	var conv struct {
		ReferredOrgs  int     `json:"referredOrgs"`
		ConvertedOrgs int     `json:"convertedOrgs"`
		RatePct       float64 `json:"ratePct"`
	}
	if err := json.Unmarshal(data["conversion"], &conv); err != nil {
		t.Fatalf("decode conversion: %v", err)
	}
	// Two referred orgs (B, C); one converted (C produced commission).
	if conv.ReferredOrgs != 2 || conv.ConvertedOrgs != 1 {
		t.Fatalf("conversion wrong: %+v", conv)
	}
	var byLevel struct {
		L1Cents int64 `json:"l1Cents"`
		L2Cents int64 `json:"l2Cents"`
	}
	if err := json.Unmarshal(data["accrualByLevel"], &byLevel); err != nil {
		t.Fatalf("decode accrualByLevel: %v", err)
	}
	// orgC's $100 spend, 40% margin: L1 to B = 20% of margin, L2 to A = 5% of margin.
	if byLevel.L1Cents != share(10000, defaultRateBps) || byLevel.L2Cents != share(10000, defaultL2RateBps) {
		t.Fatalf("accrualByLevel wrong: %+v", byLevel)
	}
	var leaders []referrerRow
	if err := json.Unmarshal(data["topReferrers"], &leaders); err != nil {
		t.Fatalf("decode topReferrers: %v", err)
	}
	if len(leaders) != 2 || leaders[0].Org != "orgB" || leaders[0].AccruedCents != share(10000, defaultRateBps) {
		t.Fatalf("top referrer wrong: %+v", leaders)
	}
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
	r := httptest.NewRequest(http.MethodGet, "/v1/affiliates", nil)
	resp, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mounted GET /v1/affiliates (no principal) want 403, got %d", resp.StatusCode)
	}
}
