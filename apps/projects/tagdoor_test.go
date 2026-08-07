package projects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
)

func TestBuildTags(t *testing.T) {
	tags := buildTags(map[string]string{
		"ga4": "G-ABC", "meta": "123", "x": "o1",
		"reddit": "a2", "google-ads": "9", "tiktok": "",
	})
	if len(tags) != 3 {
		t.Fatalf("want 3 (ga4/meta/x), got %d: %+v", len(tags), tags)
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
	if _, has := by["reddit"]; has {
		t.Error("reddit has no browser pixel and must be omitted")
	}
	if _, has := by["tiktok"]; has {
		t.Error("empty id must be omitted")
	}
}

func TestTagsKeyHost(t *testing.T) {
	mk := func(u, auth, origin, ref string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, u, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if ref != "" {
			r.Header.Set("Referer", ref)
		}
		return r
	}
	if k := tagsKey(mk("/v1/tags?key=pk-q", "", "", "")); k != "pk-q" {
		t.Errorf("?key= → %q", k)
	}
	if k := tagsKey(mk("/v1/tags", "Bearer pk-b", "", "")); k != "pk-b" {
		t.Errorf("Bearer → %q", k)
	}
	if k := tagsKey(mk("/v1/tags?key=pk-q", "Bearer pk-b", "", "")); k != "pk-b" {
		t.Errorf("Bearer must win → %q", k)
	}
	if h := tagsHost(mk("/v1/tags?host=hanzo.ai", "", "", "")); h != "hanzo.ai" {
		t.Errorf("?host= → %q", h)
	}
	if h := tagsHost(mk("/v1/tags", "", "https://hanzo.chat", "")); h != "hanzo.chat" {
		t.Errorf("Origin → %q", h)
	}
	if h := tagsHost(mk("/v1/tags", "", "", "https://hanzo.app/x?y=1")); h != "hanzo.app" {
		t.Errorf("Referer → %q", h)
	}
}

// TestServeTagsFailSafe: no key + no host ⇒ resolveSiteTags never touches the store ⇒
// an empty set at 200 with permissive CORS, so a page never breaks on its tag config.
func TestServeTagsFailSafe(t *testing.T) {
	s := &cloud.Service[state]{}
	req := httptest.NewRequest(http.MethodGet, "/v1/tags", nil)
	w := httptest.NewRecorder()
	serveTags(s, w, req)

	res := w.Result()
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
