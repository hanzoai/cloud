package dns

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// stubDNS is a fake DNS control plane: it records what the forward head sent and
// serves per-org zones keyed on the X-Org-Id it receives, so a test can assert both
// faithful passthrough AND that a caller only ever presents its OWN tenant.
type stubDNS struct {
	*httptest.Server
	mu         sync.Mutex
	hits       int
	method     string
	path       string
	query      string
	body       string
	auth       string
	orgHdr     string
	hdrs       http.Header
	zonesByOrg map[string]string // tenant-keyed store, served from X-Org-Id
	status     int               // response status (0 => 200)
}

func newStubDNS() *stubDNS {
	s := &stubDNS{zonesByOrg: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits++
		s.method, s.path, s.query = r.Method, r.URL.Path, r.URL.RawQuery
		s.body = string(body)
		s.auth = r.Header.Get("Authorization")
		s.orgHdr = r.Header.Get("X-Org-Id")
		s.hdrs = r.Header.Clone()
		status, zone := s.status, s.zonesByOrg[s.orgHdr]
		s.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		// Echo the tenant the plane resolved + its zone, so isolation is visible.
		_, _ = w.Write([]byte(`{"org":"` + s.orgHdr + `","zones":["` + zone + `"]}`))
	}))
	return s
}

// dnsApp mounts the forward head pointed at the stub (via HANZO_DNS_URL).
func dnsApp(t *testing.T, upstream string) *zip.App {
	t.Helper()
	t.Setenv("HANZO_DNS_URL", upstream)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// as stamps the identity headers SanitizeIdentity mints for a validated principal
// (X-User-Id + X-Org-Id) plus the caller's own bearer (what CallerBearer relays).
func as(req *http.Request, org, user, bearer string) *http.Request {
	req.Header.Set("X-User-Id", user)
	req.Header.Set("X-Org-Id", org)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func do(t *testing.T, app *zip.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res, string(b)
}

// A write forwards verb + path + body verbatim and relays the caller's OWN bearer
// and validated org; the upstream response passes back unchanged.
func TestForwardsVerbPathBodyAndRelaysIdentity(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	up.status = http.StatusCreated
	app := dnsApp(t, up.URL)

	req := as(httptest.NewRequest(http.MethodPost, "/v1/dns/zones", strings.NewReader(`{"zone":"a.com"}`)), "orgA", "orgA/dave", "tokenA")
	req.Header.Set("Content-Type", "application/json")
	res, body := do(t, app, req)

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (upstream status must pass through)", res.StatusCode)
	}
	if up.method != http.MethodPost || up.path != "/v1/dns/zones" {
		t.Fatalf("upstream saw %s %s, want POST /v1/dns/zones", up.method, up.path)
	}
	if up.body != `{"zone":"a.com"}` {
		t.Fatalf("upstream body = %q, want the request body verbatim", up.body)
	}
	if up.auth != "Bearer tokenA" {
		t.Fatalf("upstream Authorization = %q, want the caller's own bearer relayed unchanged", up.auth)
	}
	if up.orgHdr != "orgA" {
		t.Fatalf("upstream X-Org-Id = %q, want the validated org", up.orgHdr)
	}
	if !strings.Contains(body, `"org":"orgA"`) {
		t.Fatalf("response body = %q, want the upstream envelope verbatim", body)
	}
}

// A 4xx from the plane (status + body) passes back faithfully.
func TestStatusAndErrorBodyPassThrough(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	up.status = http.StatusConflict
	app := dnsApp(t, up.URL)

	res, body := do(t, app, as(httptest.NewRequest(http.MethodPost, "/v1/dns/zones", strings.NewReader(`{}`)), "orgA", "orgA/dave", "tokenA"))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (error status must pass through)", res.StatusCode)
	}
	if !strings.Contains(body, `"org":"orgA"`) {
		t.Fatalf("error body = %q, want the upstream body verbatim", body)
	}
}

