package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/outbound"
)

// site is a page URL on a public address literal, so escalation passes the
// address check without asking live DNS.
const site = "https://93.184.216.34"

// ── escalation ──────────────────────────────────────────────────────────────
//
// The contract is narrow and each half matters for a different reason: escalate
// when the static read is too thin to be the page (else a single-page app returns
// 200 with nothing), and DON'T when it is not (else every crawl pays for a
// browser it did not need). The rest of these cases are the ways escalation can
// quietly make things worse.

// browserAt points the escalation at a stub for the duration of a test.
func browserAt(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Setenv("CRAWL_URL", srv.URL)
	t.Cleanup(srv.Close)
}

// rendered answers /crawl the way the service does: an envelope with a boolean
// success and results inline.
func rendered(md string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"results": []map[string]any{{
				"url": site + "/app", "success": true, "markdown": md,
			}},
		})
	}
}

func TestEscalateReplacesAThinPage(t *testing.T) {
	// Trimmed: browse trims, which is correct — a render's leading/trailing
	// whitespace is not content. The expectation has to match that.
	full := strings.TrimSpace(strings.Repeat("the rendered article. ", 60))
	browserAt(t, rendered(full))

	shell := &Page{URL: site + "/app", Markdown: "Loading…"}
	got := escalate(context.Background(), shell, site+"/app")

	if got.Markdown != full {
		t.Fatalf("a thin page was not replaced by the render:\n got %q", trunc(got.Markdown))
	}
	if got.Metadata["renderer"] != "browser" {
		t.Errorf("the rendered page should record renderer=browser, got %v", got.Metadata["renderer"])
	}
}

func TestEscalateLeavesASufficientPageAlone(t *testing.T) {
	// Fails the test if the browser is called at all: a page that already has its
	// content must not cost a render.
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the browser was called for a page that was already complete")
	})

	article := strings.Repeat("real content already present. ", 40)
	p := &Page{URL: site + "/post", Markdown: article}

	if got := escalate(context.Background(), p, site+"/post"); got.Markdown != article {
		t.Fatalf("a complete page was altered")
	}
}

func TestEscalateKeepsTheStaticPageWhenTheBrowserIsUnreachable(t *testing.T) {
	// The state this ships in: crawl not deployed. A crawl that worked before must
	// still work, so an absent browser is a non-event.
	t.Setenv("CRAWL_URL", "http://127.0.0.1:1")

	shell := &Page{URL: site + "/app", Markdown: "Loading…"}
	got := escalate(context.Background(), shell, site+"/app")

	if got != shell {
		t.Fatalf("an unreachable browser must leave the static page untouched, got %q", trunc(got.Markdown))
	}
}

func TestEscalateKeepsTheStaticPageWhenTheRenderIsWorse(t *testing.T) {
	// A render CAN come back thinner — a consent wall, a bot check, a login gate.
	// Taking the browser's word for it would make escalation a downgrade, which is
	// the opposite of the point.
	browserAt(t, rendered("Please accept cookies to continue."))

	static := &Page{URL: site + "/x", Markdown: "a short but real summary of the page"}
	got := escalate(context.Background(), static, site+"/x")

	if got != static {
		t.Fatalf("a thinner render must not win, got %q", trunc(got.Markdown))
	}
}

func TestEscalateKeepsTheStaticPageWhenTheRenderFails(t *testing.T) {
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"results": []map[string]any{{"url": site + "/app", "success": false}},
		})
	})

	shell := &Page{URL: site + "/app", Markdown: "Loading…"}
	if got := escalate(context.Background(), shell, site+"/app"); got != shell {
		t.Fatalf("success:false must not replace the static page")
	}
}

