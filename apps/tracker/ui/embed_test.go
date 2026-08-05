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

// TestRealBundleEmbedded proves the embedded dist is the REAL Tracker SPA and
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
	if !strings.Contains(html, "Hanzo Tracker") {
		t.Errorf("index.html is not the tracker shell:\n%s", html)
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
