package webui

// The console's bytes come from OUTSIDE this package now (a published site
// release, see webui/release), so this file pins the three properties that
// separation has to hold: a bundle that is not a console is refused before it
// serves, a process with no bundle says so instead of rendering nothing, and a
// bundle that CHANGES under a running handler is picked up whole.

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

// TestBundleWithoutShellIsRefused: a source with no index.html has no SPA shell,
// so every client-side route would 404 and the product would look broken while
// the process looked healthy. Refuse it where the source is named.
func TestBundleWithoutShellIsRefused(t *testing.T) {
	_, err := Handler(fstest.MapFS{"_next/static/chunk.js": {Data: []byte("//")}}, testDoor(), nil)
	if err == nil {
		t.Fatal("Handler accepted a bundle with no index.html — every deep link would 404")
	}
	if !strings.Contains(err.Error(), "index.html") {
		t.Errorf("error = %q, want it to name index.html — the message is the diagnosis", err)
	}
}

// TestNoBundleIsA503_NotAShell: a process that mounted no console (every per-app
// child — cloud.Listen explains why one cannot bootstrap it) still owns the
// terminal handler. It must keep the API namespaces honest and must NOT invent a
// page: an empty 200 of HTML is the one answer a public endpoint may never give.
func TestNoBundleIsA503_NotAShell(t *testing.T) {
	h, err := Handler(nil, testDoor(), nil)
	if err != nil {
		t.Fatalf("Handler(nil): %v — no bundle is a stated case, not an error", err)
	}

	// A console path: honest 503, no HTML body pretending to be the product.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orgs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /orgs = %d, want 503 when this process serves no console", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<!doctype") || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /orgs answered with markup: %q", rec.Body.String())
	}

	// The API namespace answers exactly as before — that rule never depended on
	// there being bytes, and a JSON caller must still never receive HTML.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /v1/does-not-exist = %d, want 404 (API namespace), not the console's answer", rec.Code)
	}
}

// swapFS is a bundle that can be replaced under a running handler — what a
// release swap does (webui/release stores a new snapshot atomically).
type swapFS struct{ cur atomic.Pointer[fstest.MapFS] }

func (s *swapFS) Open(name string) (fs.File, error) { return s.cur.Load().Open(name) }

func (s *swapFS) set(m fstest.MapFS) { s.cur.Store(&m) }

// TestReleaseSwapIsServedImmediately is the defect the cached shell would have
// been. index.html used to be read ONCE at startup, which was free while the
// bundle was baked into the binary. Against a live release it means that after a
// publish the shell still names the PREVIOUS build's chunk hashes — so the
// browser asks for scripts the mounted release does not have, every one of them
// falls through to the SPA shell as HTML, and the console is white with a console
// full of MIME-type errors. The shell must always be the mounted release's.
func TestReleaseSwapIsServedImmediately(t *testing.T) {
	src := &swapFS{}
	src.set(fstest.MapFS{
		"index.html":                      {Data: []byte(`<html><head><title>Hanzo Cloud Console</title></head><body>REL-A</body></html>`)},
		"_next/static/chunks/aaaa1111.js": {Data: []byte("//a")},
	})

	h, err := Handler(src, testDoor(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	get := func(target string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Code, rec.Body.String()
	}

	if _, body := get("/orgs"); !strings.Contains(body, "REL-A") {
		t.Fatalf("GET /orgs before the swap = %q, want the mounted release's shell", body)
	}

	// Publish: a complete new release replaces the old one.
	src.set(fstest.MapFS{
		"index.html":                      {Data: []byte(`<html><head><title>Hanzo Cloud Console</title></head><body>REL-B</body></html>`)},
		"_next/static/chunks/bbbb2222.js": {Data: []byte("//b")},
	})

	if _, body := get("/orgs"); !strings.Contains(body, "REL-B") {
		t.Errorf("GET /orgs after the swap = %q, want the NEW release's shell — a cached shell names dead chunk hashes", body)
	}
	if _, body := get("/"); !strings.Contains(body, "REL-B") {
		t.Errorf("GET / after the swap = %q, want the NEW release's shell", body)
	}
	if code, _ := get("/_next/static/chunks/bbbb2222.js"); code != http.StatusOK {
		t.Errorf("GET the new release's chunk = %d, want 200", code)
	}
}

// polledFS is a source that re-reads on an interval: empty now, filled later.
// swap is the poll landing.
type polledFS struct{ cur atomic.Pointer[fstest.MapFS] }

func (p *polledFS) Polled() bool { return true }
func (p *polledFS) Open(name string) (fs.File, error) {
	m := p.cur.Load()
	if m == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return m.Open(name)
}
func (p *polledFS) swap(m fstest.MapFS) { p.cur.Store(&m) }

// TestAPolledSourceMountsEmptyAndRecovers is the outage this separation cost,
// written down.
//
// A release read from the object store can be empty at boot for a reason that is
// nothing to do with the bundle — S3 briefly unreachable, one auxiliary object
// unreadable. That used to be refused HERE, at mount, exactly as a broken static
// bundle is. The composition root then mounted nothing and skipped the watch
// loop, so the console stayed down after the cause was repaired, because nothing
// was left to re-read it. The repair was already in the object store; no process
// ever looked again.
//
// The distinction this pins: emptiness is a MOMENT for a polled source and a
// VERDICT for a static one. TestBundleWithoutShellIsRefused above pins the other
// half, and the two must stay different.
func TestAPolledSourceMountsEmptyAndRecovers(t *testing.T) {
	src := &polledFS{}

	h, err := Handler(src, testDoor(), nil)
	if err != nil {
		t.Fatalf("a polled source must mount while empty — it is what can still fill in: %v", err)
	}

	// Empty: the honest 503, never a blank page dressed as the product.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("empty source answered %d, want 503", rec.Code)
	}

	// The poll lands — no restart, no re-mount.
	src.swap(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>console</title>")}})

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("after the poll landed the console answered %d, want 200 — recovery must not need a restart", rec.Code)
	}
	// The shell that just arrived, with its <title> rewritten to the request host's
	// brand — which is brandTitle doing its job, not the bundle leaking through.
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("served %q, want the shell that just arrived", rec.Body.String())
	}
}
