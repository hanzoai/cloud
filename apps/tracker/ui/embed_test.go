// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package ui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRealBundleEmbedded proves the embedded dist is the REAL Todo SPA and
// that it was built for the path cloud mounts it on. Both halves matter: a
// bundle built with the wrong base resolves every chunk to a prefix nothing
// serves, which is a blank page rather than an error.
func TestRealBundleEmbedded(t *testing.T) {
	root := FS()
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Fatalf("index.html must be embedded: %v", err)
	}
	html := string(data)
	if !strings.Contains(html, "/tracker/assets/") {
		t.Errorf("index.html does not reference /tracker/assets/ — wrong base path:\n%s", html)
	}
	if !strings.Contains(html, "Hanzo Todo") {
		t.Errorf("index.html is not the Todo shell:\n%s", html)
	}
	if _, err := fs.Stat(root, "assets"); err != nil {
		t.Fatalf("assets/ dir must exist in the build: %v", err)
	}
}

// TestBundleCallsTheTrackerAPI pins the SPA's ONE backend. The bundle is built
// with VITE_API_PREFIX=/v1/tracker, which is inlined into the JS at build time
// — so a bundle synced from a build that was pointed somewhere else (a dev
// proxy, another mount prefix) would ship a UI that renders and then talks to
// a surface this binary does not serve. Grep the chunk that holds the client.
func TestBundleCallsTheTrackerAPI(t *testing.T) {
	root := FS()
	var found bool
	err := fs.WalkDir(root, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") || found {
			return nil
		}
		b, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "/v1/tracker") {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk assets: %v", err)
	}
	if !found {
		t.Fatal("no embedded chunk references /v1/tracker — the bundle was built against a different API prefix")
	}
}

// TestHandlerServesIndexAndAssets exercises the SPA-fallback + asset paths the
// same way the browser hits them through StripPrefix("/tracker", …).
func TestHandlerServesIndexAndAssets(t *testing.T) {
	h := Handler()

	// Root → index.html (200, html).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}

	// A deep link the client router owns → SPA fallback, not 404. This is the
	// reload case: /tracker/boards/ENG/timeline is a route only the browser
	// knows, so the server must answer the shell and let it resolve.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boards/ENG/timeline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("deep link = %d, want 200 (SPA fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Errorf("deep link did not fall back to the SPA shell")
	}

	// A real hashed asset → 200 with immutable cache hint.
	var asset string
	_ = fs.WalkDir(FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") && asset == "" {
			asset = p
		}
		return nil
	})
	if asset == "" {
		t.Fatal("no hashed .js asset found in build")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+asset, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s = %d, want 200", asset, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset cache-control = %q, want immutable", cc)
	}

	// Non-GET is rejected: this is a static bundle, not a surface.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

// TestTheBundleTellsNoTales pins what the handler must NOT give away or get
// wrong. Each of these was live: the handler served whatever it could stat, and
// fell back to the SPA shell for everything it could not.
func TestTheBundleTellsNoTales(t *testing.T) {
	h := Handler()
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}

	t.Run("the asset directory is not a listing", func(t *testing.T) {
		// fs.Stat succeeds on a directory and http.FileServer LISTS it, so this
		// published the whole build manifest. A directory is not a page.
		for _, p := range []string{"/assets", "/assets/"} {
			rec := get(p)
			if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "<a href=") {
				t.Errorf("GET %s returned a directory listing:\n%s", p, rec.Body.String())
			}
		}
	})

	t.Run("the provenance stamp is not embedded", func(t *testing.T) {
		// dist/.sync-stamp names the source repository, branch and commit. It is
		// a fact for the repo, not a file to publish — `//go:embed dist` (no
		// `all:`) is what keeps it out of the binary.
		if _, err := fs.Stat(FS(), ".sync-stamp"); err == nil {
			t.Error(".sync-stamp is embedded — it names the source repo/branch/commit")
		}
		if rec := get("/.sync-stamp"); rec.Code == http.StatusOK &&
			strings.Contains(rec.Body.String(), "source=") {
			t.Errorf("GET /.sync-stamp served the stamp:\n%s", rec.Body.String())
		}
	})

	t.Run("a missing asset is 404, not the shell", func(t *testing.T) {
		// A stale index referencing a purged chunk must fail as a miss. Answering
		// index.html hands a <script> tag an HTML document, which surfaces as
		// "Unexpected token '<'" — a corrupt-bundle report for a cache problem.
		rec := get("/assets/index-DOESNOTEXIST.js")
		if rec.Code != http.StatusNotFound {
			t.Errorf("missing asset = %d, want 404 (body: %.80s)", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") &&
			strings.Contains(rec.Body.String(), "<div id=\"root\">") {
			t.Error("missing asset answered the SPA shell")
		}
	})

	t.Run("a client route still falls back to the shell", func(t *testing.T) {
		// The 404 above must be scoped to assets/ — everywhere else the fallback
		// is what makes a deep-link reload work.
		rec := get("/boards/ENG")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Errorf("client route = %d, want the SPA shell", rec.Code)
		}
	})
}
