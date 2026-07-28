package sites

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// captureHostHandler records the org + path every invocation is handed, so a test
// can prove the site-host carve FORCES the tenant from the resolved Site (never the
// caller/body) and routes only the intended POST paths. It is the analytics twin of
// fakeResolver's recording discipline.
type captureHostHandler struct {
	mu    sync.Mutex
	orgs  []string
	paths []string
	kind  string // header value emitted so a test can see which carve fired
}

func (h *captureHostHandler) handle(org string, c *zip.Ctx) error {
	h.mu.Lock()
	h.orgs = append(h.orgs, org)
	h.paths = append(h.paths, c.Path())
	h.mu.Unlock()
	c.SetHeader("X-Carve", h.kind)
	return c.String(http.StatusOK, "ok")
}

func (h *captureHostHandler) seen() ([]string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	o := append([]string(nil), h.orgs...)
	p := append([]string(nil), h.paths...)
	return o, p
}

// beaconBody is a Segment/PostHog-shaped payload that ALSO carries a foreign org
// claim (properties.space + a body org) — the values the carve must ignore in
// favour of the resolved Site.Org.
const beaconBody = `{"batch":[{"type":"pageview"}],"org":"attacker-org","properties":{"space":"attacker-org"}}`

func postReq(host, path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "attacker-org") // forged identity header — must be ignored
	return req
}

// TestIsAnalyticsPath pins the ingest-path predicate as an EXACT SET: the canonical
// door /v1/event plus the deprecated beacon ingest paths, and nothing else. Everything
// else on a site host falls through to the static serve.
//
// The READ lenses are the point of the exact set. `HasPrefix(p, "/v1/analytics")` also
// matched /v1/analytics/overview|timeseries|top|health, leaving the carve's POST check
// as the only thing keeping a read path out of the beacon handler — so a lens's
// exposure hung on the verb it happened to be mounted under. They are OUT of the set
// now, and stay out even if one grows a POST.
func TestIsAnalyticsPath(t *testing.T) {
	yes := []string{"/v1/event", "/v1/analytics", "/v1/analytics/batch", "/v1/insights/e"}
	for _, p := range yes {
		if !isAnalyticsPath(p) {
			t.Errorf("isAnalyticsPath(%q) = false, want true", p)
		}
	}
	no := []string{
		// the read lenses — never ingest, whatever verb they carry
		"/v1/analytics/overview", "/v1/analytics/timeseries", "/v1/analytics/top", "/v1/analytics/health",
		// no prefix match: a future /v1/analytics/* route is out by default
		"/v1/analytics/anything", "/v1/analytics/batch/extra", "/v1/eventx", "/v1/insights/e/extra",
		"/v1/base", "/v1/base/collections", "/v1/tracker",
		"/v1/insights", "/v1/insights/events", "/v1/insights/health", "/", "/index.html",
	}
	for _, p := range no {
		if isAnalyticsPath(p) {
			t.Errorf("isAnalyticsPath(%q) = true, want false", p)
		}
	}
}

