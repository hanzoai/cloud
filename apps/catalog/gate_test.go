package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The four pages this gate exists because of, verbatim from what the live edge
// served on 2026-07-28 (minus the edge-injected beacon, which is asserted on
// separately below). They are the specification: if any of these is ever
// published again, this test fails before a person has to notice.
const (
	probePage = `<html><head></head><body>x</body></html>`
	probeOK   = `<!doctype html><html><head><title>Probe</title></head><body><h1>probe ok</h1></body></html>`
	stubPage  = `<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><title>Template</title>` +
		`<script src="https://a.hanzo.ai/analytics.js" data-org="hanzo" defer></script></head>` +
		`<body><h1>Template Placeholder</h1><p>This template directory is used for scaffolding.</p></body></html>`
	// The two REAL apps that a naive size rule would have destroyed. Both are
	// well under a kilobyte and both are working sites.
	spaShell = `<!doctype html><html><head><title>Hanzo Team</title>` +
		`<script defer src="bundle.d9cf662c12e9d57334d6.js"></script></head><body></body></html>`
	redirectStub = `<!doctype html><meta http-equiv="refresh" content="0;url=dashboard/projects/">` +
		`<title>Demo</title><script>location.replace("dashboard/projects/")</script>`
)

// acmePage is the mislabeled scaffold: a generic landing page with none of what
// its entry claimed. It is padded well past a kilobyte on purpose — a scaffold
// this gate must catch on WHAT IT SAYS, never on how small it happens to be.
var acmePage = `<!DOCTYPE html><html><head><title>ACME</title></head><body><nav>About Product Pricing</nav>` +
	`<h1>Welcome to Project <span>ACME</span></h1><p>Discover and collaborate on acme.</p>` +
	strings.Repeat("<div class=filler>x</div>", 400) + `</body></html>`

// TestGateHoldsJunk is the whole point: each junk page is held, for the RIGHT
// reason, and each real page is published.
func TestGateHoldsJunk(t *testing.T) {
	serve(t, map[string]string{
		"https://probe.x": probePage,
		"https://ok.x":    probeOK,
		"https://vite.x":  stubPage,
		"https://next.x":  stubPage,
		"https://acme.x":  acmePage,
		"https://team.x":  spaShell,
		"https://prism.x": redirectStub,
		"https://real.x":  strings.Repeat("<p>a real page with real words on it</p>", 60),
		"https://real2.x": strings.Repeat("<p>a different real page, also with words</p>", 60),
		"https://real3.x": strings.Repeat("<p>a third real page, different again</p>", 60),
		"https://gone.x":  "", // unreachable: unread is unjudged
	})
	rows := []Entry{
		{ID: "hanzo/probe", URL: "https://probe.x"},
		{ID: "hanzo/ok", URL: "https://ok.x"},
		{ID: "hanzo/next", URL: "https://next.x"},
		{ID: "hanzo/vite", URL: "https://vite.x"},
		{ID: "hanzo/acme", URL: "https://acme.x"},
		{ID: "hanzo/team", URL: "https://team.x"},
		{ID: "hanzo/prism", URL: "https://prism.x"},
		{ID: "hanzo/real", URL: "https://real.x"},
		{ID: "hanzo/real2", URL: "https://real2.x"},
		{ID: "hanzo/real3", URL: "https://real3.x"},
		{ID: "hanzo/gone", URL: "https://gone.x"},
	}
	pub, held := admit(context.Background(), rows)

	want := map[string]string{
		"hanzo/probe": whyThin,
		"hanzo/ok":    whyThin,
		"hanzo/vite":  whyPlaceholder,
		"hanzo/next":  whyPlaceholder,
		"hanzo/acme":  whyPlaceholder,
	}
	got := map[string]string{}
	for _, e := range held {
		got[e.ID] = e.Note
	}
	for id, why := range want {
		if got[id] != why {
			t.Errorf("%s: held reason %q, want %q", id, got[id], why)
		}
	}
	if len(held) != len(want) {
		t.Errorf("held %d rows, want %d: %v", len(held), len(want), got)
	}
	for _, id := range []string{"hanzo/team", "hanzo/prism", "hanzo/real", "hanzo/real2", "hanzo/real3", "hanzo/gone"} {
		if !published(pub, id) {
			t.Errorf("%s is a real page (or unreadable) and must stay published", id)
		}
	}
	for _, e := range pub {
		if e.Note != "" {
			t.Errorf("a published row must carry no held-reason: %s: %q", e.ID, e.Note)
		}
	}
}

// TestGateHoldsTheSecondCopy pins duplicate handling: one app, one entry — and
// the SAME entry survives every pass, or the catalog would rename its own winner
// on every sync.
func TestGateHoldsTheSecondCopy(t *testing.T) {
	page := strings.Repeat("<p>the very same application, entered twice</p>", 60)
	serve(t, map[string]string{"https://a.x": page, "https://z.x": page})
	rows := []Entry{{ID: "hanzo/zeta", URL: "https://z.x"}, {ID: "hanzo/alpha", URL: "https://a.x"}}

	for pass := range 3 {
		pub, held := admit(context.Background(), rows)
		if len(pub) != 1 || pub[0].ID != "hanzo/alpha" {
			t.Fatalf("pass %d: published %+v, want only hanzo/alpha", pass, pub)
		}
		if len(held) != 1 || held[0].ID != "hanzo/zeta" || held[0].Note != whyDuplicate+"hanzo/alpha" {
			t.Fatalf("pass %d: held %+v, want hanzo/zeta naming its twin", pass, held)
		}
		rows[0], rows[1] = rows[1], rows[0] // store order must not decide the winner
	}
}

