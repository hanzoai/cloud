package cloudflare

// asset_plane_test.go — tests for the surfaces added when the plane became
// first-class /v1/cloudflare/*: Zones + Analytics (read), Workers AI (metered
// inference), KV values, and D1 query — plus the SSRF guard on the Workers AI model
// and the org-admin gate on every new mutation. It reuses the fakeCF stub, capture,
// and doReq/do helpers from cloudflare_test.go.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
)

// ── Workers AI model validation (the SSRF guard) ────────────────────────────────

// aiModel is the single choke point that folds a caller-supplied model into the
// upstream /accounts/{id}/ai/run/{model} URL. It must accept real CF model ids and
// reject anything that could smuggle path structure, a scheme, or a host.
func TestAIModelValidation(t *testing.T) {
	ok := []string{
		"@cf/meta/llama-3.1-8b-instruct",
		"@hf/thebloke/mistral-7b",
		"@cf/baai/bge-base-en-v1.5",
		"custom-model_1",
	}
	for _, m := range ok {
		if got, err := aiModel(m); err != nil || got != m {
			t.Fatalf("aiModel(%q) = %q,%v; want it accepted unchanged", m, got, err)
		}
	}
	bad := []string{
		"", " ", "..", ".",
		"../etc/passwd",
		"@cf/../secret",
		"foo/./bar",
		"a//b",
		"/leading",
		"trailing/",
		"a%2fb",                  // percent-encoding is not in the charset
		"http://evil.com/x",      // scheme via ':' + '//'
		"model?x=1",              // query
		"model#frag",             // fragment
		"a b",                    // space
		"héllo",                  // non-ascii
		strings.Repeat("a", 200), // over length
	}
	for _, m := range bad {
		if _, err := aiModel(m); err == nil {
			t.Fatalf("aiModel(%q) accepted a hostile/invalid model", m)
		}
	}
}

// A hostile model on the WIRE never reaches Cloudflare: model validation runs before
// account resolution, so there is not even a discovery round-trip.
func TestWorkersAIHostileModelNoUpstream(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, path := range []string{
		"/v1/cloudflare/ai/run/", // empty model
		"/v1/cloudflare/ai/run/..%2f..%2fadmin",
		"/v1/cloudflare/ai/run/@cf%2f..%2fsecret",
	} {
		rec.reqs = nil
		status, _, _ := doReq(t, app, http.MethodPost, path, "ua", "orga", false, `{"prompt":"x"}`)
		if status == http.StatusOK {
			t.Fatalf("%s: hostile model returned 200", path)
		}
		for _, r := range rec.reqs {
			if strings.Contains(r.path, "..") || strings.Contains(r.path, "/admin") || strings.Contains(r.path, "/secret") {
				t.Fatalf("%s: a traversed path reached Cloudflare: %q", path, r.path)
			}
		}
	}
}

// ── Zones + Analytics ───────────────────────────────────────────────────────────

// Zones list/get/analytics are READS: they relay the correct upstream path with the
// caller-org token, forward allowlisted query params, and gate on a validated org.
func TestZonesAndAnalytics(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	zone := "0123456789abcdef0123456789abcdef"

	// list, with pagination forwarded
	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/zones?page=2&per_page=5&name=acme.com", "ua", "orga", ""); status != 200 {
		t.Fatalf("zones list status=%d", status)
	}
	r, ok := rec.find("/zones")
	if !ok || r.auth != "Bearer tok-A" {
		t.Fatalf("zones list: reached=%v token=%q", ok, r.auth)
	}
	if !strings.Contains(r.query, "per_page=5") || !strings.Contains(r.query, "page=2") {
		t.Fatalf("zones list did not forward pagination; query=%q", r.query)
	}

	// analytics addresses the zone dashboard and forwards the window
	rec.reqs = nil
	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/zones/"+zone+"/analytics?since=-1440&until=0", "ua", "orga", ""); status != 200 {
		t.Fatalf("zone analytics status=%d", status)
	}
	ra, ok := rec.find("/zones/" + zone + "/analytics/dashboard")
	if !ok {
		t.Fatalf("zone analytics did not address the dashboard path; got %+v", rec.reqs)
	}
	if !strings.Contains(ra.query, "since=-1440") {
		t.Fatalf("zone analytics did not forward the window; query=%q", ra.query)
	}

	// read gate: unvalidated principal is refused before any CF contact
	rec.reqs = nil
	if s, _ := do(t, app, http.MethodGet, "/v1/cloudflare/zones", "", "orga", ""); s != http.StatusForbidden {
		t.Fatalf("unvalidated zones list status=%d, want 403", s)
	}
	if len(rec.reqs) != 0 {
		t.Fatalf("unvalidated zones list contacted CF %d time(s)", len(rec.reqs))
	}
}

