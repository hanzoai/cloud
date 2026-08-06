// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// These are the tests for the POLICY. Three app UIs depend on it (tasks,
// research, meet) and each of their suites tests it through its own committed
// bundle — which means each proves the policy on the one tree that happens to be
// checked in, and none of them can build the tree that would break it.
//
// So the trees here are synthetic and deliberately hostile: a bundle that never
// synced, a chunk that is not there, a path that climbs out. A shared handler's
// failure modes belong beside the handler, not distributed across three consumers
// who would each have to imagine them.
package spa

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// site is a small built bundle: a shell, a hashed asset, and a file outside
// assets/ that must NOT get the immutable hint.
func site() fs.FS {
	return fstest.MapFS{
		"index.html":            {Data: []byte(`<!doctype html><div id="root"></div>`)},
		"assets/app-abc123.js":  {Data: []byte(`console.log(1)`)},
		"assets/app-abc123.css": {Data: []byte(`body{}`)},
		"favicon.svg":           {Data: []byte(`<svg/>`)},
		"robots.txt":            {Data: []byte("User-agent: *\n")},
	}
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestTheShellIsServedAndIsNotCached. A deployed build replaces the shell, and a
// cached shell points at hashed chunks that no longer exist — a blank page that
// answers 200 and heals only when someone hard-reloads.
func TestTheShellIsServedAndIsNotCached(t *testing.T) {
	h := Handler(site(), "demo")
	w := get(h, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET / cache-control = %q, want no-cache", cc)
	}
}

// TestTheExplicitIndexRedirectsToTheRoot records a behavior that comes from
// net/http rather than from us, because it is surprising and because a future
// reader will otherwise read it as a bug: http.FileServer canonicalises
// "/index.html" to "./" with a 301, so the explicit path never serves bytes.
//
// It is harmless — the browser follows to "/" and gets the shell with the same
// no-cache hint — and it is left alone rather than special-cased, because
// intercepting it would mean this handler carrying its own opinion about a
// filename that only the file server knows is special. Pinned so that if a Go
// release changes it, the change is seen HERE and not in three app suites.
func TestTheExplicitIndexRedirectsToTheRoot(t *testing.T) {
	w := get(Handler(site(), "demo"), "/index.html")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /index.html = %d, want 301 (FileServer canonicalisation)", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "./" {
		t.Errorf("GET /index.html Location = %q, want ./", loc)
	}
}

// TestOnlyHashedAssetsAreImmutable. The immutable hint is a PROMISE that the bytes
// at a name never change, and Vite only earns it under assets/ where the name
// carries a content hash. Handing it to anything else pins a stale file in every
// browser and proxy for a year with no way to revoke it.
func TestOnlyHashedAssetsAreImmutable(t *testing.T) {
	h := Handler(site(), "demo")

	for _, path := range []string{"/assets/app-abc123.js", "/assets/app-abc123.css"} {
		w := get(h, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("GET %s cache-control = %q, want immutable", path, cc)
		}
	}
	// Real files that are NOT content-addressed.
	for _, path := range []string{"/favicon.svg", "/robots.txt"} {
		w := get(h, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
			t.Errorf("GET %s cache-control = %q — only hashed assets may be immutable", path, cc)
		}
	}
}

// TestADeepLinkFallsBackToTheShell. This is the whole reason a client-side router
// works on a reload, and for meet it is what makes a pasted room URL a real URL.
func TestADeepLinkFallsBackToTheShell(t *testing.T) {
	h := Handler(site(), "demo")
	for _, path := range []string{
		"/namespaces/hanzo/workflows",
		"/11111111-1111-4111-8111-111111111111/standup",
		"/anything/at/all",
	} {
		w := get(h, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (SPA fallback)", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), `<div id="root">`) {
			t.Errorf("GET %s did not fall back to the shell", path)
		}
	}
}

// TestAMissingChunkIs404AndNotTheShell is the ONE place the fallback is narrowed,
// and assets/ is the only subtree where it CAN be: Vite hashes every filename
// under it, so nothing there is ever a client-side route and a name that is not
// present is a stale shell asking for a purged chunk.
//
// Answering the shell there hands a <script> tag an HTML document — "Unexpected
// token '<'" — which reads as a corrupt bundle rather than the cache miss it is.
// A 404 names what actually happened.
func TestAMissingChunkIs404AndNotTheShell(t *testing.T) {
	w := get(Handler(site(), "demo"), "/assets/app-deadbeef.js")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET a missing chunk = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), `<div id="root">`) {
		t.Error("a missing chunk was answered with the shell")
	}
}