// Nested path + query pass through verbatim (records under a zone).
func TestPathAndQueryPassthrough(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	app := dnsApp(t, up.URL)

	do(t, app, as(httptest.NewRequest(http.MethodGet, "/v1/dns/zones/example.com/records?type=A", nil), "orgA", "orgA/dave", "tokenA"))
	if up.path != "/v1/dns/zones/example.com/records" {
		t.Fatalf("upstream path = %q, want the full nested path", up.path)
	}
	if up.query != "type=A" {
		t.Fatalf("upstream query = %q, want type=A passed through", up.query)
	}
}

// TENANT ISOLATION: each caller reaches ONLY its own org's zones through the proxy,
// and always presents its OWN identity upstream -- org A can never reach org B.
func TestTenantIsolation_CallerReachesOnlyOwnOrg(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	up.zonesByOrg = map[string]string{"orgA": "a.com", "orgB": "b.com"}
	app := dnsApp(t, up.URL)

	_, aBody := do(t, app, as(httptest.NewRequest(http.MethodGet, "/v1/dns/zones", nil), "orgA", "orgA/dave", "tokenA"))
	if up.orgHdr != "orgA" || up.auth != "Bearer tokenA" {
		t.Fatalf("caller A presented org=%q auth=%q upstream, want orgA / Bearer tokenA", up.orgHdr, up.auth)
	}
	if !strings.Contains(aBody, "a.com") || strings.Contains(aBody, "b.com") {
		t.Fatalf("caller A saw %q, want ONLY a.com (org B's zones must be unreachable)", aBody)
	}

	_, bBody := do(t, app, as(httptest.NewRequest(http.MethodGet, "/v1/dns/zones", nil), "orgB", "orgB/eve", "tokenB"))
	if up.orgHdr != "orgB" || up.auth != "Bearer tokenB" {
		t.Fatalf("caller B presented org=%q auth=%q upstream, want orgB / Bearer tokenB", up.orgHdr, up.auth)
	}
	if !strings.Contains(bBody, "b.com") || strings.Contains(bBody, "a.com") {
		t.Fatalf("caller B saw %q, want ONLY b.com", bBody)
	}
}

// A forged / anonymous org (X-Org-Id present but NO validated principal) is refused
// 403 and NEVER reaches the DNS plane -- the cloud-side tenant gate, fail-closed.
func TestForgedOrgIsRefusedAndNeverReachesUpstream(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	up.zonesByOrg = map[string]string{"victim": "secret.com"}
	app := dnsApp(t, up.URL)

	// No X-User-Id => principal.Org fails closed. X-Org-Id is a bare forge.
	req := httptest.NewRequest(http.MethodGet, "/v1/dns/zones", nil)
	req.Header.Set("X-Org-Id", "victim")
	res, _ := do(t, app, req)

	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unvalidated (forged) org", res.StatusCode)
	}
	if up.hits != 0 {
		t.Fatalf("upstream was reached %d time(s), want 0 -- a forge must be refused before forwarding", up.hits)
	}
}

// The head builds a FRESH upstream request: an injected inbound header (or cookie)
// that the head does not explicitly set NEVER reaches the DNS plane.
func TestOnlyExplicitHeadersReachUpstream(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	app := dnsApp(t, up.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/dns/zones", nil), "orgA", "orgA/dave", "tokenA")
	req.Header.Set("X-Injected", "evil")
	req.Header.Set("Cookie", "hanzo_iam_token=steal")
	do(t, app, req)

	if v := up.hdrs.Get("X-Injected"); v != "" {
		t.Fatalf("upstream saw injected header X-Injected=%q, want it dropped", v)
	}
	if v := up.hdrs.Get("Cookie"); v != "" {
		t.Fatalf("upstream saw a relayed Cookie=%q, want it dropped (no blind header passthrough)", v)
	}
}

// A degenerate HANZO_DNS_URL that trims to an empty base fails closed (503), never
// forwarding to an unintended host.
func TestDegenerateURLFailsClosed(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	app := dnsApp(t, "/") // TrimRight("/","/") => "" base

	res, _ := do(t, app, as(httptest.NewRequest(http.MethodGet, "/v1/dns/zones", nil), "orgA", "orgA/dave", "tokenA"))
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (empty base must fail closed)", res.StatusCode)
	}
	if up.hits != 0 {
		t.Fatalf("upstream reached %d time(s), want 0", up.hits)
	}
}
