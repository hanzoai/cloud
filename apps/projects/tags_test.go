package projects

import (
	"encoding/json"
	"github.com/zap-proto/zip"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
)

func TestBuildTags(t *testing.T) {
	tags := buildTags(map[string]string{
		"ga4": "G-ABC", "meta": "123", "x": "o1",
		"reddit": "a2", "google-ads": "9", "tiktok": "",
		"insights": "hi-1",
	})
	// tiktok is present with an EMPTY id, so it is omitted; the other five carry ids.
	if len(tags) != 5 {
		t.Fatalf("want 5 (ga4/google-ads/meta/reddit/x), got %d: %+v", len(tags), tags)
	}
	by := map[string]browserTagOut{}
	for _, tg := range tags {
		by[tg.Platform] = tg
	}
	if by["ga4"].Type != "ga" || by["ga4"].ID != "G-ABC" {
		t.Errorf("ga4 → %+v", by["ga4"])
	}
	if by["meta"].Type != "meta" || by["meta"].ID != "123" {
		t.Errorf("meta → %+v", by["meta"])
	}
	if by["x"].Type != "x" || by["x"].ID != "o1" {
		t.Errorf("x → %+v", by["x"])
	}
	if by["reddit"].Type != "reddit" || by["reddit"].ID != "a2" {
		t.Errorf("reddit → %+v", by["reddit"])
	}
	if by["google-ads"].Type != "gads" || by["google-ads"].ID != "9" {
		t.Errorf("google-ads → %+v", by["google-ads"])
	}
	// Hanzo Insights is a server-side destination with no browser pixel, so a site
	// that has connected it contributes nothing to the tag.
	if _, has := by["insights"]; has {
		t.Error("insights forwards server-side only and must be omitted")
	}
	if _, has := by["tiktok"]; has {
		t.Error("empty id must be omitted")
	}
}

func TestTagsKeyHost(t *testing.T) {
	// A real request through the real route, because that is the only way to hold
	// a Ctx — and it is the better subject anyway: it exercises the address the
	// handler is registered at rather than a hand-made request the router never saw.
	// The Ctx is request-scoped and released when the handler returns, so what
	// travels back out is the ANSWER, not the context it was read from.
	mk := func(u, auth, origin, ref string) (string, string) {
		t.Helper()
		app := zip.New(zip.Config{DisableStartupMessage: true})
		var key, host string
		reached := false
		app.Get("/v1/project/tags", func(c *zip.Ctx) error {
			key, host, reached = tagsKey(c), tagsHost(c), true
			return c.NoContent(http.StatusNoContent)
		})
		if err := app.Build(); err != nil {
			t.Fatalf("build: %v", err)
		}
		r := httptest.NewRequest(http.MethodGet, u, nil)
		for k, v := range map[string]string{"Authorization": auth, "Origin": origin, "Referer": ref} {
			if v != "" {
				r.Header.Set(k, v)
			}
		}
		if _, err := app.Fiber().Test(r); err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		if !reached {
			t.Fatalf("GET %s never reached the handler", u)
		}
		return key, host
	}
	if k, _ := mk("/v1/project/tags?key=pk-q", "", "", ""); k != "pk-q" {
		t.Errorf("?key= → %q", k)
	}
	if k, _ := mk("/v1/project/tags", "Bearer pk-b", "", ""); k != "pk-b" {
		t.Errorf("Bearer → %q", k)
	}
	if k, _ := mk("/v1/project/tags?key=pk-q", "Bearer pk-b", "", ""); k != "pk-b" {
		t.Errorf("Bearer must win → %q", k)
	}
	if _, h := mk("/v1/project/tags?host=hanzo.ai", "", "", ""); h != "hanzo.ai" {
		t.Errorf("?host= → %q", h)
	}
	if _, h := mk("/v1/project/tags", "", "https://hanzo.chat", ""); h != "hanzo.chat" {
		t.Errorf("Origin → %q", h)
	}
	if _, h := mk("/v1/project/tags", "", "", "https://hanzo.app/x?y=1"); h != "hanzo.app" {
		t.Errorf("Referer → %q", h)
	}
}

// TestServeTagsFailSafe: no key + no host ⇒ resolveSiteTags never touches the store ⇒
// an empty set at 200 with permissive CORS, so a page never breaks on its tag config.
func TestServeTagsFailSafe(t *testing.T) {
	s := &cloud.Service[state]{}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/v1/project/tags", func(c *zip.Ctx) error { return serveTags(s, c) })
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/project/tags", nil))
	if err != nil {
		t.Fatalf("GET /v1/project/tags: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the tag loads cross-origin — must allow any origin")
	}
	var out tagConfig
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tags) != 0 {
		t.Errorf("no resolvable site must yield an empty tag set, got %+v", out.Tags)
	}
}
