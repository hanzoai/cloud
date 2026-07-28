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

// TestRealBundleEmbedded proves the embedded dist is the REAL Tasks SPA, not the
// "No UI build present" placeholder the tasks module ships. This is the whole
// point of cloud owning the embed — if this regresses, tasks.hanzo.ai goes back
// to a blank shell.
func TestRealBundleEmbedded(t *testing.T) {
	root := FS()
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Fatalf("index.html must be embedded: %v", err)
	}
	html := string(data)
	if strings.Contains(html, "No UI build present") {
		t.Fatal("embedded index.html is still the placeholder — dist/ was not synced")
	}
	// The SPA is built with base '/tasks/', which must match the path cloud mounts
	// it on (tasks.go: StripPrefix("/tasks", …) behind /tasks and /tasks/*), or the
	// entrypoint and every hashed chunk resolve to a prefix nothing serves and the
	// page loads as a blank shell.
	if !strings.Contains(html, "/tasks/assets/") {
		t.Errorf("index.html does not reference /tasks/assets/ — wrong base path:\n%s", html)
	}
	// A real Vite build ships hashed JS chunks.
	if _, err := fs.Stat(root, "assets"); err != nil {
		t.Fatalf("assets/ dir must exist in the build: %v", err)
	}
}

// TestHandlerServesIndexAndAssets exercises the SPA-fallback + asset paths the
// same way the browser hits them through StripPrefix("/tasks", …).
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

	// Unknown client route → SPA fallback (index.html), not 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/namespaces/hanzo/workflows", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("deep link = %d, want 200 (SPA fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
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

	// Non-GET is rejected.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}