// An invalid zone id (not 32-hex) is refused 400 and never folded into a CF path.
func TestZoneIDValidated(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	if s, _ := do(t, app, http.MethodGet, "/v1/cloudflare/zones/..%2f..%2fadmin/analytics", "ua", "orga", ""); s == http.StatusOK {
		t.Fatal("hostile zone id returned 200")
	}
	for _, r := range rec.reqs {
		if strings.Contains(r.path, "/admin") || strings.Contains(r.path, "..") {
			t.Fatalf("hostile zone id reached CF: %q", r.path)
		}
	}
}

// ── KV values ───────────────────────────────────────────────────────────────────

// A KV value GET relays the raw stored bytes (not the CF envelope) with its content
// type; a value PUT forwards the request body verbatim as the value. Both address the
// correct namespace/value upstream path with the caller-org token.
func TestKVValueGetPutRaw(t *testing.T) {
	rec := &capture{}
	ns := "0123456789abcdef0123456789abcdef"
	// The GET value is a RAW stored value (not an envelope); the PUT gets the default
	// success envelope. Distinct keys keep the two response shapes apart.
	resultFor := func(path string) (int, string) {
		if strings.Contains(path, "/values/getkey") {
			return 200, "RAW-VALUE-BYTES" // getRaw must relay this verbatim
		}
		return 0, ""
	}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, resultFor)

	// GET raw
	status, body := do(t, app, http.MethodGet, "/v1/cloudflare/kv/namespaces/"+ns+"/values/getkey", "ua", "orga", "")
	if status != 200 || body != "RAW-VALUE-BYTES" {
		t.Fatalf("kv value get status=%d body=%q; want 200 + raw value", status, body)
	}
	r, ok := rec.find("/storage/kv/namespaces/" + ns + "/values/getkey")
	if !ok || r.auth != "Bearer tok-A" {
		t.Fatalf("kv value get: reached=%v token=%q", ok, r.auth)
	}

	// PUT forwards the body as the value (mutation → org admin)
	rec.reqs = nil
	status, resp, _ := doReq(t, app, http.MethodPut, "/v1/cloudflare/kv/namespaces/"+ns+"/values/putkey", "ua", "orga", true, "hello-value")
	if status != 200 {
		t.Fatalf("kv value put status=%d resp=%s", status, resp)
	}
	rp, ok := rec.find("/values/putkey")
	if !ok || string(rp.body) != "hello-value" {
		t.Fatalf("kv value put did not forward the raw body; got %q", string(rp.body))
	}
	if rp.ctype != "application/json" {
		t.Fatalf("kv value put content-type=%q, want the caller's type forwarded", rp.ctype)
	}
}

// A dot-only KV key ('.'/'..') is refused — url.PathEscape leaves dots intact, so it
// could otherwise walk up to the namespace endpoint.
func TestKVKeyDotRejected(t *testing.T) {
	rec := &capture{}
	ns := "0123456789abcdef0123456789abcdef"
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, key := range []string{"..", "."} {
		rec.reqs = nil
		status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/kv/namespaces/"+ns+"/values/"+key, "ua", "orga", "")
		if status == http.StatusOK {
			t.Fatalf("dot key %q returned 200", key)
		}
		for _, r := range rec.reqs {
			// must not have addressed the bare namespace (a walk-up)
			if strings.HasSuffix(r.path, "/namespaces/"+ns) {
				t.Fatalf("dot key %q walked up to the namespace endpoint: %q", key, r.path)
			}
		}
	}
}

// ── D1 query ────────────────────────────────────────────────────────────────────

// A D1 query addresses the singular upstream database path, requires a non-empty sql,
// forwards the body verbatim (params preserved), and is a WRITE (org admin).
func TestD1Query(t *testing.T) {
	rec := &capture{}
	db := "d1abcdef-0123-4567-89ab-cdef01234567"
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)

	// missing sql → 400, no CF contact
	if status, _, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/d1/databases/"+db+"/query", "ua", "orga", true, `{"params":[]}`); status != http.StatusBadRequest {
		t.Fatalf("d1 query without sql status=%d, want 400", status)
	}

	rec.reqs = nil
	body := `{"sql":"SELECT * FROM t WHERE id=?","params":["x"]}`
	if status, resp, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/d1/databases/"+db+"/query", "ua", "orga", true, body); status != 200 {
		t.Fatalf("d1 query status=%d resp=%s", status, resp)
	}
	r, ok := rec.find("/d1/database/" + db + "/query")
	if !ok {
		t.Fatalf("d1 query did not address the singular database path; got %+v", rec.reqs)
	}
	if r.auth != "Bearer tok-A" || string(r.body) != body {
		t.Fatalf("d1 query token=%q body=%q (want verbatim forward)", r.auth, string(r.body))
	}
}

