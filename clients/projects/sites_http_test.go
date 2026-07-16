package projects

// End-to-end tests for the /v1/sites surface, driven over HTTP through the REAL
// Mount + zip stack against an in-memory S3 double (fakeS3) and an in-memory
// commerce double (billServer). They prove the security + billing contract of the
// deploy_site capability: org 403, hosting 402/503 fail-closed BEFORE any work,
// meter-once-on-success / zero-on-failure, idempotent redeploy with tenant-safe
// host binding, and the raw file-manifest path. SanitizeIdentity is not wired in
// this harness, so a validated principal is simulated by setting the identity
// headers the gateway would mint (X-Org-Id + X-User-Id), exactly like the s3 and
// agents billing tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// billServer is a minimal commerce double: a fixed balance + records the X-Org-Id
// header (the debited tenant) and body of every usage debit. Mirrors the header
// commerce's service-token auth reads (metering v0.1.2), so a wrong tenant here
// would prove a cross-tenant leak.
type billServer struct {
	available int64

	mu        sync.Mutex
	usageOrg  string
	usageBody []byte
	usages    int32
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.usages, 1)
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.usageOrg, b.usageBody = r.Header.Get("X-Org-Id"), body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32 { return atomic.LoadInt32(&b.usages) }
func (b *billServer) lastDebit() (string, []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageOrg, b.usageBody
}

func waitForDebit(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// mountSites mounts the projects surface with the given AI, a metering client
// pointed at commerceURL (default org "hanzo", so a debit landing on the CALLER
// proves the per-call override), and whatever S3 env the caller already set.
func mountSites(t *testing.T, ai *fakeAI, commerceURL string) *zip.App {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-tok", Org: "hanzo", Timeout: time.Second})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	var aic cloud.AIClient
	if ai != nil {
		aic = ai
	}
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AI: aic, Metering: m, Env: "mainnet"}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// doSite issues a request with a simulated validated principal (X-Org-Id +
// X-User-Id). An empty org sends neither header (the anonymous 403 path).
func doSite(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// okManifest is a valid model manifest (model omits the viewport tag on purpose).
func okManifest() string {
	return manifest("Acme Landing", map[string]string{
		"index.html": htmlNoViewport,
		"style.css":  "body{margin:0;max-width:100%}",
	})
}

// TestSites_NoPrincipal_403: POST /v1/sites with no validated principal (no
// X-User-Id) is refused 403 — the tenant boundary precedes generation, S3, and
// billing. Nothing is generated, uploaded, or debited.
func TestSites_NoPrincipal_403(t *testing.T) {
	f := startFakeS3(t)
	bs := &billServer{available: 100000}
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, bs.start(t))

	code, _ := doSite(t, app, http.MethodPost, "/v1/sites", "", map[string]any{"brief": "hi"})
	if code != http.StatusForbidden {
		t.Fatalf("no-principal want 403, got %d", code)
	}
	if ai.calls != 0 {
		t.Fatalf("no generation may occur, AI called %d times", ai.calls)
	}
	if f.puts != 0 {
		t.Fatalf("no upload may occur, %d puts", f.puts)
	}
	if bs.debits() != 0 {
		t.Fatalf("no debit for a refused request, got %d", bs.debits())
	}
}

// TestSites_UnfundedGate_402: an unfunded org is refused 402 BEFORE any inference
// or upload, and nothing is debited — the fail-closed hosting gate.
func TestSites_UnfundedGate_402(t *testing.T) {
	f := startFakeS3(t)
	bs := &billServer{available: 0}
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, bs.start(t))

	code, body := doSite(t, app, http.MethodPost, "/v1/sites", "acme", map[string]any{"brief": "a shop"})
	if code != http.StatusPaymentRequired {
		t.Fatalf("unfunded want 402, got %d (%s)", code, body)
	}
	if ai.calls != 0 {
		t.Fatalf("gate must precede generation, AI called %d times", ai.calls)
	}
	if f.puts != 0 {
		t.Fatalf("gate must precede upload, %d puts", f.puts)
	}
	if bs.debits() != 0 {
		t.Fatalf("a gate-refused deploy must not debit, got %d", bs.debits())
	}
}

