package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	fiber "github.com/zap-proto/fiber/v3"
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
		app.Raw(http.MethodGet, "/v1/project/tags", func(c *zip.Ctx) error {
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
		if _, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
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
	app.Raw(http.MethodGet, "/v1/project/tags", func(c *zip.Ctx) error { return serveTags(s, c) })
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/project/tags", nil), fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	// A visitor nobody can place is under the strict rule: eu, nothing on. The answer
	// names the visitor's region, so a shared cache must not keep it, and it varies by
	// every header it turns on.
	if out.Consent.Mode != "eu" || out.Consent.Defaults.Analytics || out.Consent.Defaults.Marketing {
		t.Errorf("an unplaced visitor is eu with nothing on, got %+v", out.Consent)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control = %q, the answer is per visitor", cc)
	}
	for _, h := range []string{"Authorization", "Sec-GPC", "DNT", "CF-IPCountry", "CF-Region-Code"} {
		if !strings.Contains(res.Header.Get("Vary"), h) {
			t.Errorf("Vary = %q, want %s: the answer turns on it", res.Header.Get("Vary"), h)
		}
	}
}

// A conversion rule beside a platform's id rides that tag as `events`, keyed by
// our event name; a rule with no platform id is dropped with it.
func TestBuildTagsCarriesConversionRules(t *testing.T) {
	tags := buildTags(map[string]string{
		"linkedin": "555", "linkedin:order_completed": "9876", "linkedin:signup_completed": "9875",
		"google-ads": "AW-1", "google-ads:order_completed": "AW-1/abc",
		"x:order_completed": "tw-1-2",
	})
	by := map[string]browserTagOut{}
	for _, tg := range tags {
		by[tg.Platform] = tg
	}
	if len(tags) != 2 {
		t.Fatalf("want linkedin and google-ads only, got %+v", tags)
	}
	if by["linkedin"].Events["order_completed"] != "9876" || by["linkedin"].Events["signup_completed"] != "9875" {
		t.Errorf("linkedin rules: %+v", by["linkedin"])
	}
	if by["google-ads"].Events["order_completed"] != "AW-1/abc" {
		t.Errorf("google-ads rules: %+v", by["google-ads"])
	}
}

// A host that carries its own tags reports to its own stream even when the page sends
// the org's shared key; a host with none falls back to the key's project.
func TestSiteTagsHostBeatsKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := storeAt(t)
		mk := func(slug string, tags map[string]string) Project {
			p := Project{Org: "hanzo", Slug: slug, Analytics: true, Tags: tags, CreatedAt: 1, UpdatedAt: 1, ID: slug}
			if err := setProjectDefaults(&p, nil); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateProject(ctx, p); err != nil {
				t.Fatal(err)
			}
			return p
		}
		main := mk("ai", map[string]string{"ga4": "G-MAIN"})
		mk("market", map[string]string{"ga4": "G-MARKET"})
		mk("bare", nil)
		for host, slug := range map[string]string{"hanzo.market": "market", "hanzo.bare": "bare"} {
			if err := st.BindHost(ctx, host, "hanzo", slug, 1); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := siteOf(ctx, st, main.Key, "hanzo.market"); got.Tags["ga4"] != "G-MARKET" {
			t.Errorf("host with its own tags: got %v", got.Tags)
		}
		if got, _ := siteOf(ctx, st, main.Key, "hanzo.bare"); got.Tags["ga4"] != "G-MAIN" {
			t.Errorf("host with no tags falls back to the key: got %v", got.Tags)
		}
		if got, _ := siteOf(ctx, st, main.Key, "unbound.example"); got.Tags["ga4"] != "G-MAIN" {
			t.Errorf("unbound host: got %v", got.Tags)
		}
	})
}

// TestBuildTagsNamesEveryMetaPixel: a site reporting to several brands names each
// Meta pixel, comma separated; each is a tag of its own with the platform's rules,
// trimmed, once, in order, and nothing that is not a pixel id survives. One id is a
// tag as it always was.
func TestBuildTagsNamesEveryMetaPixel(t *testing.T) {
	tags := buildTags(map[string]string{
		"meta":      " 1790308611893001, 758250658033751 ,366194547064903,1790308611893001, x<script>,, 12a",
		"meta:lead": "Lead",
		"ga4":       "G-ABC, G-DEF",
	})
	var meta []browserTagOut
	for _, tg := range tags {
		if tg.Platform == "meta" {
			meta = append(meta, tg)
		}
	}
	if len(meta) != 3 || meta[0].ID != "1790308611893001" || meta[1].ID != "758250658033751" || meta[2].ID != "366194547064903" {
		t.Fatalf("meta = %+v", meta)
	}
	for _, tg := range meta {
		if tg.Type != "meta" || tg.Events["lead"] != "Lead" {
			t.Errorf("every pixel carries the type and the rules: %+v", tg)
		}
	}
	// Only Meta reads a list: another platform's entry is the one id it holds.
	if n := len(tags) - len(meta); n != 1 || tags[0].ID != "G-ABC, G-DEF" {
		t.Errorf("ga4 = %+v", tags)
	}
	if one := buildTags(map[string]string{"meta": "123"}); len(one) != 1 || one[0].ID != "123" {
		t.Errorf("one pixel = %+v", one)
	}
}

func TestHanzoBrandLinkedInTagDefaults(t *testing.T) {
	tags := buildTags(withHanzoBrandTags("hanzo", map[string]string{"ga4": "G-HANZO", "meta": "1790308611893001"}))
	by := map[string]browserTagOut{}
	for _, tg := range tags {
		by[tg.Platform] = tg
	}
	li, ok := by["linkedin"]
	if !ok || li.ID != "10406532" {
		t.Fatalf("hanzo brand site missing LinkedIn Insight Tag 10406532: %+v", li)
	}
	oai, ok := by["openai"]
	if !ok || oai.Type != "openai" || oai.ID != "TXKHZduRxZZGp4B1ysfiwm" {
		t.Fatalf("hanzo brand site missing OpenAI pixel TXKHZduRxZZGp4B1ysfiwm: %+v", oai)
	}
	// Disabled legacy Boost tag 10420116 is replaced by 10406532.
	legacy := buildTags(withHanzoBrandTags("hanzo", map[string]string{"ga4": "G-HANZO", "meta": "1790308611893001", "linkedin": "10420116"}))
	for _, tg := range legacy {
		if tg.Platform == "linkedin" && tg.ID != "10406532" {
			t.Fatalf("disabled LinkedIn partner ID leaked: %+v", tg)
		}
	}
	// Customer orgs never inherit Hanzo's LinkedIn or OpenAI pixels.
	cust := buildTags(withHanzoBrandTags("acme", map[string]string{"ga4": "G-ACME", "meta": "1790308611893001"}))
	for _, tg := range cust {
		if tg.Platform == "linkedin" || tg.Platform == "openai" {
			t.Fatalf("customer org must not inherit Hanzo brand tag: %+v", tg)
		}
	}
}