// ── org-admin gate on every new mutation ────────────────────────────────────────

// R2/KV/D1 creates, a KV value write, and a D1 query are mutations: a non-admin org
// member is refused 403 BEFORE any Cloudflare contact.
func TestNewMutationsRequireOrgAdmin(t *testing.T) {
	rec := &capture{}
	ns := "0123456789abcdef0123456789abcdef"
	db := "d1abcdef-0123-4567-89ab-cdef01234567"
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/cloudflare/r2/buckets", `{"name":"b"}`},
		{http.MethodPost, "/v1/cloudflare/kv/namespaces", `{"title":"t"}`},
		{http.MethodPost, "/v1/cloudflare/d1/databases", `{"name":"d"}`},
		{http.MethodPut, "/v1/cloudflare/kv/namespaces/" + ns + "/values/k", "v"},
		{http.MethodPost, "/v1/cloudflare/d1/databases/" + db + "/query", `{"sql":"SELECT 1"}`},
	}
	for _, tc := range cases {
		rec.reqs = nil
		status, _, _ := doReq(t, app, tc.method, tc.path, "ua", "orga", false /* NOT admin */, tc.body)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s: non-admin status=%d, want 403", tc.method, tc.path, status)
		}
		if len(rec.reqs) != 0 {
			t.Fatalf("%s %s: non-admin mutation contacted CF %d time(s)", tc.method, tc.path, len(rec.reqs))
		}
	}
}

// ── Workers AI meters through the ONE unified spine ─────────────────────────────

// billStub is a minimal commerce double: a balance for the pre-call gate and a capture
// of the usage debit body, so a test can prove Workers AI gates AND debits the SAME
// "ai" spine. The balance is positive by default; set broke to model a frozen / broke
// org the gate must refuse.
type billStub struct {
	broke  bool // when true the gate sees a 0 balance
	mu     sync.Mutex
	usages int32
	body   []byte
}

func (b *billStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	avail := int64(100000)
	if b.broke {
		avail = 0
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, fmt.Sprintf(`{"available":%d}`, avail))
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.usages, 1)
		b.mu.Lock()
		b.body, _ = io.ReadAll(r.Body)
		b.mu.Unlock()
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (b *billStub) usageBody() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.body)
}