// TestSites_FailClosedCommerce_503: when commerce is unreachable the gate denies
// (fail-closed) with 503 and no work runs.
func TestSites_FailClosedCommerce_503(t *testing.T) {
	f := startFakeS3(t)
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, "http://127.0.0.1:1") // dead commerce

	code, body := doSite(t, app, http.MethodPost, "/v1/sites", "acme", map[string]any{"brief": "x"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable commerce want 503, got %d (%s)", code, body)
	}
	if ai.calls != 0 || f.puts != 0 {
		t.Fatalf("no work on a fail-closed gate: aiCalls=%d puts=%d", ai.calls, f.puts)
	}
}

// TestSites_AINotConfigured_503: buildSite with no inference client is an honest
// 503 (not a silent success), and never touches billing or S3.
func TestSites_AINotConfigured_503(t *testing.T) {
	f := startFakeS3(t)
	bs := &billServer{available: 100000}
	app := mountSites(t, nil, bs.start(t)) // nil AI

	code, _ := doSite(t, app, http.MethodPost, "/v1/sites", "acme", map[string]any{"brief": "x"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("nil AI want 503, got %d", code)
	}
	if f.puts != 0 || bs.debits() != 0 {
		t.Fatalf("no work when AI unconfigured: puts=%d debits=%d", f.puts, bs.debits())
	}
}

// TestBuildSite_MetersOnce: a funded build succeeds (200), returns the pretty
// <slug>.hanzo.app URL, actually writes the site to S3, and debits the CALLER org
// EXACTLY once with product=hosting, unit=deploy, amount=fee.
func TestBuildSite_MetersOnce(t *testing.T) {
	f := startFakeS3(t)
	bs := &billServer{available: 100000}
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, bs.start(t))

	code, body := doSite(t, app, http.MethodPost, "/v1/sites", "acme",
		map[string]any{"brief": "a landing page", "slug": "shop"})
	if code != http.StatusOK {
		t.Fatalf("funded build want 200, got %d (%s)", code, body)
	}
	var resp struct {
		URL, Slug, Name, DeploymentID, Status string
		Files                                 []string
	}
	mustJSON(t, body, &resp)
	if resp.URL != "https://shop.hanzo.app" {
		t.Fatalf("url=%q want https://shop.hanzo.app", resp.URL)
	}
	if resp.Slug != "shop" || resp.Status != "live" || resp.DeploymentID == "" {
		t.Fatalf("bad response: %+v", resp)
	}
	if !contains(resp.Files, "index.html") {
		t.Fatalf("response files must include index.html: %v", resp.Files)
	}
	if ai.calls != 1 {
		t.Fatalf("exactly one generation expected, got %d", ai.calls)
	}
	if f.count("hanzo-sites/acme/shop/") == 0 {
		t.Fatal("site was not written to S3")
	}
	// The responsive guarantee survives the round-trip to storage.
	f.mu.Lock()
	stored := string(f.objects["hanzo-sites/acme/shop/index.html"])
	f.mu.Unlock()
	if !bytesContains(stored, viewportMeta) {
		t.Fatal("stored index.html is missing the viewport meta (responsive guarantee)")
	}

	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("a successful deploy must debit exactly once, got %d", bs.debits())
	}
	org, ubody := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller acme (never default hanzo)", org)
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(ubody, &u)
	if u.User != "acme" || u.Amount != cloud.DefaultResourceFeeCents || u.Provider != hostingProvider || u.Model != deployKind {
		t.Fatalf("debit = %+v, want user=acme amount=%d provider=%s model=%s", u, cloud.DefaultResourceFeeCents, hostingProvider, deployKind)
	}
}