// TestADirectoryIsNotAPage. http.FileServer renders a directory as an index
// listing, so serving whatever stat succeeds on publishes the entire asset
// manifest at /<app>/assets/ — the bundle's contents should stay something a
// caller has to already know the name of.
func TestADirectoryIsNotAPage(t *testing.T) {
	w := get(Handler(site(), "demo"), "/assets")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET the assets directory = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "app-") {
		t.Errorf("the asset manifest was listed: %q", w.Body.String())
	}
}

// TestAnUnsyncedBundleIs503AndNamesItself. A build that failed to sync must be
// LOUD in staging, never a blank 200 in production — and the body has to name
// which app's bundle is missing, or an operator reading a failed deploy learns
// only that "the UI" is broken.
func TestAnUnsyncedBundleIs503AndNamesItself(t *testing.T) {
	h := Handler(fstest.MapFS{}, "meet")
	for _, path := range []string{"/", "/deep/link", "/assets/x.js"} {
		w := get(h, path)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s on an empty bundle = %d, want 503", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "meet") {
			t.Errorf("GET %s body does not name the app: %q", path, w.Body.String())
		}
	}
}

// TestOnlyReadsAreServed. A static bundle has no writes, so anything else is a
// client bug or a probe, and either way it is answered once, here, rather than by
// a file server that would happily serve a body to a POST.
func TestOnlyReadsAreServed(t *testing.T) {
	h := Handler(site(), "demo")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s / = %d, want 405", method, w.Code)
		}
	}
	// HEAD is a read and must work — probes and preflight-ish clients use it.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("HEAD / = %d, want 200", w.Code)
	}
}

// TestNothingEscapesTheBundle. The handler is mounted under StripPrefix, so the
// path it sees is attacker-shaped. path.Clean resolves the traversals before
// anything is looked up, and fs.FS rejects what is left — but "it is safe because
// of two libraries" is exactly the claim that should have a test.
//
// Every one of these must end at the shell or a file INSIDE the bundle. None may
// read the filesystem, and none may 500.
func TestNothingEscapesTheBundle(t *testing.T) {
	h := Handler(site(), "demo")
	for _, path := range []string{
		"/../../etc/passwd",
		"/..%2f..%2fetc/passwd",
		"/assets/../../../etc/passwd",
		"/./././../secret",
		"//etc/passwd",
		"/assets/..%00/index.html",
		"/%2e%2e/%2e%2e/etc/passwd",
	} {
		w := get(h, path)
		if w.Code != http.StatusOK {
			// A refusal is fine; a 500 is not, and neither is a body.
			if w.Code == http.StatusInternalServerError {
				t.Errorf("GET %s = 500 — a hostile path must not fault the handler", path)
			}
			continue
		}
		body := w.Body.String()
		if strings.Contains(body, "root:") || strings.Contains(body, "/bin/") {
			t.Fatalf("SECURITY: GET %s escaped the bundle:\n%s", path, body)
		}
		if !strings.Contains(body, `<div id="root">`) && !strings.Contains(body, "console.log") &&
			!strings.Contains(body, "svg") && !strings.Contains(body, "User-agent") && !strings.Contains(body, "body{}") {
			t.Errorf("GET %s served something that is not in the bundle:\n%s", path, body)
		}
	}
}

// TestSubRootsTheBundle. Every ui package spells `fs.Sub(embedded, "dist")`, and
// getting it wrong serves the WRAPPER — so index.html is at dist/index.html, every
// path is off by one segment, and the shell 503s while the files are all present.
func TestSubRootsTheBundle(t *testing.T) {
	wrapped := fstest.MapFS{
		"dist/index.html":           {Data: []byte(`<!doctype html><div id="root"></div>`)},
		"dist/assets/app-abc123.js": {Data: []byte(`console.log(1)`)},
	}
	root := Sub(wrapped)
	if _, err := fs.Stat(root, "index.html"); err != nil {
		t.Fatalf("Sub did not root the bundle at dist/: %v", err)
	}
	if w := get(Handler(root, "demo"), "/"); w.Code != http.StatusOK {
		t.Fatalf("GET / through a Sub'd bundle = %d, want 200", w.Code)
	}
	// A bundle with no dist/ is returned as-is rather than as an error a caller
	// cannot act on — an embed.FS is validated at compile time, so this shape only
	// exists in a test.
	if Sub(fstest.MapFS{"index.html": {Data: []byte("x")}}) == nil {
		t.Error("Sub returned nil for a bundle with no dist/")
	}
}
