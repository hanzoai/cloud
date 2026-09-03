package websearch

// render.go — when an engine answers with nothing, ask it again through a real
// browser.
//
// A search engine served a bot challenge returns 200 with a page that parses to
// zero results. That is not an error and must not become one: another engine may
// have answered, and a request that fails because one engine was unhappy is worse
// than a request with fewer results. But zero is also not an ANSWER, and the
// static fetch has no way to tell the two apart.
//
// A real browser can. It runs the JavaScript the challenge relies on, carries a
// real fingerprint, and gets the page a person would get. We already run one:
// Hanzo Crawl (headless Chromium, ghcr.io/hanzoai/crawl) is deployed in-cluster
// and apps/crawl already escalates to it for client-rendered pages. This is the
// same escalation, for the same reason, against the same service — the search
// path simply never used it.
//
// ESCALATION IS ONE-WAY AND BEST-EFFORT, exactly as in apps/crawl/browser.go: it
// is attempted only when the static fetch produced NOTHING, and if the browser is
// absent, slow or unhappy the zero stands. So this can add results and can never
// remove them, and a deployment without the browser behaves exactly as before.
//
// The RENDERED HTML IS PARSED BY THE ENGINE'S OWN PARSER. There is no second
// extraction path and no second idea of what a result is — parseBing and parseDDG
// are the only readers of an engine's markup, whichever fetch produced it.
//
// WHAT THE BROWSER FIXES, MEASURED. lite.duckduckgo.com, one URL, one second
// apart from the same cluster:
//
//	static   25,672 bytes, 0 results — "Unfortunately, bots use DuckDuckGo too.
//	                                   Select all squares containing a duck."
//	browser  24,410 bytes, 10 results — github.com/firecracker-microvm/firecracker,
//	                                   fly.io/learn/firecracker-vm, wikipedia.
//
// WHAT IT DOES NOT FIX: GOOGLE. Do not add a Google engine here; it was tried
// and it is not viable from this network. Every path returns the /sorry/
// interstitial — ~6KB, 19 captcha markers, "unusual traffic", zero results:
//
//	crawl, headless, stealth on          /sorry/  0 results
//	crawl, headless, &udm=14 and &gbv=1  /sorry/  0 results
//	bot-browser, HEADFUL Chrome 150 on a real X display, second egress IP
//	                                     /sorry/  0 results
//
// The control rules out our technique: that same headful browser reads DDG's ten
// results, and google.com's HOMEPAGE loads normally through it (268KB, title
// "Google", no captcha). Only /search is refused, from two different egress IPs,
// headless and headful alike. That is reputation attached to datacenter
// addresses, not headless detection, so no browser flag reaches it — the fix
// would be residential egress, which is a different decision than a parser.
// Shipping it anyway would mean an engine permanently blind and permanently
// warning: noise where outcome.go is trying to keep a signal.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// crawlURL is the in-cluster Hanzo Crawl service. Same default as
// apps/crawl/browser.go, and the same env, because it is the SAME service — two
// homes for one address is how one of them goes stale.
func crawlURL() string {
	if v := environ.Or("CRAWL_URL", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://crawl.hanzo.svc:11235"
}

// renderTimeout bounds the escalation. A browser render costs seconds where a
// static fetch costs hundreds of milliseconds, and this runs while the caller
// waits, so it is short: past this the zero result stands and the answer is
// whatever the other engines returned.
func renderTimeout() time.Duration {
	if v := environ.Or("WEBSEARCH_RENDER_TIMEOUT", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 12 * time.Second
}

// renderEnabled reports whether escalation may be attempted. NAMED, never
// assumed — WEBSEARCH_RENDER=on turns it on, exactly as WEBSEARCH_ENGINES names
// the engines.
//
// It defaults OFF for a reason found by a test rather than argued from taste.
// apps/answer READS pages through the same Hanzo Crawl service, so search
// escalation and answer's reading share one CRAWL_URL. On by default, a caller
// who stubs crawl for its own reading silently feeds this too:
// TestSSEEmptySourcesStillTerminates points Bing at an empty page to assert the
// no-results path, and search then escalated into that test's crawl stub and
// produced sources the test had gone to trouble to remove.
//
// Nothing there is wrong with the escalation; what is wrong is one process-wide
// endpoint changing a second subsystem's answers without anyone naming it. So it
// is named. Production names it in universe beside WEBSEARCH_ENGINES.
func renderEnabled() bool {
	return strings.EqualFold(environ.Or("WEBSEARCH_RENDER", ""), "on")
}

// renderedResults asks the browser for the engine's page and parses it with that
// engine's own parser. Any failure returns nil, which the caller treats as "the
// static zero stands".
//
// The second return says whether the browser actually RENDERED the page — not
// whether it found anything. The caller stamps it onto a blind answer, and it is
// the difference between two faults that need different people: browsed=false
// means escalation was off or the service was unreachable (configuration);
// browsed=true means a real browser drew the page and our parser still read
// nothing out of it (selector rot). Collapsing them would put the loudest signal
// this package has back into the same bucket as "not switched on".
func renderedResults(ctx context.Context, e engine, query, lang string) ([]webResult, bool) {
	if !renderEnabled() {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, renderTimeout())
	defer cancel()

	// A render holds a browser on the crawl pod, which is the same act — and the
	// same cost — apps/crawl charges for. It reaches that pod by its own route
	// rather than through crawl.browse, so it carries its own gate and its own
	// debit; declaring this surface Metered for the SEARCH fee never covered it.
	// See meter.go.
	ch, err := affordRender(ctx)
	if err != nil {
		return nil, false
	}
	defer ch.Release()

	body, err := renderPage(ctx, e.build(query, lang))
	if err != nil || strings.TrimSpace(body) == "" {
		return nil, false
	}
	chargeRender(ch)
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, false
	}
	return e.parse(root), true
}

// renderPage POSTs one URL to Hanzo Crawl and returns the rendered HTML.
//
// The request and response shapes are Crawl's, not ours, and they are read
// defensively: the service is a separate process on its own release cadence, so a
// field it stops sending must degrade to "no render" rather than to a panic.
func renderPage(ctx context.Context, target string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"urls":         []string{target},
		"cache_mode":   "bypass",
		"screenshot":   false,
		"only_text":    false,
		"page_timeout": int(renderTimeout() / time.Millisecond),
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, crawlURL()+"/crawl", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Required by the service, and the same KMS-sourced token apps/crawl sends —
	// one service, one credential, read from one env.
	if tok := environ.Or("CRAWL_API_TOKEN", ""); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := searchClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("crawl: http %d", resp.StatusCode)
	}
	var out struct {
		Results []struct {
			HTML        string `json:"html"`
			CleanedHTML string `json:"cleaned_html"`
			Success     bool   `json:"success"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return "", err
	}
	for _, r := range out.Results {
		if r.HTML != "" {
			return r.HTML, nil
		}
		if r.CleanedHTML != "" {
			return r.CleanedHTML, nil
		}
	}
	return "", nil
}
