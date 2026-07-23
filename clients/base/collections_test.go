// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// TestAllowCollections pins the least-privilege boundary — the twin of the retired
// console BFF's allowBaseSurface. The data plane the console uses is admitted; every
// non-collection Base admin surface (settings/backups/logs) and any malformed path is
// refused, so /v1/collections can never become a general Base tunnel.
func TestAllowCollections(t *testing.T) {
	ok := []string{
		"/v1/collections",                        // list schemas + create a type
		"/v1/collections/meta/scaffolds",         // field-template palette
		"/v1/collections/tenants",                // one content type
		"/v1/collections/tenants/records",        // a type's records
		"/v1/collections/tenants/records/abc123", // one record
		"v1/collections/submissions/records",     // no leading slash (defensive)
		"/v1/collections/meta/records",           // a collection literally named "meta"
	}
	for _, p := range ok {
		if !allowCollections(p) {
			t.Errorf("allowCollections(%q) = false, want true", p)
		}
	}

	deny := []string{
		"/v1/settings",                        // Base admin — NOT collections
		"/v1/collections/tenants/records/a/b", // over-deep
		"/v1/collections/tenants/logs",        // non-records sub-path
		"/v1/collections//records",            // empty name
		"/v1/backups",                         // Base admin
		"/v1/logs",                            // Base admin
		"/v2/collections",                     // wrong version
		"",                                    // empty
	}
	for _, p := range deny {
		if allowCollections(p) {
			t.Errorf("allowCollections(%q) = true, want false", p)
		}
	}
}

// TestCollectionsProxyDirector proves the director sets the upstream vhost Host and
// forwards the path UNCHANGED (1:1 with base's own /v1/collections/*) — never a
// rewrite, and never leaks the in-cluster Service name as the Host.
func TestCollectionsProxyDirector(t *testing.T) {
	var seenPath, seenHost, seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenHost, seenAuth = r.URL.Path, r.Host, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy, err := newCollectionsProxy(upstream.URL, "base.hanzo.ai")
	if err != nil {
		t.Fatalf("newCollectionsProxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/tenants/records", nil)
	req.Header.Set("Authorization", "Bearer test.jwt")
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	if seenPath != "/v1/collections/tenants/records" {
		t.Errorf("upstream path = %q, want unchanged /v1/collections/tenants/records", seenPath)
	}
	if seenHost != "base.hanzo.ai" {
		t.Errorf("upstream Host = %q, want base.hanzo.ai (the managed Base vhost)", seenHost)
	}
	if seenAuth != "Bearer test.jwt" {
		t.Errorf("upstream Authorization = %q, want the caller's Bearer forwarded", seenAuth)
	}
}

// TestServeCollectionsGate proves the principal gate: a bearer-less/forged call
// (no X-User-Id — the ONE signal SanitizeIdentity mints from a verified credential)
// is refused 403 BEFORE it can reach the managed Base; a validated caller with an
// allow-listed path is forwarded.
func TestServeCollectionsGate(t *testing.T) {
	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	proxy, err := newCollectionsProxy(upstream.URL, "base.hanzo.ai")
	if err != nil {
		t.Fatalf("newCollectionsProxy: %v", err)
	}

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.All("/v1/collections/*", func(c *zip.Ctx) error { return serveCollections(proxy, c) })

	// No principal → 403, upstream never reached.
	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/collections/tenants/records", nil), fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("app.Test (anon): %v", err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("anon status = %d, want 403", res.StatusCode)
	}
	if reached {
		t.Error("anon call reached the managed Base — the principal gate is not fail-closed")
	}

	// Validated principal (X-User-Id set) + allow-listed path → forwarded.
	req := httptest.NewRequest(http.MethodGet, "/v1/collections/tenants/records", nil)
	req.Header.Set("X-User-Id", "u-123")
	res, err = app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("app.Test (authed): %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("authed status = %d, want 200 (forwarded)", res.StatusCode)
	}
	if !reached {
		t.Error("validated call did not reach the managed Base")
	}
}