func TestEscalateReadsAnEnvelopeLargerThanMaxBody(t *testing.T) {
	// The envelope wraps the page, so it is larger than the document it carries —
	// a heavy article renders to more JSON than maxBody admits of raw HTML.
	// Reading it through the fetch cap truncated the JSON mid-stream, the decode
	// failed, and exactly the pages worth escalating came back as empty successes
	// (usenaive.ai/blog: a 21MB envelope against an 8MiB cap). The envelope gets
	// its own cap; this pins that a render past maxBody arrives whole.
	full := strings.TrimSpace(strings.Repeat("an article heavy enough to overflow the fetch cap. ", maxBody/50))
	browserAt(t, rendered(full))

	shell := &Page{URL: site + "/app", Markdown: "Loading…"}
	got := escalate(context.Background(), shell, site+"/app")

	if len(got.Markdown) != len(full) {
		t.Fatalf("a render larger than maxBody must arrive whole: sent %d bytes, got %d", len(full), len(got.Markdown))
	}
}

// The service is shape-polymorphic about `markdown` across builds: a bare string
// on some, an object with fit_markdown/raw_markdown on others. Decoding only one
// shape means a working browser reads as an empty render on the other.
func TestBrowseDecodesBothMarkdownShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   any
		want string
	}{
		{"bare string", "plain markdown body", "plain markdown body"},
		{"fit preferred over raw", map[string]any{
			"fit_markdown": "the stripped body", "raw_markdown": "boilerplate and the body",
		}, "the stripped body"},
		{"raw when fit is empty", map[string]any{
			"fit_markdown": "", "raw_markdown": "only the raw body",
		}, "only the raw body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			browserAt(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"results": []map[string]any{{"url": site, "success": true, "markdown": tc.md}},
				})
			})
			p, err := browse(context.Background(), site)
			if err != nil {
				t.Fatalf("browse: %v", err)
			}
			if p.Markdown != tc.want {
				t.Errorf("markdown = %q, want %q", p.Markdown, tc.want)
			}
		})
	}
}

func trunc(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// ── escalation must not become an SSRF bypass ────────────────────────────────
//
// A URL whose own host is internal never reaches the browser. What the page
// leads the browser to — a redirect, a frame, a script — is bounded by the
// browser pod's egress policy, not here; a refused static fetch is never
// escalated at all (TestReadDoesNotEscalateARefusal).
//
// The address rule — including a host that answers with one public address beside
// an internal one — is hanzoai/outbound's and is tested there; this pins that the
// escalation asks it.
func TestBrowseRefusesAnInternalTarget(t *testing.T) {
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the browser was asked to fetch an internal address")
	})
	for _, raw := range []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://localhost/",
		// The metadata endpoint spelled as NAT64, which the dialer's old rule admitted.
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
	} {
		if _, err := browse(context.Background(), raw); !errors.Is(err, outbound.ErrRefused) {
			t.Errorf("browse(%q) = %v, want the blocked-address error", raw, err)
		}
	}
}

// Non-HTTP schemes never reach a dialer, so the scheme check is the only place
// they can be refused.
func TestBrowseRefusesNonHTTPSchemes(t *testing.T) {
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the browser was asked to fetch a non-http scheme")
	})
	for _, raw := range []string{"file:///etc/passwd", "gopher://x/", "data:text/html,x"} {
		if _, err := browse(context.Background(), raw); err == nil {
			t.Errorf("browse accepted %q", raw)
		}
	}
}

// ── signing the render (hanzo-inc/cloud#423) ────────────────────────────────

// rendererSees stands in for the renderer, keeps the /crawl body a render sent and
// answers a render good enough to win over a thin page.
func rendererSees(t *testing.T) *map[string]any {
	t.Helper()
	var got map[string]any
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		rendered(strings.Repeat("the rendered article. ", 60))(w, r)
	})
	return &got
}

// sentSignatures reads crawler_config.request_signatures out of a /crawl body.
func sentSignatures(t *testing.T, body map[string]any) map[string]map[string]string {
	t.Helper()
	cfg, ok := body["crawler_config"].(map[string]any)
	if !ok {
		return nil
	}
	raw, _ := json.Marshal(cfg["request_signatures"])
	var out map[string]map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("request_signatures: %v", err)
	}
	return out
}

