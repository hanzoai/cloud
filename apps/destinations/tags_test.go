package destinations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
)

func TestBuildTags(t *testing.T) {
	tags := buildTags([]Row{
		{Platform: "ga4", Enabled: true, Config: Config{"measurementId": "G-ABC"}},
		{Platform: "meta", Enabled: true, Config: Config{"pixelId": "123"}},
		{Platform: "x", Enabled: true, Config: Config{"pixelId": "o1"}},
		{Platform: "reddit", Enabled: true, Config: Config{"accountId": "a2"}}, // server-side only → no browser pixel
		{Platform: "google-ads", Enabled: true, Config: Config{"customerId": "9"}}, // server-side only
		{Platform: "tiktok", Enabled: true, Config: Config{}}, // connected but no id → omitted
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
	if _, has := by["google-ads"]; has {
		t.Error("google-ads is server-side only and must be omitted")
	}
	if _, has := by["tiktok"]; has {
		t.Error("a connected destination with no browser-tag id must be omitted")
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

// TestServeTagsNoKey proves the fail-safe: no key never touches the store, answers 200
// with an empty tag set and permissive CORS, so a page never breaks on its tag config.
func TestServeTagsNoKey(t *testing.T) {
	s := &cloud.Service[state]{} // no key ⇒ store is never dereferenced
	req := httptest.NewRequest(http.MethodGet, "/v1/tags", nil)
	w := httptest.NewRecorder()
	serveTags(s, w, req)

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
		t.Errorf("no key must yield an empty tag set, got %+v", out.Tags)
	}
}
