package referral

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce is an in-memory stand-in for the ONE question this package asks the
// money plane: how much has this org spent? It cannot deposit, because the client it
// implements cannot deposit.
type fakeCommerce struct {
	mu     sync.Mutex
	spend  map[string]int64 // org → metered spend cents (qualify signal)
	reads  int              // spendCents calls (proves the sweep did the work)
	failOn string           // org whose spend read errors, to exercise "stays pending"
}

func newFakeCommerce() *fakeCommerce { return &fakeCommerce{spend: map[string]int64{}} }

func (f *fakeCommerce) spendCents(_ context.Context, org string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if org == f.failOn {
		return 0, errNoLedger
	}
	return f.spend[org], nil
}

func (f *fakeCommerce) setSpend(org string, cents int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spend[org] = cents
}

func (f *fakeCommerce) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// mount builds a referrals app backed by a fresh store + the injected commerce
// client, returning the app and the fake for assertions.
func mount(t *testing.T) (*zip.App, *cloud.Service[state], *fakeCommerce) {
	t.Helper()
	fc := newFakeCommerce()
	app, s := mountWith(t, fc)
	return app, s, fc
}

// mountWith is mount over an arbitrary commerce client, so a test can drive the real
// payout client at a stub server instead of the in-memory fake.
func mountWith(t *testing.T, c commerce) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Brand: "hanzo"}, "referrals"),
		State: state{
			store:    store,
			commerce: c,
			linkBase: "https://hanzo.ai",
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	return app, s
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// req drives one HTTP request. org sets a VALIDATED principal (X-Org-Id +
// X-User-Id, the principal.Acting gate); admin additionally sets X-User-IsAdmin.
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
	// Generous ceiling — the fiber default is 1s, which flakes under machine load.
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	if code, _ := req(t, app, http.MethodPost, "/v1/referral/claim", "", false, map[string]any{"code": aCode}); code != http.StatusForbidden {
		t.Fatalf("no-principal claim want 403, got %d", code)
	}
	// Unknown code → 404.
	if code, _ := req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": "ZZZZZZZZ"}); code != http.StatusNotFound {
		t.Fatalf("unknown-code claim want 404, got %d", code)
	}
	// Self-referral (orgA claims orgA's own code) → 400.
	if code, _ := req(t, app, http.MethodPost, "/v1/referral/claim", "orgA", false, map[string]any{"code": aCode}); code != http.StatusBadRequest {
		t.Fatalf("self-referral want 400, got %d", code)
	}
	// orgB claims orgA's code → 201 created.
	code, body := req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
	if code != http.StatusCreated {
		t.Fatalf("first claim want 201, got %d (%s)", code, body)
	}
	// Re-claim (same code) → 200, not created (idempotent).
	code, body = req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
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
	req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": cCode})
	ref, err := s.State.store.getByReferee(ctx, "orgB")
	if err != nil {
		t.Fatalf("getByReferee: %v", err)
	}
	if ref.ReferrerOrg != "orgA" || ref.Code != aCode {
		t.Fatalf("first-touch broken: referrer=%q code=%q (want orgA/%s)", ref.ReferrerOrg, ref.Code, aCode)
	}
}

// ── the P0 proofs: a GET grants nothing and changes nothing ──────────────────