// TestFailedDeploy_NotBilled: when the S3 upload fails, the deploy returns 502 and
// NOTHING is debited — failed work is never charged.
func TestFailedDeploy_NotBilled(t *testing.T) {
	// S3 is reachable (so blob.configured() is true and the gate is passed and
	// generation runs) but every object PUT is rejected, so the UPLOAD is where the
	// deploy breaks — the one place we must NOT bill.
	f := startFakeS3(t)
	f.failPut.Store(true)
	bs := &billServer{available: 100000}
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, bs.start(t))

	code, body := doSite(t, app, http.MethodPost, "/v1/sites", "acme", map[string]any{"brief": "x", "slug": "brokensite"})
	if code != http.StatusBadGateway {
		t.Fatalf("failed upload want 502, got %d (%s)", code, body)
	}
	if ai.calls != 1 {
		t.Fatalf("generation should have run once, got %d", ai.calls)
	}
	if waitForDebit(func() bool { return bs.debits() > 0 }) {
		t.Fatalf("a failed deploy must NOT be billed, got %d debits", bs.debits())
	}
}

// TestIdempotentRedeploy: deploying the same slug twice returns the SAME
// <slug>.hanzo.app URL and bills each deploy (distinct billable events), and a
// SECOND org cannot hijack the host — the binding stays with the first owner.
func TestIdempotentRedeploy(t *testing.T) {
	startFakeS3(t)
	bs := &billServer{available: 1000000}
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, bs.start(t))

	first := deployVia(t, app, "acme", "myapp")
	second := deployVia(t, app, "acme", "myapp") // redeploy, same owner+slug
	if first != "https://myapp.hanzo.app" || second != first {
		t.Fatalf("redeploy URL not stable: first=%q second=%q", first, second)
	}
	// Two deploys = two billable events.
	if !waitForDebit(func() bool { return bs.debits() == 2 }) {
		t.Fatalf("two deploys must bill twice, got %d", bs.debits())
	}
	// The host binding is owned by acme.
	if p, err := mounted.State.store.ResolveHost(context.Background(), "myapp"); err != nil || p.Org != "acme" {
		t.Fatalf("host must resolve to acme, got org=%q err=%v", p.Org, err)
	}

	// A different org deploying the SAME slug does not hijack the host: its deploy
	// still succeeds (serving its own S3 prefix) but "myapp" keeps resolving to acme.
	evil := deployVia(t, app, "evil", "myapp")
	if evil != "https://myapp.hanzo.app" {
		t.Fatalf("evil deploy url=%q", evil)
	}
	if p, err := mounted.State.store.ResolveHost(context.Background(), "myapp"); err != nil || p.Org != "acme" {
		t.Fatalf("host hijack: myapp resolves to %q, must stay acme", p.Org)
	}
}

