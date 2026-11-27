package host

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/internal/markdown"
	"github.com/zap-proto/zip"
)

func cacheTestApp(t *testing.T) *zip.App {
	t.Helper()
	pol, _ := edge.New("", "admin", edge.Policy{CachePaths: publicCatalog})
	app := zip.New(zip.Config{})
	app.Use(markdown.Negotiation(nil))
	app.Use(EdgeCache(pol))
	app.Raw(http.MethodGet, "/v1/models", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]any{"data": []string{"zen5"}})
	})
	app.Raw(http.MethodGet, "/v1/models/missing", func(c *zip.Ctx) error {
		return c.JSON(404, map[string]string{"error": "no such model"})
	})
	app.Raw(http.MethodGet, "/v1/account", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"id": "me"})
	})
	app.Raw(http.MethodGet, "/v1/avatar", func(c *zip.Ctx) error {
		c.SetHeader("Cache-Control", "public, max-age=31536000, immutable")
		return c.String(200, "png")
	})
	app.Raw(http.MethodGet, "/v1/session", func(c *zip.Ctx) error {
		c.SetHeader("Set-Cookie", "s=1")
		return c.JSON(200, map[string]string{"ok": "1"})
	})
	app.Raw(http.MethodPost, "/v1/models", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"ok": "1"})
	})
	return app
}

func cacheDo(t *testing.T, app *zip.App, method, target string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestEdgeCache_AnonymousCatalogIsPublic(t *testing.T) {
	app := cacheTestApp(t)
	resp, body := cacheDo(t, app, "GET", "/v1/models", nil)
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=60, s-maxage=300, stale-while-revalidate=86400" {
		t.Fatalf("Cache-Control = %q", got)
	}
	etag := resp.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q, want a weak tag", etag)
	}
	if !strings.Contains(body, "zen5") {
		t.Fatalf("body = %q", body)
	}

	resp, body = cacheDo(t, app, "GET", "/v1/models", map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified || body != "" {
		t.Fatalf("revalidation = %d %q, want 304 with no body", resp.StatusCode, body)
	}
	if resp.Header.Get("ETag") != etag {
		t.Fatalf("304 ETag = %q, want %q", resp.Header.Get("ETag"), etag)
	}
	if resp, _ = cacheDo(t, app, "GET", "/v1/models", map[string]string{"If-None-Match": `"other"`}); resp.StatusCode != 200 {
		t.Fatalf("a different tag = %d, want 200", resp.StatusCode)
	}
}

func TestEdgeCache_CredentialIsPrivate(t *testing.T) {
	app := cacheTestApp(t)
	for _, h := range credentialHeaders {
		resp, _ := cacheDo(t, app, "GET", "/v1/models", map[string]string{h: "x"})
		if got := resp.Header.Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("%s: Cache-Control = %q, want private, no-store", h, got)
		}
		if resp.Header.Get("ETag") != "" {
			t.Errorf("%s: a private answer carries an ETag", h)
		}
	}
}

func TestEdgeCache_UnlistedErrorOrWriteSaysNothing(t *testing.T) {
	app := cacheTestApp(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/account"},        // no TTL: the edge policy does not name it
		{"GET", "/v1/models/missing"}, // an error must not outlive its moment
		{"GET", "/v1/session"},        // a cookie-setting answer is someone's
		{"POST", "/v1/models"},        // a write
	} {
		resp, _ := cacheDo(t, app, tc.method, tc.path, nil)
		if got := resp.Header.Get("Cache-Control"); got != "" {
			t.Errorf("%s %s: Cache-Control = %q, want none", tc.method, tc.path, got)
		}
		if resp.Header.Get("ETag") != "" {
			t.Errorf("%s %s: carries an ETag", tc.method, tc.path)
		}
	}
}

func TestEdgeCache_HandlerPublicKeepsItsWordAndGainsAnETag(t *testing.T) {
	app := cacheTestApp(t)
	resp, _ := cacheDo(t, app, "GET", "/v1/avatar", nil)
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want the handler's", got)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("a public answer carries no ETag")
	}
}

func TestEdgeCache_KeepsMarkdownVary(t *testing.T) {
	app := cacheTestApp(t)
	resp, _ := cacheDo(t, app, "GET", "/v1/models", map[string]string{"Accept": "text/markdown"})
	if !strings.Contains(resp.Header.Get("Vary"), "Accept") {
		t.Fatalf("Vary = %q, want Accept", resp.Header.Get("Vary"))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown") {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestEdgeCache_Helpers(t *testing.T) {
	for cc, want := range map[string]bool{
		"public, max-age=60":   true,
		"public, no-store":     false,
		"private, max-age=60":  false,
		"max-age=60":           false,
		"Public, S-MaxAge=300": true,
	} {
		if got := isPublic(cc); got != want {
			t.Errorf("isPublic(%q) = %v", cc, got)
		}
	}
	for inm, want := range map[string]bool{
		`W/"abc"`:      true,
		`"abc"`:        true,
		`"x", W/"abc"`: true,
		`*`:            true,
		`"abd"`:        false,
		``:             false,
	} {
		if got := etagMatches(inm, `W/"abc"`); got != want {
			t.Errorf("etagMatches(%q) = %v", inm, got)
		}
	}
	if got := publicCache(30); got != "public, max-age=30, s-maxage=30, stale-while-revalidate=86400" {
		t.Errorf("publicCache(30) = %q", got)
	}
}

// The plan catalog and commerce's catalog are one copy for every anonymous reader,
// the plan catalog's probe is never kept, and the plan list a browser reads with
// its credentials stays out.
func TestTheCatalogNamesThePlanAndCommerceReads(t *testing.T) {
	pol, _ := edge.New("", "admin", edge.Policy{CachePaths: publicCatalog})
	for path, want := range map[string]bool{
		"/v1/plan":                    true,
		"/v1/plan/gpu":                true,
		"/v1/plan/health":             false,
		"/v1/commerce/catalog":        true,
		"/v1/billing/plans":           false,
		"/v1/leaderboard/standing":           false,
		"/v1/leaderboard/badge/zeekay.svg":   true,
		"/v1/reward/badge/zeekay.svg":        true,
		"/v1/commerce/catalog/models":        true,
	} {
		if got := pol.Catalog(path); got != want {
			t.Errorf("Catalog(%q) = %v, want %v", path, got, want)
		}
	}
}
