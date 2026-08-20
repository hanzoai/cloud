package crawl

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// publicDNS makes every hostname answer with one routable address, so these tests
// exercise escalation without depending on live DNS.
func publicDNS(t *testing.T) {
	t.Helper()
	prev := resolve
	resolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	t.Cleanup(func() { resolve = prev })
}

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
	publicDNS(t)
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
				"url": "https://example.com/app", "success": true, "markdown": md,
			}},
		})
	}
}

func TestEscalateReplacesAThinPage(t *testing.T) {
	// Trimmed: browse trims, which is correct — a render's leading/trailing
	// whitespace is not content. The expectation has to match that.
	full := strings.TrimSpace(strings.Repeat("the rendered article. ", 60))
	browserAt(t, rendered(full))

	shell := &Page{URL: "https://example.com/app", Markdown: "Loading…"}
	got := escalate(context.Background(), shell, "https://example.com/app")

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
	p := &Page{URL: "https://example.com/post", Markdown: article}

	if got := escalate(context.Background(), p, "https://example.com/post"); got.Markdown != article {
		t.Fatalf("a complete page was altered")
	}
}

func TestEscalateKeepsTheStaticPageWhenTheBrowserIsUnreachable(t *testing.T) {
	// The state this ships in: crawl not deployed. A crawl that worked before must
	// still work, so an absent browser is a non-event.
	publicDNS(t)
	t.Setenv("CRAWL_URL", "http://127.0.0.1:1")

	shell := &Page{URL: "https://example.com/app", Markdown: "Loading…"}
	got := escalate(context.Background(), shell, "https://example.com/app")

	if got != shell {
		t.Fatalf("an unreachable browser must leave the static page untouched, got %q", trunc(got.Markdown))
	}
}

func TestEscalateKeepsTheStaticPageWhenTheRenderIsWorse(t *testing.T) {
	// A render CAN come back thinner — a consent wall, a bot check, a login gate.
	// Taking the browser's word for it would make escalation a downgrade, which is
	// the opposite of the point.
	browserAt(t, rendered("Please accept cookies to continue."))

	static := &Page{URL: "https://example.com/x", Markdown: "a short but real summary of the page"}
	got := escalate(context.Background(), static, "https://example.com/x")

	if got != static {
		t.Fatalf("a thinner render must not win, got %q", trunc(got.Markdown))
	}
}

func TestEscalateKeepsTheStaticPageWhenTheRenderFails(t *testing.T) {
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"results": []map[string]any{{"url": "https://example.com/app", "success": false}},
		})
	})

	shell := &Page{URL: "https://example.com/app", Markdown: "Loading…"}
	if got := escalate(context.Background(), shell, "https://example.com/app"); got != shell {
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

	shell := &Page{URL: "https://example.com/app", Markdown: "Loading…"}
	got := escalate(context.Background(), shell, "https://example.com/app")

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
					"results": []map[string]any{{"url": "https://e.com", "success": true, "markdown": tc.md}},
				})
			})
			p, err := browse(context.Background(), "https://e.com")
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
// Read escalates when a static Fetch FAILS, and one reason it fails is the
// guarded dialer refusing an internal address. If the escalation did not apply
// the same check, "crawl http://10.0.0.1/" would be refused here and then handed
// to a headless Chromium that fetches it happily — reachable by anyone who can
// call /v1/crawl. The browser has no guard of its own, so this one is the only
// thing standing there.
func TestBrowseRefusesAnInternalTarget(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
		t.Run(ip, func(t *testing.T) {
			prev := resolve
			resolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
			}
			t.Cleanup(func() { resolve = prev })

			browserAt(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("the browser was asked to fetch an internal address (%s)", ip)
			})
			// browserAt reinstalls the public stub, so re-point at the hostile answer.
			resolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
			}

			if _, err := browse(context.Background(), "http://internal.example/"); err == nil {
				t.Fatalf("browse accepted a target resolving to %s", ip)
			}
		})
	}
}

// A host that answers with one public address BESIDE an internal one is the
// documented way past a check that only inspects the first answer.
func TestBrowseRefusesAHostThatAlsoResolvesInternal(t *testing.T) {
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the browser was asked to fetch a host with an internal address")
	})
	resolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("10.1.2.3")},
		}, nil
	}
	if _, err := browse(context.Background(), "http://split.example/"); err == nil {
		t.Fatal("browse accepted a host that also resolves to an internal address")
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