// TestGateFailsOpen is the property that keeps this gate from ever being the
// thing that empties the catalog. An unreadable page is unjudged, and a pass that
// would hold MOST of the corpus has diagnosed the reader, not the sites.
func TestGateFailsOpen(t *testing.T) {
	t.Run("unreadable is admitted", func(t *testing.T) {
		serve(t, nil) // every fetch errors
		pub, held := admit(context.Background(), []Entry{{ID: "hanzo/a", URL: "https://a.x"}})
		if len(pub) != 1 || len(held) != 0 {
			t.Fatalf("pub=%+v held=%+v; an unread page must be admitted", pub, held)
		}
	})
	t.Run("a mostly-junk verdict is not acted on", func(t *testing.T) {
		serve(t, map[string]string{
			"https://a.x": probePage, "https://b.x": probePage2, "https://c.x": probeOK,
			"https://d.x": strings.Repeat("<p>the one real page here</p>", 60),
		})
		pub, held := admit(context.Background(), []Entry{
			{ID: "hanzo/a", URL: "https://a.x"}, {ID: "hanzo/b", URL: "https://b.x"},
			{ID: "hanzo/c", URL: "https://c.x"}, {ID: "hanzo/d", URL: "https://d.x"},
		})
		if len(pub) != 4 || len(held) != 0 {
			t.Fatalf("pub=%d held=%d; 3-of-4 held means the READER is broken", len(pub), len(held))
		}
		for _, e := range pub {
			if e.Note != "" {
				t.Errorf("%s: a row nobody acted on must carry no reason: %q", e.ID, e.Note)
			}
		}
	})
}

// probePage2 differs from probePage so the mostly-junk case is not ALSO a
// duplicate case — the guard under test must fire on thinness alone.
const probePage2 = `<html><head></head><body>y</body></html>`

// TestInjectedScriptsDoNotMakeAPageAlive is the trap the gate has to survive in
// production and nowhere else: every page we serve gets our analytics tag and the
// edge's beacon stapled on. If those counted as "the page loads something", every
// probe on the fleet would look like a working app.
func TestInjectedScriptsDoNotMakeAPageAlive(t *testing.T) {
	const beacon = `<script type="module" src="https://static.cloudflareinsights.com/beacon.min.js/v451" ` +
		`integrity="sha512-ZE9" data-cf-beacon='{"version":"2024.11.0"}' crossorigin="anonymous"></script>`
	const ours = `<script src="https://a.hanzo.ai/analytics.js" data-org="hanzo" defer></script>` +
		`<script src="https://a.hanzo.ai/chat.js" data-org="hanzo" data-mode="site" defer></script>`
	if !inert([]byte(probePage + ours + beacon)) {
		t.Error("a probe wrapped in OUR injections is still a probe")
	}
	if inert([]byte(spaShell)) {
		t.Error("a relative bundle is the site's own code: the page is not inert")
	}
	if inert([]byte(redirectStub)) {
		t.Error("a page that redirects goes somewhere: it is not inert")
	}
	if inert([]byte(`<html><body><iframe src="https://demo.example"></iframe></body></html>`)) {
		t.Error("a page that frames an app is not inert")
	}
	if inert([]byte(`<html><head><link rel=stylesheet href="app.css"></head><body>hi</body></html>`)) {
		t.Error("a relative stylesheet is the site's own: the page is not inert")
	}
}

// TestGateReadsWhatAVisitorReads pins the text reduction the scaffold list is
// matched against: markup, scripts and whitespace must not be able to hide the
// placeholder copy.
func TestGateReadsWhatAVisitorReads(t *testing.T) {
	if got := text([]byte("<h1>Template\n  <b>Placeholder</b></h1><script>var x='nothing here'</script>")); got != "template placeholder" {
		t.Errorf("text() = %q, want %q", got, "template placeholder")
	}
	if !scaffolded([]byte(acmePage)) {
		t.Error("a placeholder split across a <span> must still be recognised")
	}
	if scaffolded([]byte("<p>Our template gallery is a placeholder-free zone</p>")) {
		t.Error("prose that merely contains the words is not a scaffold page")
	}
}

// serve swaps the body seam for the duration of one test: a url in the map is
// served, anything else fails to load. An empty body is a failed load too, which
// is how a test says "unreachable".
func serve(t *testing.T, pages map[string]string) {
	t.Helper()
	prev := fetchBody
	fetchBody = func(_ context.Context, url string) ([]byte, error) {
		if b := pages[url]; b != "" {
			return []byte(b), nil
		}
		return nil, errors.New("catalog: unreachable")
	}
	t.Cleanup(func() { fetchBody = prev })
}

func published(rows []Entry, id string) bool {
	for _, e := range rows {
		if e.ID == id {
			return true
		}
	}
	return false
}
