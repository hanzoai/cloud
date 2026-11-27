package host

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// sharedApp is the edge as a plugin composes it — CORS, then EdgeOrigin, then
// the cache — over the catalog paths and one that is not, each answering whether
// an Origin reached it.
func sharedApp(t *testing.T) *zip.App {
	t.Helper()
	pol, _ := edge.New("", "admin", edge.Policy{CachePaths: publicCatalog})
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(pol, nil))
	app.Use(EdgeOrigin())
	app.Use(EdgeCache(pol))
	for _, p := range []string{"/v1/models", "/v1/pricing/models", "/v1/research/runs", "/v1/template", "/v1/billing/plans", "/v1/account"} {
		app.Raw(http.MethodGet, p, func(c *zip.Ctx) error {
			return c.JSON(200, map[string]string{"origin": c.Header("Origin")})
		})
	}
	return app
}

// TestEdgeShared_AnonymousCatalogIsEveryOrigins: an anonymous read of a catalog
// path is one answer for every origin and for none — `*`, no credentials, no
// Vary: Origin, the same ETag — so one copy at the edge serves them all. No child
// sees the Origin.
func TestEdgeShared_AnonymousCatalogIsEveryOrigins(t *testing.T) {
	app := sharedApp(t)
	for _, path := range []string{"/v1/models", "/v1/pricing/models?id=kai", "/v1/research/runs?org=hanzo&view=summary", "/v1/template"} {
		etags := map[string]bool{}
		// "" is a request with no Origin — a crawler, curl, a server — which may be
		// the one that fills the edge's copy, so it must be answered the same.
		for _, origin := range []string{"", "https://hanzo.ai", "https://cloud.hanzo.ai", "https://evil.example"} {
			for _, method := range []string{"GET", "HEAD"} {
				h := map[string]string{}
				if origin != "" {
					h["Origin"] = origin
				}
				resp, body := cacheDo(t, app, method, path, h)
				if resp.StatusCode != 200 {
					t.Fatalf("%s %s from %s = %d", method, path, origin, resp.StatusCode)
				}
				if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
					t.Errorf("%s %s from %s: ACAO = %q, want *", method, path, origin, got)
				}
				if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
					t.Errorf("%s %s from %s: ACAC = %q, want none", method, path, origin, got)
				}
				if strings.Contains(resp.Header.Get("Vary"), "Origin") {
					t.Errorf("%s %s from %s: Vary = %q, want no Origin", method, path, origin, resp.Header.Get("Vary"))
				}
				if !strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), "Retry-After") {
					t.Errorf("%s %s from %s: exposes %q", method, path, origin, resp.Header.Get("Access-Control-Expose-Headers"))
				}
				if got := resp.Header.Get("Cache-Control"); got != "public, max-age=60, s-maxage=300, stale-while-revalidate=86400" {
					t.Errorf("%s %s from %s: Cache-Control = %q", method, path, origin, got)
				}
				if method == "GET" {
					if !strings.Contains(body, `"origin":""`) {
						t.Errorf("GET %s from %s: a child saw the Origin: %s", path, origin, body)
					}
					etags[resp.Header.Get("ETag")] = true
				}
			}
		}
		if len(etags) != 1 {
			t.Errorf("%s: %d ETags across origins, want one", path, len(etags))
		}
	}
}

// TestEdgeShared_ACredentialKeepsItsOwnAnswer: the same path read with a
// credential is that caller's — the reflected origin with credentials, Vary:
// Origin, private, no-store — so it never reaches or becomes the shared copy.
func TestEdgeShared_ACredentialKeepsItsOwnAnswer(t *testing.T) {
	app := sharedApp(t)
	for _, h := range credentialHeaders {
		resp, _ := cacheDo(t, app, "GET", "/v1/models", map[string]string{"Origin": "https://hanzo.ai", h: "x"})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://hanzo.ai" {
			t.Errorf("%s: ACAO = %q, want the reflected origin", h, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("%s: ACAC = %q, want true", h, got)
		}
		if !strings.Contains(resp.Header.Get("Vary"), "Origin") {
			t.Errorf("%s: Vary = %q, want Origin", h, resp.Header.Get("Vary"))
		}
		if got := resp.Header.Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("%s: Cache-Control = %q, want private, no-store", h, got)
		}
	}
}

// TestEdgeShared_OnlyTheCatalogIsShared: a path outside the catalog — the plan
// list among them, read with credentials: 'include' by signed-out pages — keeps
// the per-origin answer, and a preflight is answered per origin as before.
func TestEdgeShared_OnlyTheCatalogIsShared(t *testing.T) {
	app := sharedApp(t)
	for _, path := range []string{"/v1/billing/plans", "/v1/account"} {
		resp, _ := cacheDo(t, app, "GET", path, map[string]string{"Origin": "https://hanzo.ai"})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://hanzo.ai" {
			t.Errorf("%s: ACAO = %q, want the reflected origin", path, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("%s: ACAC = %q, want true", path, got)
		}
	}
	resp, _ := cacheDo(t, app, "OPTIONS", "/v1/models", map[string]string{
		"Origin": "https://hanzo.ai", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "content-type",
	})
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "https://hanzo.ai" ||
		resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("preflight = %d ACAO %q ACAC %q, want 204 with the reflected origin and credentials", resp.StatusCode,
			resp.Header.Get("Access-Control-Allow-Origin"), resp.Header.Get("Access-Control-Allow-Credentials"))
	}
}

func TestPolicyCatalogNamesTheListedPathsOnly(t *testing.T) {
	pol, _ := edge.New("", "admin", edge.Policy{CachePaths: publicCatalog, CacheTTLSec: 30})
	for path, want := range map[string]bool{
		"/v1/models": true, "/v1/models/kai": true, "/v1/pricing/models": true, "/v1/research/runs": true,
		"/v1/template": true, "/v1/template/prism": true, "/.well-known/agent-skills": true,
		"/v1/billing/plans": false, "/v1/research/studies": false, "/v1/account": false,
	} {
		if got := pol.Catalog(path); got != want {
			t.Errorf("Catalog(%s) = %v, want %v (a default TTL does not make a path a catalog)", path, got, want)
		}
	}
}
