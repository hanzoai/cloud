package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce stands in for the commerce billing S2S surface. It records the
// Authorization header and the trusted X-Org-Id/`user` selectors (so a test can
// prove every read is pinned to the CALLER's own org) and answers a canned rollup +
// a recent-timestamped ledger so the category/series roll-up has data in-window.
type fakeCommerce struct {
	mu       sync.Mutex
	gotAuth  string
	gotOrg   map[string]string // path -> X-Org-Id seen
	gotUser  map[string]string // path -> ?user seen
	hitPaths []string
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.gotOrg = map[string]string{}
	f.gotUser = map[string]string{}
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		f.mu.Lock()
		f.gotAuth = r.Header.Get("Authorization")
		f.gotOrg[r.URL.Path] = r.Header.Get("X-Org-Id")
		f.gotUser[r.URL.Path] = r.URL.Query().Get("user")
		f.hitPaths = append(f.hitPaths, r.URL.Path)
		f.mu.Unlock()
	}
	mux.HandleFunc("/v1/billing/usage-rollup", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"consumedCents":5000,"overageCents":100,"balance":{"balanceCents":20000,"availableCents":15000}}`)
	})
	mux.HandleFunc("/v1/billing/transactions", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"transactions":[`+
			`{"id":"t1","type":"withdraw","amount":300,"tags":"gpu-h100","createdAt":%q},`+
			`{"id":"t2","type":"withdraw","amount":200,"tags":"llm","createdAt":%q},`+
			`{"id":"t3","type":"deposit","amount":9999,"tags":"","createdAt":%q}]}`, recent, recent, recent)
		_, _ = io.WriteString(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mountApp(t *testing.T, base, token string) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_COMMERCE_HTTP_URL", base)
	t.Setenv("COMMERCE_SERVICE_TOKEN", token)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}); err != nil {
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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decodeSummary(t *testing.T, b []byte) Summary {
	t.Helper()
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode summary: %v (%s)", err, b)
	}
	return s
}

func TestSummary_ScopedToCallerOrg_RollsUpCommerce(t *testing.T) {
	f := &fakeCommerce{}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, "/v1/usage/summary?range=30d", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("summary: want 200, got %d (%s)", code, body)
	}
	s := decodeSummary(t, body)

	if s.Scope.Org != "maxpower" {
		t.Fatalf("scope org: want maxpower, got %q", s.Scope.Org)
	}
	// commerce read scoped to the caller's OWN org on BOTH the S2S selector and subject.
	if f.gotOrg["/v1/billing/usage-rollup"] != "maxpower" || f.gotUser["/v1/billing/usage-rollup"] != "maxpower" {
		t.Fatalf("rollup not scoped to caller: org=%q user=%q", f.gotOrg["/v1/billing/usage-rollup"], f.gotUser["/v1/billing/usage-rollup"])
	}
	if f.gotAuth != "Bearer svc-token" {
		t.Fatalf("commerce auth: want service token, got %q", f.gotAuth)
	}
	// Spend rolled up: rollup figures + windowed withdrawals (300+200=500; deposit excluded).
	if !s.Spend.Available || !s.Sources.Commerce {
		t.Fatalf("spend must be available when commerce answered: %+v", s.Sources)
	}
	if s.Spend.MTDCents != 5000 || s.Spend.AvailableCents != 15000 {
		t.Fatalf("rollup figures not carried: mtd=%d avail=%d", s.Spend.MTDCents, s.Spend.AvailableCents)
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
	app := mountApp(t, f.server(t).URL, "svc-token")
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
	app := mountApp(t, f.server(t).URL, "svc-token")
	// A forged ?user=victim must be overwritten server-side with the caller's own org.
	q := url.Values{"user": {"victim"}, "org": {"other"}}.Encode()
	code, _ := call(t, app, "/v1/usage/summary?"+q, "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if f.gotUser["/v1/billing/usage-rollup"] != "maxpower" {
		t.Fatalf("forged user must be overwritten with caller org, got %q", f.gotUser["/v1/billing/usage-rollup"])
	}
	if f.gotOrg["/v1/billing/transactions"] != "maxpower" {
		t.Fatalf("commerce X-Org-Id must be caller org, got %q", f.gotOrg["/v1/billing/transactions"])
	}
}

func TestSummary_CommerceUnconfigured_HonestZeros(t *testing.T) {
	// No commerce base/token: the summary degrades to honest zeros (200), NOT a 501 —
	// a partial deploy still renders the screen; the source marker says "not connected".
	app := mountApp(t, "", "")
	code, body := call(t, app, "/v1/usage/summary", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("unconfigured: want 200 honest zeros, got %d (%s)", code, body)
	}
	s := decodeSummary(t, body)
	if s.Spend.Available || s.Sources.Commerce {
		t.Fatalf("spend must be honest-empty when commerce is unconfigured: %+v", s.Sources)
	}
	if s.Spend.TotalCents != 0 || s.Spend.MTDCents != 0 {
		t.Fatalf("unconfigured spend must be zero, got total=%d mtd=%d", s.Spend.TotalCents, s.Spend.MTDCents)
	}
	if s.Spend.ByCategory == nil || s.Spend.Series == nil {
		t.Fatal("slices must serialize as [] (non-nil) even when unconfigured")
	}
}

func TestSummary_BadRange_400(t *testing.T) {
	app := mountApp(t, "", "")
	code, _ := call(t, app, "/v1/usage/summary?range=bogus", "maxpower/dave", "maxpower")
	// ResolveCloudUsageWindow rejects an unknown range enum with a 400.
	if code != http.StatusBadRequest {
		t.Fatalf("bad range: want 400, got %d", code)
	}
}