// meteredHarness is harness + an injected commerce client, so the "ai"-provider meter
// this subsystem builds is actually enabled and its debit is observable.
func meteredHarness(t *testing.T, tokens map[string]string, rec *capture, resultFor func(string) (int, string), m *metering.Client) *zip.App {
	t.Helper()
	srv := httptest.NewServer(fakeCF(rec, resultFor))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUDFLARE_API_BASE", srv.URL)

	prev := tokenFor
	tokenFor = func(_ context.Context, org, provider, name string) ([]byte, error) {
		if provider != providerCloudflare || name != secretAPIToken {
			return nil, fmt.Errorf("unexpected coordinate %s/%s", provider, name)
		}
		tok, ok := tokens[org]
		if !ok {
			return nil, fmt.Errorf("not connected for %q", org)
		}
		return []byte(tok), nil
	}
	t.Cleanup(func() { tokenFor = prev })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Metering: m}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func TestWorkersAIMetersUnifiedSpine(t *testing.T) {
	t.Setenv("CLOUD_AI_PRICE_UUSD_PER_1K", "2000") // deterministic rate; BYO fee = 100 bps of it
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "0")       // isolate the token-proportional debit (floor tested separately)

	bill := &billStub{}
	m, err := metering.New(metering.Config{BaseURL: bill.server(t).URL, Token: "svc", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	rec := &capture{}
	resultFor := func(path string) (int, string) {
		if strings.Contains(path, "/ai/run/") {
			return 200, `{"success":true,"errors":[],"result":{"response":"hi","usage":{"prompt_tokens":100,"completion_tokens":900,"total_tokens":1000}}}`
		}
		return 0, ""
	}
	app := meteredHarness(t, map[string]string{"orga": "tok-A"}, rec, resultFor, m)

	model := "@cf/meta/llama-3-8b-instruct"
	reqBody := `{"prompt":"` + strings.Repeat("a", 400) + `"}` // ~100 est tokens → gate exercised
	status, resp, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/ai/run/"+model, "ua", "orga", false, reqBody)
	if status != 200 {
		t.Fatalf("ai run status=%d resp=%s", status, resp)
	}

	// Upstream: the run addressed /accounts/{acct}/ai/run/{model} with the caller token.
	r, ok := rec.find("/ai/run/" + model)
	if !ok {
		t.Fatalf("ai run did not reach the model path; got %+v", rec.reqs)
	}
	if want := "/accounts/" + testAccountID + "/ai/run/" + model; r.path != want {
		t.Fatalf("ai run addressed %q, want %q", r.path, want)
	}
	if r.auth != "Bearer tok-A" {
		t.Fatalf("ai run used %q, want Bearer tok-A", r.auth)
	}

	// Debit fired on the UNIFIED spine (async fire-and-forget). total=1000 tokens,
	// rate=2000 uUSD/1k, BYO 100 bps → fee = 1000*2000/1000*100/10000 = 20 micro-USD.
	waitFor(t, "usage debit", func() bool { return atomic.LoadInt32(&bill.usages) > 0 })
	ub := bill.usageBody()
	for _, want := range []string{`"provider":"ai"`, `"service":"ai"`, `"model":"` + model + `"`, `"amountMicros":20`, `"user":"orga"`} {
		if !strings.Contains(ub, want) {
			t.Fatalf("usage debit missing %s; body=%s", want, ub)
		}
	}
}

// F-1: a non-text Workers AI model (whisper audio) yields a 0 token estimate, but the
// FLOORED fee forces the balance gate to run — so a broke/frozen org is REFUSED (402)
// and its inference is never proxied to Cloudflare (no discovery, no run, no debit).
func TestWorkersAIBrokeOrgRefusedOnNonText(t *testing.T) {
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "100") // floor on ⇒ gate always runs

	bill := &billStub{broke: true} // 0 balance
	m, err := metering.New(metering.Config{BaseURL: bill.server(t).URL, Token: "svc", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	rec := &capture{}
	app := meteredHarness(t, map[string]string{"orga": "tok-A"}, rec, nil, m)

	// A non-text body (audio bytes) — no prompt/messages/text ⇒ estTokens == 0.
	status, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/ai/run/@cf/openai/whisper", "ua", "orga", false, `{"audio":[1,2,3,4]}`)
	if status != http.StatusPaymentRequired {
		t.Fatalf("broke org on a non-text model: status=%d, want 402; body=%s", status, body)
	}
	if len(rec.reqs) != 0 {
		t.Fatalf("a refused inference was still proxied to Cloudflare (%d calls): %+v", len(rec.reqs), rec.reqs)
	}
	if n := atomic.LoadInt32(&bill.usages); n != 0 {
		t.Fatalf("a refused inference recorded %d debit(s); want 0", n)
	}
}

// F-1 (billing half): a funded org's text run whose model reports NO usage still leaves
// a debit row ≥ the floor — never a silent, unbilled proxied call.
func TestWorkersAINoUsageBillsFloor(t *testing.T) {
	t.Setenv("CLOUD_AI_PRICE_UUSD_PER_1K", "2000")
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "100")

	bill := &billStub{}
	m, err := metering.New(metering.Config{BaseURL: bill.server(t).URL, Token: "svc", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	rec := &capture{}
	// The model returns a result with NO usage field (as classification/image models do).
	resultFor := func(path string) (int, string) {
		if strings.Contains(path, "/ai/run/") {
			return 200, `{"success":true,"errors":[],"result":{"label":"cat"}}`
		}
		return 0, ""
	}
	app := meteredHarness(t, map[string]string{"orga": "tok-A"}, rec, resultFor, m)

	status, resp, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/ai/run/@cf/microsoft/resnet-50", "ua", "orga", false, `{"image":[1,2,3]}`)
	if status != 200 {
		t.Fatalf("no-usage run status=%d resp=%s", status, resp)
	}
	waitFor(t, "floor debit", func() bool { return atomic.LoadInt32(&bill.usages) > 0 })
	if ub := bill.usageBody(); !strings.Contains(ub, `"amountMicros":100`) {
		t.Fatalf("a no-usage run must bill the floor (100); body=%s", ub)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
