// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// TestBundleIsTheRealSPA proves the embedded bundle is the built client and is
// built for the path this binary serves it on.
//
// The base path is the one thing a build can get wrong SILENTLY: every asset and
// every preload in index.html is absolute, so a bundle built for any other prefix
// loads a shell whose scripts all 404 — a blank page that answers 200. There is
// no runtime signal for it, which is why it is pinned here.
func TestBundleIsTheRealSPA(t *testing.T) {
	root := FS()
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Fatalf("index.html must be embedded: %v", err)
	}
	html := string(data)
	if !strings.Contains(html, "/meet/assets/") {
		t.Errorf("index.html does not reference /meet/assets/ — the bundle was built for another base path:\n%s", html)
	}
	if !strings.Contains(html, `<div id="root">`) {
		t.Error("index.html is not the SPA shell")
	}
	if _, err := fs.Stat(root, "assets"); err != nil {
		t.Fatalf("assets/ must exist in the build: %v", err)
	}
}

// TestTheCallEngineIsBundled proves the client ships LiveKit's own stack rather
// than something hand-rolled around it, and that the ROOM half was built at all.
//
// The room is the whole product and it is also the part that renders only after a
// token: a build that dropped it would still serve a lobby, still answer 200 on
// every path, and still pass every other assertion in this file. So the markers
// here are STRUCTURAL — the peer connection, the screen-share entry point, and
// the room UI's own stylesheet — rather than a package name a minifier is free to
// erase.
func TestTheCallEngineIsBundled(t *testing.T) {
	want := map[string]string{
		".js":  "RTCPeerConnection",   // the WebRTC session itself
		".css": "lk-participant-tile", // the room's grid, styled — i.e. the room was bundled
	}
	found := map[string]bool{}
	_ = fs.WalkDir(FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		for ext, marker := range want {
			if !strings.HasSuffix(p, ext) || found[ext] {
				continue
			}
			b, err := fs.ReadFile(FS(), p)
			if err == nil && strings.Contains(string(b), marker) {
				found[ext] = true
			}
		}
		return nil
	})
	for ext, marker := range want {
		if !found[ext] {
			t.Errorf("no bundled %s carries %q — the call engine is missing from this build", ext, marker)
		}
	}
	// getDisplayMedia is screen share, which the brief calls for by name and which
	// nothing else in this bundle would reach for.
	var share bool
	_ = fs.WalkDir(FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") {
			b, err := fs.ReadFile(FS(), p)
			share = share || (err == nil && strings.Contains(string(b), "getDisplayMedia"))
		}
		return nil
	})
	if !share {
		t.Error("no bundled chunk reaches for getDisplayMedia — screen share is not in this build")
	}
}

// TestHandlerServesTheShellAndItsAssets exercises the paths the browser takes
// through StripPrefix("/meet", …): the shell, a hashed asset, and a room deep
// link — the last of which is the one that matters, because a room URL is the
// thing people paste to each other and a 404 on reload would break every
// invitation.
func TestHandlerServesTheShellAndItsAssets(t *testing.T) {
	h := Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}

	// A room deep link is not a file and must fall back to the shell.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws-uuid/standup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("room deep link = %d, want 200 (SPA fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Error("room deep link did not fall back to the SPA shell")
	}

	var asset string
	_ = fs.WalkDir(FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") && asset == "" {
			asset = p
		}
		return nil
	})
	if asset == "" {
		t.Fatal("no hashed .js asset found in the build")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+asset, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s = %d, want 200", asset, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset cache-control = %q, want immutable", cc)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

// TestSignInStaysOnThisMount pins the two paths the sign-in round trip is made
// of against the prefix this binary serves the bundle on.
//
// Both are absolute paths held as runtime strings, and a runtime string is the
// one thing Vite's `base` cannot reach: it rewrites asset URLs and even the
// root-relative hrefs in index.html, so everything else here tracks the mount by
// construction and these two track it only if someone wrote them that way.
//
// One of them did not, and it took sign-in down. The bundle shipped a bare
// "/login", which under this mount is not this app at all — it is cloud's own
// console sign-in, a different application with its own IAM client and its own
// storage keys. An unauthenticated visitor was handed off to it, it opened the
// OIDC transaction and stored the PKCE verifier under keys it does not share,
// IAM returned the code to /meet/callback, and the gate here had nothing to
// redeem with. Measured in production: "Missing PKCE code verifier", on a screen
// with no way off it.
//
// Nothing about that is visible in the source, where "/login" is a perfectly
// ordinary path — it is wrong only RELATIVE TO THE MOUNT, which the source does
// not know and this package does. So it is pinned on the bytes, like the
// double-bundle below.
//
// (A bare "/login" also appears in the gate's own chunk as the default value of
// a prop this app overrides. It is inert, and it is not what these assertions
// are about: they name the app's own two constants, which carry the prefix or
// the round trip breaks.)
func TestSignInStaysOnThisMount(t *testing.T) {
	var js strings.Builder
	_ = fs.WalkDir(FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return nil
		}
		b, err := fs.ReadFile(FS(), p)
		if err == nil {
			js.Write(b)
		}
		return nil
	})
	bundle := js.String()
	if bundle == "" {
		t.Fatal("no javascript in the build")
	}

	// Where an unauthenticated visitor is sent. Its absence means the bundle is
	// sending them to the ROOT of this host, which this app does not own.
	if !strings.Contains(bundle, "/meet/login") {
		t.Error("the bundle carries no /meet/login — sign-in is being handed to whatever answers /login on this host, which is a different application with a different IAM client, and the callback will have no PKCE verifier to redeem")
	}
	// Where IAM sends them back. This one is also the URI REGISTERED on the
	// hanzo-cloud IAM app, so it is refused outright if it ever stops matching.
	if !strings.Contains(bundle, "/meet/callback") {
		t.Error("the bundle carries no /meet/callback — IAM refuses an authorize request naming any other redirect_uri, so sign-in cannot start at all")
	}
}

// TestTheAuthModuleIsBundledOnce is a regression test for a bug that only exists
// in the SHIPPED artifact, which is why it is asserted here and not in the SPA's
// own suite.
//
// AuthGate keeps "this OIDC code has been redeemed" in MODULE state. Vite
// resolved @hanzogui/admin twice, so the bundle carried two copies of that
// module — two guards, neither aware of the other — and the authorization code
// was redeemed once per copy. The second redemption fails, because a code is
// single-use, and the failure tore down the session the first had just
// established: measured cold, two exchanges on the wire, the token never
// persisted, and the visitor back at the sign-in screen.
//
// Nothing about that is visible in the source. It is a property of how the
// bundler resolved the graph, so the only place it can be caught is the bytes
// that ship. POST_LOGIN_KEY is the module's own private storage key — one copy
// of the module, one occurrence.
func TestTheAuthModuleIsBundledOnce(t *testing.T) {
	const marker = "hanzo_iam_post_login" // AuthGate's module-private storage key
	total := 0
	_ = fs.WalkDir(FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return nil
		}
		b, err := fs.ReadFile(FS(), p)
		if err == nil {
			total += strings.Count(string(b), marker)
		}
		return nil
	})
	if total == 0 {
		t.Fatal("the bundle carries no auth module at all — AuthGate did not ship, so this host cannot sign anyone in")
	}
	if total > 1 {
		t.Errorf("the auth module is bundled %d times — each copy is its own one-code-one-redemption guard, so the OIDC code is redeemed once per copy and the second failure destroys the session the first established. Add the package to resolve.dedupe + optimizeDeps.include in the SPA's vite config.", total)
	}
}