// TestARenderCarriesASignatureForEachHostItMayStartAt is the point of the field:
// the host asked for and the host the static fetch landed on each get a signature
// a site verifies against the published directory, under the authority the
// renderer will match a navigation to, and not for any other host.
func TestARenderCarriesASignatureForEachHostItMayStartAt(t *testing.T) {
	s := &botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{}}
	botauth.Use(s)
	t.Cleanup(func() { botauth.Use(nil) })
	doc := published(t, s)
	sent := rendererSees(t)

	shell := &Page{URL: "https://Landed.example.com:8443/app", Markdown: "Loading…"}
	escalate(context.Background(), shell, site+"/app")

	got := sentSignatures(t, *sent)
	if len(got) != 2 || got["93.184.216.34"] == nil || got["landed.example.com:8443"] == nil {
		t.Fatalf("want a signature for each of the two authorities, got %v", got)
	}
	verify := func(h map[string]string, url string) error {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		r.Host = r.URL.Host
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return botauth.Verify(r, doc, time.Now())
	}
	if err := verify(got["93.184.216.34"], site+"/app"); err != nil {
		t.Errorf("the asked host's signature does not verify: %v", err)
	}
	if err := verify(got["landed.example.com:8443"], "https://landed.example.com:8443/other"); err != nil {
		t.Errorf("the landed host's signature does not verify: %v", err)
	}
	if err := verify(got["93.184.216.34"], "https://landed.example.com:8443/app"); err == nil {
		t.Error("a signature for one host verified for another")
	}
	for _, h := range got {
		if len(h) != 3 || h["Signature-Agent"] == "" || h["Signature-Input"] == "" || h["Signature"] == "" {
			t.Errorf("a signature is the three headers and nothing else, got %v", h)
		}
	}
}

func TestARenderSignsASharedHostOnceAndAKeysAuthorityIsTheDefaultPortDropped(t *testing.T) {
	s := &botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{}}
	botauth.Use(s)
	t.Cleanup(func() { botauth.Use(nil) })
	published(t, s) // the first directory request mints the key; a fetch never does
	sent := rendererSees(t)

	shell := &Page{URL: site + ":443/landed", Markdown: "Loading…"}
	escalate(context.Background(), shell, site+"/app")

	if got := sentSignatures(t, *sent); len(got) != 1 || got["93.184.216.34"] == nil {
		t.Fatalf("one host is one signature under its authority, got %v", got)
	}
}

// TestARenderAsksForNoSignatureWithoutASigner pins the fallback: a process that
// cannot sign sends the render exactly as it always did.
func TestARenderAsksForNoSignatureWithoutASigner(t *testing.T) {
	botauth.Use(nil)
	sent := rendererSees(t)

	escalate(context.Background(), &Page{URL: site + "/app", Markdown: "Loading…"}, site+"/app")

	if _, ok := (*sent)["crawler_config"]; ok {
		t.Fatalf("a process with no signer must not send crawler_config, got %v", *sent)
	}
	if (*sent)["urls"] == nil {
		t.Fatalf("the render itself was not sent: %v", *sent)
	}
}

// TestARenderStillRunsWhenTheKeysCannotBeRead pins the other half: a signature is
// an identity assertion, never a reason to drop a page.
func TestARenderStillRunsWhenTheKeysCannotBeRead(t *testing.T) {
	botauth.Use(&botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{fail: errors.New("kms is down")}})
	t.Cleanup(func() { botauth.Use(nil) })
	sent := rendererSees(t)

	got := escalate(context.Background(), &Page{URL: site + "/app", Markdown: "Loading…"}, site+"/app")

	if got.Metadata["renderer"] != "browser" {
		t.Fatalf("the render did not run: %v", got.Metadata)
	}
	if _, ok := (*sent)["crawler_config"]; ok {
		t.Fatalf("no key, no signatures: %v", *sent)
	}
}