// TestMiddlewareAnalyticsCarveReadLensPostNotHijacked is the behavioral half of the
// exact set: a POST to a read-lens path on a site host must NOT reach the ingest carve.
// Under the old prefix predicate the method check was the only guard, so this case
// depended entirely on the lenses never accepting a POST.
func TestMiddlewareAnalyticsCarveReadLensPostNotHijacked(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "hanzo", Slug: "yadota", Bucket: "b", Prefix: "hanzo/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	h := &captureHostHandler{kind: "analytics"}
	SetAnalyticsHostHandler(h.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(testServer())

	for _, p := range []string{"/v1/analytics/overview", "/v1/analytics/timeseries", "/v1/analytics/top", "/v1/analytics/health"} {
		resp, err := app.Fiber().Test(postReq("yadota.hanzo.app", p, beaconBody))
		if err != nil {
			t.Fatalf("POST %s: %v", p, err)
		}
		if resp.Header.Get("X-Carve") == "analytics" {
			t.Errorf("POST %s routed to the ingest carve — a read lens is not an ingest path", p)
		}
	}
	if orgs, _ := h.seen(); len(orgs) != 0 {
		t.Fatalf("the ingest carve fired for a read-lens path (%v)", orgs)
	}
}

// TestMiddlewareAnalyticsCarveSlugHost: a POST beacon to a LIVE slug host on each
// ingest path routes to the analytics handler with the tenant = Site.Org — NOT the
// attacker-org in the body/header — and never leaks into the API pipeline.
func TestMiddlewareAnalyticsCarveSlugHost(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "hanzo", Slug: "yadota", Bucket: "b", Prefix: "hanzo/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	h := &captureHostHandler{kind: "analytics"}
	SetAnalyticsHostHandler(h.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(testServer())

	for _, p := range []string{"/v1/event", "/v1/analytics", "/v1/analytics/batch", "/v1/insights/e"} {
		resp, err := app.Fiber().Test(postReq("yadota.hanzo.app", p, beaconBody))
		if err != nil {
			t.Fatalf("POST %s: %v", p, err)
		}
		if resp.Header.Get("X-Carve") != "analytics" {
			t.Errorf("POST %s did not route to the analytics carve (X-Carve=%q)", p, resp.Header.Get("X-Carve"))
		}
		if resp.Header.Get("X-Sentinel") == "hit" {
			t.Errorf("POST %s leaked into the API pipeline", p)
		}
	}
	orgs, paths := h.seen()
	if len(orgs) != 4 {
		t.Fatalf("analytics handler invoked %d times, want 4 (%v)", len(orgs), paths)
	}
	for i, o := range orgs {
		if o != "hanzo" {
			t.Fatalf("ingest %d attributed to %q, want hanzo (host-derived, not the body/header claim)", i, o)
		}
	}
}

// TestMiddlewareAnalyticsCarveCustomDomain: the same forced-org carve fires for a
// bound LIVE custom domain, resolved by its FULL host, tenant = Site.Org.
func TestMiddlewareAnalyticsCarveCustomDomain(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "yadota", Slug: "yadota", Bucket: "b", Prefix: "yadota/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	h := &captureHostHandler{kind: "analytics"}
	SetAnalyticsHostHandler(h.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(selfServer())

	resp, err := app.Fiber().Test(postReq("yadota.tech", "/v1/analytics", beaconBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.Header.Get("X-Carve") != "analytics" || resp.Header.Get("X-Sentinel") == "hit" {
		t.Fatalf("custom-domain beacon did not route to the analytics carve (X-Carve=%q)", resp.Header.Get("X-Carve"))
	}
	orgs, _ := h.seen()
	if len(orgs) != 1 || orgs[0] != "yadota" {
		t.Fatalf("custom-domain ingest orgs = %v, want [yadota] (host-derived)", orgs)
	}
	if got := fr.slugs(); len(got) != 1 || got[0] != "yadota.tech" {
		t.Fatalf("resolver called with %v, want exactly [yadota.tech] (full host only)", got)
	}
}

// TestMiddlewareAnalyticsCarveGetServesStatic: a GET on an ingest path is NOT
// hijacked — the POST gate lets it fall to the static serve (X-Hanzo-Site set,
// carve never fired). This is what keeps the read-lens surface uninvolved.
func TestMiddlewareAnalyticsCarveGetServesStatic(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "hanzo", Slug: "yadota", Bucket: "b", Prefix: "hanzo/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	h := &captureHostHandler{kind: "analytics"}
	SetAnalyticsHostHandler(h.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(testServer())

	req := httptest.NewRequest(http.MethodGet, "http://yadota.hanzo.app/v1/analytics/overview", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.Header.Get("X-Carve") == "analytics" {
		t.Fatal("a GET must never route to the analytics ingest carve")
	}
	if resp.Header.Get("X-Hanzo-Site") != "yadota" {
		t.Errorf("GET did not reach the static serve (X-Hanzo-Site=%q)", resp.Header.Get("X-Hanzo-Site"))
	}
	if resp.Header.Get("X-Sentinel") == "hit" {
		t.Error("site host leaked into the API pipeline on GET")
	}
	if orgs, _ := h.seen(); len(orgs) != 0 {
		t.Fatalf("analytics carve fired on a GET (%v)", orgs)
	}
}

// TestMiddlewareBaseCarveStillWins: the base carve stays first and independent — a
// /v1/base request on a site host still routes to base, never to the analytics
// carve (the two paths are disjoint but this pins the ordering explicitly).
func TestMiddlewareBaseCarveStillWins(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "hanzo", Slug: "yadota", Bucket: "b", Prefix: "hanzo/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	base := &captureHostHandler{kind: "base"}
	anal := &captureHostHandler{kind: "analytics"}
	SetBaseHostHandler(base.handle)
	defer SetBaseHostHandler(nil)
	SetAnalyticsHostHandler(anal.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(testServer())

	req := httptest.NewRequest(http.MethodGet, "http://yadota.hanzo.app/v1/base/collections", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET /v1/base: %v", err)
	}
	if resp.Header.Get("X-Carve") != "base" {
		t.Fatalf("/v1/base did not route to the base carve (X-Carve=%q)", resp.Header.Get("X-Carve"))
	}
	if orgs, _ := anal.seen(); len(orgs) != 0 {
		t.Fatalf("analytics carve fired for a /v1/base request (%v)", orgs)
	}
}

// TestMiddlewareAnalyticsCarveNonSiteHostUnaffected: a self host (api.hanzo.ai) is
// never a site — a beacon POST there Continues to the normal pipeline, the resolver
// is never consulted, and the carve never fires.
func TestMiddlewareAnalyticsCarveNonSiteHostUnaffected(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "x", Slug: "x", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	h := &captureHostHandler{kind: "analytics"}
	SetAnalyticsHostHandler(h.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(selfServer())

	resp, err := app.Fiber().Test(postReq("api.hanzo.ai", "/v1/analytics", beaconBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.Header.Get("X-Sentinel") != "hit" {
		t.Fatalf("self host beacon did not pass through to the API pipeline (status %d)", resp.StatusCode)
	}
	if orgs, _ := h.seen(); len(orgs) != 0 {
		t.Fatalf("analytics carve fired for a self host (%v)", orgs)
	}
	if n := len(fr.slugs()); n != 0 {
		t.Fatalf("resolver consulted %d times for a self host, want 0", n)
	}
}

// TestMiddlewareAnalyticsCarveNonLive405: a beacon POST to a NON-live site host is
// not ingested — it falls to the static serve, which 405s the POST (unchanged from
// today). Only a LIVE site accepts its beacons.
func TestMiddlewareAnalyticsCarveNonLive405(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "hanzo", Slug: "yadota", Status: "building"}}
	SetResolver(fr)
	defer SetResolver(nil)
	h := &captureHostHandler{kind: "analytics"}
	SetAnalyticsHostHandler(h.handle)
	defer SetAnalyticsHostHandler(nil)
	app := newTestApp(testServer())

	resp, err := app.Fiber().Test(postReq("yadota.hanzo.app", "/v1/analytics", beaconBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("non-live beacon POST → %d, want 405", resp.StatusCode)
	}
	if orgs, _ := h.seen(); len(orgs) != 0 {
		t.Fatalf("analytics carve fired for a non-live site (%v)", orgs)
	}
}

// TestMiddlewareNoAnalyticsHandlerIs405: with NO handler installed (the default,
// e.g. public capture disabled), a site host still 405s a beacon POST — the fix is
// inert until analytics.Mount installs the carve.
func TestMiddlewareNoAnalyticsHandlerIs405(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "hanzo", Slug: "yadota", Bucket: "b", Prefix: "hanzo/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	SetAnalyticsHostHandler(nil)
	app := newTestApp(testServer())

	resp, err := app.Fiber().Test(postReq("yadota.hanzo.app", "/v1/analytics", beaconBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("no-handler beacon POST → %d, want 405 (carve inert until installed)", resp.StatusCode)
	}
}
