package usage

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce stands in for the process that owns the ledger. It records the org
// each op was invoked FOR — which is the whole of tenant isolation here, now that
// the org rides the caller rather than a header the reader sets — and answers a
// canned total plus a recent-timestamped movement list so the category/series
// roll-up has data in-window.
type fakeCommerce struct {
	mu       sync.Mutex
	gotOrg   map[string]string // op -> the caller org it acted for
	hitPaths []string          // ops invoked, in order
}

// serve stands the money plane up the way Mount does and records the org each op
// was called FOR. The org is what tenant isolation turns on here, and it now
// rides the CALL rather than a header the reader set — so this is where the
// isolation is observed.
func (f *fakeCommerce) serve(t *testing.T) {
	t.Helper()
	f.gotOrg = map[string]string{}
	recent := time.Now().Add(-1 * time.Hour).UTC().Unix()

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

	record := func(ctx context.Context, op string) {
		f.mu.Lock()
		f.gotOrg[op] = cloud.Who(ctx).Org
		f.hitPaths = append(f.hitPaths, op)
		f.mu.Unlock()
	}
	p := cloud.Plane()
	zip.Post[plane.SpendIn, plane.Spend](p, "/finance/spend",
		func(ctx context.Context, _ *plane.SpendIn) (*plane.Spend, error) {
			record(ctx, plane.FinanceSpend)
			return &plane.Spend{
				Consumed: plane.Money{Decimal: "50.00", Currency: "USD"},
				Balance:  plane.Money{Decimal: "200.00", Currency: "USD"},
			}, nil
		}, zip.WithOperationID(plane.FinanceSpend))
	zip.Post[plane.TxnsIn, plane.Txns](p, "/finance/txns",
		func(ctx context.Context, _ *plane.TxnsIn) (*plane.Txns, error) {
			record(ctx, plane.FinanceTxns)
			return &plane.Txns{Rows: []plane.Txn{
				{ID: "t1", Kind: string(finance.KindUsage), Ref: "gpu-h100", Amount: plane.Money{Decimal: "3.00", Currency: "USD"}, CreatedAt: recent},
				{ID: "t2", Kind: string(finance.KindUsage), Ref: "llm", Amount: plane.Money{Decimal: "2.00", Currency: "USD"}, CreatedAt: recent},
				{ID: "t3", Kind: string(finance.KindDeposit), Ref: "", Amount: plane.Money{Decimal: "99.99", Currency: "USD"}, CreatedAt: recent},
			}}, nil
		}, zip.WithOperationID(plane.FinanceTxns))

	sock := zip.SocketPath("commerce")
	go func() { _ = p.Listen(sock) }()
	t.Cleanup(func() { _ = p.Shutdown() })
	for range 300 {
		if c, derr := net.Dial("unix", sock); derr == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the commerce plane socket never began listening")
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing — and a test that skips it does not
// test a stricter program, it tests a program where every org-scoped op refuses
// for a reason that would never exist in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// call drives a request. A non-empty user injects a VALIDATED principal (X-User-Id,
// which the gateway sets ONLY from a verified credential) with org as X-Org-Id.
func call(t *testing.T, app *zip.App, path, user, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decodeSummary(t *testing.T, b []byte) usageSummary {
	t.Helper()
	var s usageSummary
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode summary: %v (%s)", err, b)
	}
	return s
}

func TestSummary_ScopedToCallerOrg_RollsUpCommerce(t *testing.T) {
	f := &fakeCommerce{}
	f.serve(t)
	app := mountApp(t)

	code, body := call(t, app, "/v1/usage/summary?range=30d", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("summary: want 200, got %d (%s)", code, body)
	}
	s := decodeSummary(t, body)

	if s.Scope.Org != "maxpower" {
		t.Fatalf("scope org: want maxpower, got %q", s.Scope.Org)
	}
	// Both reads acted for the caller's OWN org. The org rides the CALL now, so
	// there is no header a reader sets and no query parameter a client could aim
	// somewhere else — the callee reads the tenant off the caller and refuses an
	// empty one.
	if f.gotOrg[plane.FinanceSpend] != "maxpower" || f.gotOrg[plane.FinanceTxns] != "maxpower" {
		t.Fatalf("the ledger reads were not scoped to the caller: spend=%q txns=%q",
			f.gotOrg[plane.FinanceSpend], f.gotOrg[plane.FinanceTxns])
	}
	// Spend rolled up: rollup figures + windowed withdrawals (300+200=500; deposit excluded).
	if !s.Spend.Available || !s.Sources.Commerce {
		t.Fatalf("spend must be available when commerce answered: %+v", s.Sources)
	}
	if s.Spend.MTDCents != 5000 || s.Spend.AvailableCents != 20000 {
		t.Fatalf("ledger figures not carried: mtd=%d avail=%d", s.Spend.MTDCents, s.Spend.AvailableCents)
	}
	if s.Spend.TotalCents != 500 {
		t.Fatalf("windowed spend: want 500, got %d", s.Spend.TotalCents)
	}
	if len(s.Spend.ByCategory) != 2 {
		t.Fatalf("byCategory: want 2 (GPU,LLM), got %d: %+v", len(s.Spend.ByCategory), s.Spend.ByCategory)
	}
	if s.Spend.ByCategory[0].Category != "GPU" || s.Spend.ByCategory[0].Cents != 300 {
		t.Fatalf("byCategory[0]: want GPU/300, got %+v", s.Spend.ByCategory[0])
	}
	// No datastore in the test env → LLM honest-empty, warehouse source false.
	if s.LLM.Available || s.Sources.Warehouse {
		t.Fatalf("llm must be honest-empty without a datastore: available=%v warehouse=%v", s.LLM.Available, s.Sources.Warehouse)
	}
}

func TestSummary_NoValidatedPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	f := &fakeCommerce{}
	f.serve(t)
	app := mountApp(t)
	// Forged X-Org-Id with NO validated principal (no X-User-Id) → 401, commerce untouched.
	code, _ := call(t, app, "/v1/usage/summary", "", "victim")
	if code != http.StatusUnauthorized {
		t.Fatalf("no principal: want 401, got %d", code)
	}
	if len(f.hitPaths) != 0 {
		t.Fatalf("commerce must NOT be reached on the unauthenticated path, saw %v", f.hitPaths)
	}
}

func TestSummary_ClientCannotWidenScope(t *testing.T) {
	f := &fakeCommerce{}
	f.serve(t)
	app := mountApp(t)
	// A forged ?user=victim and ?org=other must reach the ledger as neither. The
	// tenant is not an argument at all now: it rides the caller, which is the
	// gateway's assertion, so widening the scope is not a thing this request can
	// express.
	q := url.Values{"user": {"victim"}, "org": {"other"}}.Encode()
	code, _ := call(t, app, "/v1/usage/summary?"+q, "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if f.gotOrg[plane.FinanceSpend] != "maxpower" || f.gotOrg[plane.FinanceTxns] != "maxpower" {
		t.Fatalf("a forged user/org reached the ledger: spend=%q txns=%q",
			f.gotOrg[plane.FinanceSpend], f.gotOrg[plane.FinanceTxns])
	}
}

func TestSummary_NoCommerceInTheFleet_HonestZeros(t *testing.T) {
	// No commerce peer at all: the summary degrades to honest zeros (200), NOT a
	// 501 — a partial deploy still renders the screen; the source marker says
	// "not connected". There is no "unconfigured" shape any more, because there is
	// nothing to configure: this is a fleet that runs no commerce.
	dir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	app := mountApp(t)
	code, body := call(t, app, "/v1/usage/summary", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("unconfigured: want 200 honest zeros, got %d (%s)", code, body)
	}
	s := decodeSummary(t, body)
	if s.Spend.Available || s.Sources.Commerce {
		t.Fatalf("spend must be honest-empty when no commerce runs here: %+v", s.Sources)
	}
	if s.Spend.TotalCents != 0 || s.Spend.MTDCents != 0 {
		t.Fatalf("spend with no ledger must be zero, got total=%d mtd=%d", s.Spend.TotalCents, s.Spend.MTDCents)
	}
	if s.Spend.ByCategory == nil || s.Spend.Series == nil {
		t.Fatal("slices must serialize as [] (non-nil) even when unconfigured")
	}
}

func TestSummary_BadRange_400(t *testing.T) {
	app := mountApp(t)
	code, _ := call(t, app, "/v1/usage/summary?range=bogus", "maxpower/dave", "maxpower")
	// ResolveCloudUsageWindow rejects an unknown range enum with a 400.
	if code != http.StatusBadRequest {
		t.Fatalf("bad range: want 400, got %d", code)
	}
}
