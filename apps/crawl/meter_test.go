package crawl

// A render is the only act on this surface that costs anything, so these are the
// three facts worth pinning: rendering a page debits the CALLER's org, fetching
// one does not, and a caller who cannot pay for a render keeps the page they
// already had instead of losing the crawl.
//
// The last one is the property that makes metering this surface safe to ship.
// Escalation has always been best-effort, and a balance is one more way for it
// not to happen — never a way for a page we already hold to become an error.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// meterOn binds the process-wide meter at a ledger for one test.
func meterOn(t *testing.T, l *planetest.Ledger) {
	t.Helper()
	t.Setenv("CLOUD_ENV", "mainnet")
	bindMeter(cloud.NewMeter(cloud.Deps{Metering: l.Client(t)}, "crawl"))
	t.Cleanup(func() { bindMeter(nil) })
}

// browserCounting is browserAt with a call count, which is the fact a gate has to
// be judged on: a refused render must not merely go unbilled, it must not happen.
func browserCounting(t *testing.T, md string) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		rendered(md)(w, r)
	})
	return &n
}

// renderAs escalates one page as org through a REAL request, so the payer is
// resolved the way production resolves it: cloud.Bridge parks the validated
// caller and the meter reads it back off the context.
func renderAs(t *testing.T, org string, static *Page, url string) *Page {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var out *Page
	app.Post("/probe", func(c *zip.Ctx) error {
		out = escalate(c.Context(), static, url)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // a validated principal
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return out
}

const page = "https://example.com/app"

func article() string { return strings.TrimSpace(strings.Repeat("the rendered article. ", 60)) }

// A rendered page debits the caller's own org, once, at the declared fee.
func TestRenderDebitsTheCaller(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	hits := browserCounting(t, article())

	got := renderAs(t, "acme", &Page{URL: page, Markdown: "Loading…"}, page)
	if got.Markdown != article() {
		t.Fatalf("the thin page was not replaced, so no render happened to bill for")
	}
	if hits.Load() != 1 {
		t.Fatalf("browser called %d times, want 1", hits.Load())
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a render must bill", l.Count())
	}
	if org := l.Org(); org != "acme" {
		t.Fatalf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	_, cents, model, _ := l.Charged()
	if cents != defaultFeeCents {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, defaultFeeCents)
	}
	if model != kind {
		t.Errorf("debit unit = %q, want %q", model, kind)
	}
}

// Fetching is free. A page that came back whole never reaches the browser, so
// there is nothing to gate and nothing to bill — the ordinary crawl is unchanged.
func TestFetchedPageIsFree(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	hits := browserCounting(t, article())

	full := &Page{URL: page, Markdown: article()}
	if got := renderAs(t, "acme", full, page); got.Markdown != full.Markdown {
		t.Fatal("a page that was already whole was replaced")
	}
	if hits.Load() != 0 {
		t.Fatalf("browser called %d times for a page that did not need it, want 0", hits.Load())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a static fetch, want 0 — reading a page is free", n)
	}
}

// An org that cannot pay keeps the page it already has. The browser is never
// asked, so the refusal costs nothing, and the crawl still answers.
func TestUnfundedOrgKeepsTheStaticPage(t *testing.T) {
	l := planetest.Money(t, 0)
	meterOn(t, l)
	hits := browserCounting(t, article())

	shell := &Page{URL: page, Markdown: "Loading…"}
	got := renderAs(t, "acme", shell, page)
	if got != shell {
		t.Fatalf("an unfunded caller lost the static page; only the render may drop")
	}
	if hits.Load() != 0 {
		t.Fatalf("browser called %d times for a caller who could not pay, want 0 "+
			"(the gate must precede the render, not merely skip the debit)", hits.Load())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused render, want 0", n)
	}
}

// A caller with no principal — the shared-service-key entry point — has no
// ledger, so nothing is gated and nothing is billed, and the render still
// happens.
func TestServiceCallerRendersUnbilled(t *testing.T) {
	l := planetest.Money(t, 0) // an empty balance that must not be consulted at all
	meterOn(t, l)
	hits := browserCounting(t, article())

	if got := renderAs(t, "", &Page{URL: page, Markdown: "Loading…"}, page); got.Markdown != article() {
		t.Fatal("a service caller lost the render")
	}
	if hits.Load() != 1 {
		t.Fatalf("browser called %d times, want 1", hits.Load())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d with no principal to bill, want 0", n)
	}
}
