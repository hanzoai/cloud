package destinations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildTags(t *testing.T) {
	// A site's per-project tag config: platform → non-secret pixel id.
	tags := buildTags(map[string]string{
		"ga4":        "G-ABC",
		"meta":       "123",
		"x":          "o1",
		"reddit":     "a2", // server-side only → no browser pixel
		"google-ads": "9",  // server-side only
		"tiktok":     "",   // connected but no id → omitted
	})
	if len(tags) != 3 {
		t.Fatalf("want 3 injectable tags (ga4/meta/x), got %d: %+v", len(tags), tags)
	}
	by := map[string]browserTagOut{}
	for _, tg := range tags {
		by[tg.Platform] = tg
	}
	if by["ga4"].Type != "ga" || by["ga4"].ID != "G-ABC" {
		t.Errorf("ga4 → %+v, want {ga, G-ABC}", by["ga4"])
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
		t.Error("a platform with no id must be omitted")
	}
}

func TestTagsKey(t *testing.T) {
	mk := func(url, auth string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	if k := tagsKey(mk("/v1/tags?key=pk-q", "")); k != "pk-q" {
		t.Errorf("?key= → %q", k)
	}
	if k := tagsKey(mk("/v1/tags?ingest_key=pk-i", "")); k != "pk-i" {
		t.Errorf("?ingest_key= → %q", k)
	}
	if k := tagsKey(mk("/v1/tags", "Bearer pk-b")); k != "pk-b" {
		t.Errorf("Bearer → %q", k)
	}
	if k := tagsKey(mk("/v1/tags?key=pk-q", "Bearer pk-b")); k != "pk-b" {
		t.Errorf("Bearer must win over query, got %q", k)
	}
	if k := tagsKey(mk("/v1/tags", "")); k != "" {
		t.Errorf("no key → %q", k)
	}
}

func TestTagsHost(t *testing.T) {
	mk := func(q, origin, referer string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v1/tags"+q, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		return r
	}
	if h := tagsHost(mk("?host=hanzo.ai", "", "")); h != "hanzo.ai" {
		t.Errorf("?host= → %q", h)
	}
	if h := tagsHost(mk("", "https://hanzo.chat", "")); h != "hanzo.chat" {
		t.Errorf("Origin → %q", h)
	}
	if h := tagsHost(mk("", "", "https://hanzo.app/pricing?x=1")); h != "hanzo.app" {
		t.Errorf("Referer → %q", h)
	}
	if h := tagsHost(mk("", "", "")); h != "" {
		t.Errorf("none → %q", h)
	}
}

// TestServeTagsNoSite proves the fail-safe: with projects unmounted (TagsFor ⇒ false),
// the tag door answers 200 with an empty set and permissive CORS — a page never breaks.
func TestServeTagsNoSite(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/tags?key=pk-x", nil)
	w := httptest.NewRecorder()
	serveTags(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the tag is loaded cross-origin — must allow any origin")
	}
	var out tagConfig
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tags) != 0 {
		t.Errorf("no resolvable site must yield an empty tag set, got %+v", out.Tags)
	}
}