// TestGetIsPureReadAndAdvancesNothing is THE regression test for the live defect
// this package shipped: GET /v1/referral ran a "lazy qualify sweep" that reached
// a deposit, so merely LOADING the referrals page minted platform credit.
//
// It sets up the exact state that used to mint — a claimed referral whose referee
// HAS metered spend, i.e. one that qualifies — then loads the page as the referrer
// and asserts the referral is untouched: still signup, qualifiedAt still 0. The GET
// is a report, not a transition.
func TestGetIsPureReadAndAdvancesNothing(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
	fc.setSpend("orgB", 5000) // orgB WOULD qualify — this is the minting precondition

	before, err := s.State.store.getByReferee(ctx, "orgB")
	if err != nil {
		t.Fatalf("getByReferee: %v", err)
	}

	// Load the page repeatedly — the old code granted on every load.
	for range 3 {
		code, body := req(t, app, http.MethodGet, "/v1/referral", "orgA", false, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/referral want 200, got %d (%s)", code, body)
		}
	}

	after, err := s.State.store.getByReferee(ctx, "orgB")
	if err != nil {
		t.Fatalf("getByReferee: %v", err)
	}
	if after.Status != StatusSignup {
		t.Fatalf("GET advanced the referral: status %q → %q (a read must not transition state)", before.Status, after.Status)
	}
	if after.QualifiedAt != 0 {
		t.Fatalf("GET set qualifiedAt=%d — a read must not write", after.QualifiedAt)
	}
	if after != before {
		t.Fatalf("GET mutated the referral row:\n before %+v\n after  %+v", before, after)
	}
	// The read never even ASKS the money plane: no qualify check, so no spend read.
	if n := fc.readCount(); n != 0 {
		t.Fatalf("GET made %d commerce read(s); a pure read touches the money plane 0 times", n)
	}

	// And the capability is not lost — it moved to the gated write. One admin sweep
	// qualifies it, which is the ONLY door.
	if code, body := req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil); code != http.StatusOK {
		t.Fatalf("sweep want 200, got %d (%s)", code, body)
	}
	swept, _ := s.State.store.getByReferee(ctx, "orgB")
	if swept.Status != StatusQualified || swept.QualifiedAt == 0 {
		t.Fatalf("admin sweep did not qualify: %+v", swept)
	}
}

// TestLedgerReceivesZeroDeposits proves the absence of the mint at the WIRE, not
// at an interface a test could fake into agreement: the service is bound to the
// REAL payout client, and the money plane it reaches is stood up here with the
// two money-MOVING ops registered beside the read — each one failing the test if
// it is ever invoked.
//
// It then drives every route on the surface, in the state that used to pay out.
// The only op commerce may see is the read.
//
// It used to point the real client at an httptest.Server and assert on paths.
// That server answered a wire this fleet does not serve: co-resident, the
// transport dispatched GET /v1/billing/usage/rollup back into a router with no
// such route, and split, the client had no address at all. The proof was real and
// the wire under it was not.
func TestLedgerReceivesZeroDeposits(t *testing.T) {
	var mu sync.Mutex
	var hits []string

	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	p := cloud.Plane()
	zip.Post[plane.SpendIn, plane.Spend](p, "/finance/spend",
		func(_ context.Context, _ *plane.SpendIn) (*plane.Spend, error) {
			mu.Lock()
			hits = append(hits, plane.FinanceSpend)
			mu.Unlock()
			// The qualify signal: the referee has spent.
			return &plane.Spend{
				Consumed: plane.Money{Decimal: "42.42", Currency: "USD"},
				Balance:  plane.Money{Decimal: "0", Currency: "USD"},
			}, nil
		}, zip.WithOperationID(plane.FinanceSpend))

	// The two money-IN ops, live on the same plane and reachable by name. ANY
	// call to either is the bug this test exists to catch — and unlike a stub
	// server keyed on a URL, a caller cannot reach these by accident through a
	// path it half-matched. It has to name the op.
	mint := func(op string) func(context.Context, *plane.CreditIn) (*plane.Credited, error) {
		return func(_ context.Context, _ *plane.CreditIn) (*plane.Credited, error) {
			mu.Lock()
			hits = append(hits, op)
			mu.Unlock()
			t.Errorf("the referrals surface called %s — it issues no credit; a referral reward is an affiliate payable, settled by wire or wallet", op)
			return nil, errors.New("refused")
		}
	}
	zip.Post[plane.CreditIn, plane.Credited](p, "/finance/credit", mint(plane.FinanceCredit),
		zip.WithOperationID(plane.FinanceCredit))
	zip.Post[plane.CreditIn, plane.Credited](p, "/finance/deposit", mint("finance_deposit_probe"),
		zip.WithOperationID("finance_deposit_probe"))

	sock := zip.SocketPath("commerce")
	go func() { _ = p.Listen(sock) }()
	t.Cleanup(func() { _ = p.Shutdown() })
	for range 300 {
		if c, derr := net.Dial("unix", sock); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	app, s := mountWith(t, newCommerceClient())
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")

	// Exercise the whole surface in the qualifying state.
	req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
	req(t, app, http.MethodGet, "/v1/referral", "orgA", false, nil)
	req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)
	req(t, app, http.MethodGet, "/v1/referral", "orgA", false, nil)
	req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)
	req(t, app, http.MethodGet, "/v1/admin/referral/bonuses", "admin", true, nil)

	mu.Lock()
	defer mu.Unlock()
	if len(hits) == 0 {
		t.Fatal("the money plane was never reached at all — the no-mint proof would be vacuous")
	}
	for _, h := range hits {
		if h != plane.FinanceSpend {
			t.Fatalf("unexpected commerce op %q — the only op this surface may invoke is the spend read (all: %v)", h, hits)
		}
	}
	// The referral did qualify over that run, so the surface was genuinely exercised
	// in the state that used to pay — the zero above is not a vacuous zero.
	ref, err := s.State.store.getByReferee(ctx, "orgB")
	if err != nil {
		t.Fatalf("getByReferee: %v", err)
	}
	if ref.Status != StatusQualified {
		t.Fatalf("referral never qualified (%+v) — the no-deposit proof would be vacuous", ref)
	}
}