// TestDeploySiteFiles_JSONPath: POST /v1/sites/deploy with a raw file manifest
// deploys and returns the pretty URL, and injects the viewport guarantee.
func TestDeploySiteFiles_JSONPath(t *testing.T) {
	f := startFakeS3(t)
	bs := &billServer{available: 100000}
	app := mountSites(t, &fakeAI{}, bs.start(t))

	code, body := doSite(t, app, http.MethodPost, "/v1/sites/deploy", "acme", map[string]any{
		"slug": "raw",
		"name": "Raw Site",
		"files": []map[string]string{
			{"path": "index.html", "content": htmlNoViewport},
			{"path": "app.js", "content": "console.log(1)"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("deploy_site want 200, got %d (%s)", code, body)
	}
	var resp struct{ URL, Slug, Status string }
	mustJSON(t, body, &resp)
	if resp.URL != "https://raw.hanzo.app" || resp.Slug != "raw" || resp.Status != "live" {
		t.Fatalf("bad deploy_site response: %+v", resp)
	}
	f.mu.Lock()
	stored := string(f.objects["hanzo-sites/acme/raw/index.html"])
	f.mu.Unlock()
	if !bytesContains(stored, viewportMeta) {
		t.Fatal("deploy_site must inject the viewport guarantee")
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("deploy_site must bill once, got %d", bs.debits())
	}
}

// TestListSites: only LIVE sites are listed, at their pretty URLs, and only for
// the caller's org (tenant isolation).
func TestListSites(t *testing.T) {
	startFakeS3(t)
	bs := &billServer{available: 1000000}
	ai := &fakeAI{content: okManifest()}
	app := mountSites(t, ai, bs.start(t))

	deployVia(t, app, "acme", "one")
	deployVia(t, app, "other", "two") // different tenant

	code, body := doSite(t, app, http.MethodGet, "/v1/sites", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var sites []siteView
	mustJSON(t, body, &sites)
	if len(sites) != 1 {
		t.Fatalf("acme must see exactly its 1 live site, got %d: %+v", len(sites), sites)
	}
	if sites[0].Slug != "one" || sites[0].URL != "https://one.hanzo.app" || sites[0].Status != "live" {
		t.Fatalf("bad site view: %+v", sites[0])
	}

	// A draft project (created, never deployed) is excluded from the sites list.
	if err := mounted.State.store.CreateProject(context.Background(), Project{
		ID: "p_draft", Org: "acme", Slug: "draft", Name: "Draft", Framework: "static",
		Status: "draft", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	_, body2 := doSite(t, app, http.MethodGet, "/v1/sites", "acme", nil)
	var sites2 []siteView
	mustJSON(t, body2, &sites2)
	if len(sites2) != 1 {
		t.Fatalf("draft must be excluded; got %d sites", len(sites2))
	}
}

// capturingObserver records the last OnDeploy notification for assertions.
type capturingObserver struct {
	mu                           sync.Mutex
	org, slug, url, deploymentID string
	calls                        int
}

func (o *capturingObserver) OnDeploy(_ context.Context, org, slug, url, deploymentID string) {
	o.mu.Lock()
	o.org, o.slug, o.url, o.deploymentID, o.calls = org, slug, url, deploymentID, o.calls+1
	o.mu.Unlock()
}

// TestDeployObserver: the session-event seam fires exactly once per /v1/sites
// deploy with the canonical org/slug/url/deploymentId, and clearing it (nil) makes
// notifyDeploy a safe no-op.
func TestDeployObserver(t *testing.T) {
	startFakeS3(t)
	bs := &billServer{available: 1000000}
	app := mountSites(t, &fakeAI{content: okManifest()}, bs.start(t))

	obs := &capturingObserver{}
	SetDeployObserver(obs)
	t.Cleanup(func() { SetDeployObserver(nil) }) // don't leak into other tests

	url := deployVia(t, app, "acme", "watched")
	obs.mu.Lock()
	calls, gotOrg, gotSlug, gotURL, gotDep := obs.calls, obs.org, obs.slug, obs.url, obs.deploymentID
	obs.mu.Unlock()
	if calls != 1 || gotOrg != "acme" || gotSlug != "watched" || gotURL != url || gotDep == "" {
		t.Fatalf("observer got calls=%d org=%q slug=%q url=%q dep=%q; want one call for acme/watched at %q",
			calls, gotOrg, gotSlug, gotURL, gotDep, url)
	}

	// Cleared observer: a further deploy must not panic and must not notify.
	SetDeployObserver(nil)
	deployVia(t, app, "acme", "watched") // redeploy — no observer registered
	obs.mu.Lock()
	after := obs.calls
	obs.mu.Unlock()
	if after != 1 {
		t.Fatalf("observer must not fire after being cleared, calls=%d", after)
	}
}

// ---- small helpers ----

// deployVia deploys a generated site for org+slug and returns the live URL.
func deployVia(t *testing.T, app *zip.App, org, slug string) string {
	t.Helper()
	code, body := doSite(t, app, http.MethodPost, "/v1/sites", org, map[string]any{"brief": "b", "slug": slug})
	if code != http.StatusOK {
		t.Fatalf("deploy %s/%s want 200, got %d (%s)", org, slug, code, body)
	}
	var resp struct{ URL string }
	mustJSON(t, body, &resp)
	return resp.URL
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func bytesContains(hay, needle string) bool { return bytes.Contains([]byte(hay), []byte(needle)) }
