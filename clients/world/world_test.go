package world

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// gdeltFixture is one article in GDELT 2.0 Doc artlist JSON shape.
const gdeltFixture = `{"articles":[
  {"url":"https://example.com/a","title":"Border conflict escalates",
   "domain":"example.com","seendate":"20260707T120000Z",
   "socialimage":"https://img.example.com/a.jpg","language":"English","tone":-2.5}
]}`

// rssFixture is an RSS 2.0 feed with one keyword-matching item and one that must
// be filtered out by a "conflict" keyword filter.
const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Fixture World</title>
  <item>
    <title>Regional conflict talks resume</title>
    <link>https://example.com/rss1</link>
    <pubDate>Tue, 07 Jul 2026 09:00:00 GMT</pubDate>
  </item>
  <item>
    <title>Local weather forecast sunny</title>
    <link>https://example.com/rss2</link>
    <pubDate>Tue, 07 Jul 2026 08:00:00 GMT</pubDate>
  </item>
</channel></rss>`

// fixtureServer serves the GDELT + RSS upstreams so getNews runs deterministically
// offline.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/gdelt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gdeltFixture)
	})
	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, rssFixture)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountWorld mounts the world surface on a fresh app with a temp data dir and
// returns the app plus the mounted service (white-box access for test wiring).
func mountWorld(t *testing.T) (*zip.App, *service) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app, mounted
}

// do issues a request against the mounted app. A non-empty user simulates the
// gateway-minted validated principal (X-User-Id); project sets X-Project-Id.
func do(t *testing.T, app *zip.App, method, path, org, user, project string, body any) (int, []byte) {
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
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestGetNews_MergesNormalizesAndFilters(t *testing.T) {
	srv := fixtureServer(t)
	app, svc := mountWorld(t)

	// Point the upstreams at the fixture server and allowlist its host.
	host := strings.ToLower(mustURL(t, srv.URL).Hostname())
	svc.gdeltBase = srv.URL + "/gdelt"
	svc.rssAllow = map[string]struct{}{host: {}}

	// Seed a pipeline: the fixture RSS feed + a "conflict" keyword filter (also
	// seeds the GDELT query). PUT exercises the write + allowlist path.
	code, body := do(t, app, http.MethodPut, "/v1/world/pipeline", "acme", "u1", "proj1", pipelineReq{
		Feeds:   []string{srv.URL + "/rss"},
		Filters: Filters{Keywords: []string{"conflict"}},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT pipeline: status %d body %s", code, body)
	}

	code, body = do(t, app, http.MethodGet, "/v1/world/news", "acme", "u1", "proj1", nil)
	if code != http.StatusOK {
		t.Fatalf("GET news: status %d body %s", code, body)
	}
	var got newsResponse
	mustJSON(t, body, &got)

	// The "weather" RSS item must be filtered out by the "conflict" keyword.
	if len(got.Items) != 2 {
		t.Fatalf("want 2 items (gdelt + rss-conflict), got %d: %s", len(got.Items), body)
	}
	// Freshest-first: GDELT (12:00Z) before RSS (09:00Z).
	if got.Items[0].Link != "https://example.com/a" {
		t.Fatalf("want gdelt item first, got %+v", got.Items[0])
	}
	g := got.Items[0]
	if g.Source != "example.com" || g.Tone != "-2.5" || g.Lang != "English" || g.Image == "" {
		t.Fatalf("gdelt normalization wrong: %+v", g)
	}
	if _, err := time.Parse(time.RFC3339, g.PubDate); err != nil {
		t.Fatalf("gdelt pubDate not RFC3339 (%q): %v", g.PubDate, err)
	}
	rss := got.Items[1]
	if rss.Link != "https://example.com/rss1" || rss.Source != "Fixture World" {
		t.Fatalf("rss normalization wrong: %+v", rss)
	}
	if _, err := time.Parse(time.RFC3339, rss.PubDate); err != nil {
		t.Fatalf("rss pubDate not RFC3339 (%q): %v", rss.PubDate, err)
	}
	for _, it := range got.Items {
		if strings.Contains(strings.ToLower(it.Title), "weather") {
			t.Fatalf("keyword filter failed: weather item leaked: %+v", it)
		}
	}
}

func TestNews_NoValidatedPrincipal_403(t *testing.T) {
	app, _ := mountWorld(t)
	// X-Org-Id present but NO X-User-Id — the anonymous-forge path principal.Tenant
	// must refuse.
	code, body := do(t, app, http.MethodGet, "/v1/world/news", "victim", "", "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("want 403 for unvalidated principal, got %d: %s", code, body)
	}
}

func TestPutPipeline_RejectsNonAllowlistedFeed(t *testing.T) {
	app, _ := mountWorld(t) // real allowlist — evil.example.com is not in it
	code, body := do(t, app, http.MethodPut, "/v1/world/pipeline", "acme", "u1", "", pipelineReq{
		Feeds: []string{"https://evil.example.com/rss"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400 rejecting non-allowlisted feed, got %d: %s", code, body)
	}
	if !strings.Contains(string(body), "not allowlisted") {
		t.Fatalf("want allowlist rejection message, got: %s", body)
	}
}

func TestFetchRSS_RejectsNonAllowlistedHost(t *testing.T) {
	_, svc := mountWorld(t) // real allowlist
	_, err := svc.fetchRSS(context.Background(), "https://evil.example.com/feed.xml")
	if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("want allowlist SSRF rejection, got err=%v", err)
	}
}

func TestPipeline_PutGetRoundTrip(t *testing.T) {
	app, _ := mountWorld(t) // real allowlist — use a real allowlisted host
	feed := "https://feeds.bbci.co.uk/news/world/rss.xml"
	code, body := do(t, app, http.MethodPut, "/v1/world/pipeline", "acme", "u1", "proj1", pipelineReq{
		Feeds:   []string{feed},
		Filters: Filters{Keywords: []string{"AI"}, Regions: []string{"Europe"}},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: status %d body %s", code, body)
	}

	code, body = do(t, app, http.MethodGet, "/v1/world/pipeline", "acme", "u1", "proj1", nil)
	if code != http.StatusOK {
		t.Fatalf("GET: status %d body %s", code, body)
	}
	var pv pipelineView
	mustJSON(t, body, &pv)
	if pv.Default {
		t.Fatalf("want stored pipeline (default=false), got %+v", pv)
	}
	if len(pv.Feeds) != 1 || pv.Feeds[0] != feed {
		t.Fatalf("feeds round-trip wrong: %+v", pv.Feeds)
	}
	if len(pv.Filters.Keywords) != 1 || pv.Filters.Keywords[0] != "AI" {
		t.Fatalf("keywords round-trip wrong: %+v", pv.Filters)
	}
	if pv.Org != "acme" || pv.Project != "proj1" {
		t.Fatalf("scope round-trip wrong: org=%q project=%q", pv.Org, pv.Project)
	}
}

func TestBus_OrgScopedFanout(t *testing.T) {
	b := newBus()
	defer b.close()
	ch, cancel := b.subscribe("acme")
	defer cancel()

	b.publish(streamUpdate{Org: "other", Project: "p", Items: []NewsItem{{Title: "nope"}}})
	b.publish(streamUpdate{Org: "acme", Project: "p", Items: []NewsItem{{Title: "yes"}}})

	select {
	case u := <-ch:
		if u.Org != "acme" || len(u.Items) != 1 || u.Items[0].Title != "yes" {
			t.Fatalf("want acme update, got %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for acme update")
	}
	// The "other" org update must NOT be delivered to the acme subscriber.
	select {
	case u := <-ch:
		t.Fatalf("cross-org leak: acme subscriber received %+v", u)
	default:
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
}