// TestCommerceClientIsReadOnly pins the SHAPE of the money client. The mint existed
// because the client carried a deposit method; with no write method on the interface,
// reviving the mint cannot be a one-line call — it has to start by re-declaring the
// capability here, in front of a test that says no.
func TestCommerceClientIsReadOnly(t *testing.T) {
	typ := reflect.TypeFor[commerce]()
	banned := []string{"deposit", "credit", "grant", "mint", "transfer", "refund", "charge", "payout"}
	for method := range typ.Methods() {
		name := strings.ToLower(method.Name)
		for _, b := range banned {
			if strings.Contains(name, b) {
				t.Fatalf("commerce client grew a money-moving method %q — referrals issues no credit; a referral reward is an affiliate payable in commerce, settled by wire or wallet", method.Name)
			}
		}
	}
	// And it is exactly the read it claims to be.
	if got := typ.NumMethod(); got != 1 {
		t.Fatalf("commerce client has %d methods, want 1 (spendCents). It asks ONE question", got)
	}
}

// TestWriteMethodsRequireOrg proves the gate is scoped by SAFE METHOD rather than
// by naming POST. The predecessor waved through everything that was not POST, which
// is the reasoning that let a GET reach a deposit; a verb this package does not even
// serve must still be refused without a principal, never silently allowed.
func TestWriteMethodsRequireOrg(t *testing.T) {
	app, _, _ := mount(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		code, _ := req(t, app, m, "/v1/referral/claim", "", false, map[string]any{"code": "ZZZZZZZZ"})
		if code == http.StatusOK || code == http.StatusCreated {
			t.Fatalf("%s with no principal was allowed (got %d)", m, code)
		}
	}
	// GET stays open to the gate (its handler does its own 403) so health probes work.
	if code, _ := req(t, app, http.MethodGet, "/v1/referral", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("no-principal GET want 403 from the handler, got %d", code)
	}
}

// ── qualification (the surviving capability) ─────────────────────────────────

// TestSweepQualifiesOnceAndIsIdempotent: the admin sweep advances a referee that
// has spent, exactly once, and a re-sweep is a no-op.
func TestSweepQualifiesOnceAndIsIdempotent(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")

	if code, _ := req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode}); code != http.StatusCreated {
		t.Fatalf("claim want 201, got %d", code)
	}

	// No spend yet → sweep qualifies nothing.
	code, body := req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("sweep want 200, got %d (%s)", code, body)
	}
	if got := qualifiedCount(body); got != 0 {
		t.Fatalf("pre-spend sweep qualified=%d, want 0", got)
	}

	// orgB makes metered spend → now qualifies.
	fc.setSpend("orgB", 42)
	code, body = req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("qualify sweep want 200, got %d (%s)", code, body)
	}
	if got := qualifiedCount(body); got != 1 {
		t.Fatalf("qualify sweep qualified=%d, want 1", got)
	}
	first, _ := s.State.store.getByReferee(ctx, "orgB")
	if first.Status != StatusQualified || first.QualifiedAt == 0 {
		t.Fatalf("not qualified: %+v", first)
	}

	// Re-sweep: already qualified, so it is no longer pending and nothing moves.
	_, body = req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)
	if got := qualifiedCount(body); got != 0 {
		t.Fatalf("re-sweep qualified=%d, want 0 (at-most-once)", got)
	}
	again, _ := s.State.store.getByReferee(ctx, "orgB")
	if again != first {
		t.Fatalf("re-sweep mutated the row:\n first %+v\n again %+v", first, again)
	}
}

// TestQualifyStaysPendingOnCommerceError: a money-plane hiccup leaves the referral
// honestly pending rather than qualifying it on a failed read.
func TestQualifyStaysPendingOnCommerceError(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
	fc.setSpend("orgB", 99)
	fc.failOn = "orgB"

	if code, _ := req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil); code != http.StatusOK {
		t.Fatalf("sweep want 200")
	}
	ref, _ := s.State.store.getByReferee(ctx, "orgB")
	if ref.Status != StatusSignup {
		t.Fatalf("commerce error qualified the referral anyway: %+v", ref)
	}
}

// TestMyReferralsView: the customer read reports code, link and attribution — and
// carries no money field, because there is no money.
func TestMyReferralsView(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
	fc.setSpend("orgB", 7)
	req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)

	code, body := req(t, app, http.MethodGet, "/v1/referral", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/referral want 200, got %d (%s)", code, body)
	}
	var view myReferrals
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if view.Code != aCode {
		t.Fatalf("code = %q, want %q", view.Code, aCode)
	}
	if view.Link != "https://hanzo.ai/?ref="+aCode {
		t.Fatalf("link = %q", view.Link)
	}
	if view.Counts.Total != 1 || view.Counts.Qualified != 1 {
		t.Fatalf("counts = %+v, want total=1 qualified=1", view.Counts)
	}
	if len(view.Referrals) != 1 || view.Referrals[0].Status != StatusQualified {
		t.Fatalf("rows = %+v", view.Referrals)
	}
	if view.Referrals[0].Referee != "orgB" {
		t.Fatalf("referee = %q, want orgB", view.Referrals[0].Referee)
	}
	// No credit vocabulary survives on the wire.
	assertNoMoneyKeys(t, body)
	_ = ctx
}

// TestAdminGateAndDirectory: /v1/admin/referral/bonuses is SuperAdmin fail-closed,
// and exposes both orgs + a summary with no amounts.
func TestAdminGateAndDirectory(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
	req(t, app, http.MethodPost, "/v1/referral/claim", "orgB", false, map[string]any{"code": aCode})
	fc.setSpend("orgB", 5)
	req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)

	// A non-admin (validated tenant) is refused 403 on BOTH admin routes.
	if code, _ := req(t, app, http.MethodGet, "/v1/admin/referral/bonuses", "orgA", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin GET admin want 403, got %d", code)
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "orgA", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin sweep want 403, got %d", code)
	}

	// SuperAdmin sees the directory with both orgs + summary.
	code, body := req(t, app, http.MethodGet, "/v1/admin/referral/bonuses", "admin", true, nil)
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
	if r0.ReferrerOrg != "orgA" || r0.RefereeOrg != "orgB" || r0.Status != StatusQualified {
		t.Fatalf("admin row wrong: %+v", r0)
	}
	if out.Summary.Total != 1 || out.Summary.Qualified != 1 {
		t.Fatalf("summary wrong: %+v", out.Summary)
	}
	assertNoMoneyKeys(t, body)
	_ = ctx
}

// assertNoMoneyKeys fails if a response carries any credit/grant vocabulary. The
// old wire advertised bonus amounts and granted cents; a field that can only ever
// report zero is a lie about what this surface does, so none may survive.
func assertNoMoneyKeys(t *testing.T, body []byte) {
	t.Helper()
	for _, k := range []string{
		"creditsEarnedCents", "creditsCents", "referrerBonusCents", "refereeBonusCents",
		"referrerGrantCents", "refereeGrantCents", "grantedCents", "referrerTxn", "refereeTxn",
		"creditedAt", "credited",
	} {
		if bytes.Contains(body, []byte(`"`+k+`"`)) {
			t.Fatalf("response still carries credit vocabulary %q: %s", k, body)
		}
	}
}

// qualifiedCount pulls the "qualified" count out of an ENVELOPED sweep response
// ({status,msg,data:{swept,qualified}}).
func qualifiedCount(body []byte) int {
	var out struct {
		Data struct {
			Qualified int `json:"qualified"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Data.Qualified
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
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir(), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	// A no-principal GET is refused 403 (proves the route is bound + gated).
	r := httptest.NewRequest(http.MethodGet, "/v1/referral", nil)
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mounted GET /v1/referral (no principal) want 403, got %d", resp.StatusCode)
	}
}
