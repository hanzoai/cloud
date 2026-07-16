// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0.

package featuregate

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// gateApp mounts Enforce over a seeded registry and a catch-all "ok" handler. The
// injected approval status decides whether the caller is off the waitlist. Returns
// a driver that issues one request (host + path + identity + Accept).
func gateApp(t *testing.T, approvalStatus string) *zip.App {
	t.Helper()
	st := newTestStore(t)
	if _, err := st.Seed(context.Background(), []SeedService{
		{Service: "chat", Hosts: []string{"hanzo.chat"}, WaitlistMode: true},   // gated
		{Service: "api", Hosts: []string{"api.hanzo.ai"}, WaitlistMode: false}, // open
	}, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}
	setModuleStore(st)
	t.Cleanup(func() { setModuleStore(nil) })

	approvals := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
		return approvalStatus, true
	}, time.Minute)

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(Enforce(EnforceConfig{WaitlistURL: "https://waitlist.hanzo.ai", Approvals: approvals}))
	app.Get("/*", func(c *zip.Ctx) error { return c.String(200, "ok") })
	return app
}

type greq struct {
	host, path, user, org, accept string
	admin, approvedHdr, setApprov bool
	authorization                 string // raw Authorization header (e.g. "Bearer hk-…")
	apiKeyHeader                  string // raw api-key header value
}

func drive(t *testing.T, app *zip.App, r greq) (int, string) {
	t.Helper()
	hr := httptest.NewRequest("GET", "http://"+r.host+r.path, nil)
	hr.Host = r.host
	if r.user != "" {
		hr.Header.Set("X-User-Id", r.user)
	}
	if r.org != "" {
		hr.Header.Set("X-Org-Id", r.org)
	}
	if r.admin {
		hr.Header.Set("X-User-IsAdmin", "true")
	}
	if r.setApprov {
		if r.approvedHdr {
			hr.Header.Set("X-User-Approved", "true")
		} else {
			hr.Header.Set("X-User-Approved", "false")
		}
	}
	if r.accept != "" {
		hr.Header.Set("Accept", r.accept)
	}
	if r.authorization != "" {
		hr.Header.Set("Authorization", r.authorization)
	}
	if r.apiKeyHeader != "" {
		hr.Header.Set("api-key", r.apiKeyHeader)
	}
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("Location")
}

const html = "text/html,application/xhtml+xml"

// THE RULE — the acceptance matrix.

func TestRule_PendingUser_BouncedFromGatedHost(t *testing.T) {
	app := gateApp(t, "pending")
	// Browser → 302 to the waitlist.
	code, loc := drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "u", org: "acme", accept: html})
	if code != 302 || loc != "https://waitlist.hanzo.ai" {
		t.Fatalf("pending browser on gated host = %d loc=%q, want 302 → waitlist", code, loc)
	}
	// API (JSON accept) → 403.
	code, _ = drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "u", org: "acme", accept: "application/json"})
	if code != 403 {
		t.Fatalf("pending API on gated host = %d, want 403", code)
	}
}

func TestRule_ApprovedUser_ThroughGatedHost(t *testing.T) {
	app := gateApp(t, "approved")
	code, _ := drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "u", org: "acme", accept: html})
	if code != 200 {
		t.Fatalf("approved user on gated host = %d, want 200 (through)", code)
	}
}

func TestRule_ModeOffService_OpenToPendingUser(t *testing.T) {
	app := gateApp(t, "pending")
	// api.hanzo.ai is mode OFF → even a pending user passes.
	code, _ := drive(t, app, greq{host: "api.hanzo.ai", path: "/v1/chat/completions", user: "u", org: "acme", accept: "application/json"})
	if code != 200 {
		t.Fatalf("pending user on OPEN service = %d, want 200", code)
	}
}

func TestRule_Admin_ThroughGatedHost(t *testing.T) {
	app := gateApp(t, "pending") // status irrelevant — admin short-circuits
	code, _ := drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "z", org: "admin", admin: true, accept: html})
	if code != 200 {
		t.Fatalf("admin on gated host = %d, want 200", code)
	}
}

func TestRule_UnauthenticatedBrowser_BouncedToWaitlist(t *testing.T) {
	app := gateApp(t, "pending")
	code, loc := drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", accept: html})
	if code != 302 || loc != "https://waitlist.hanzo.ai" {
		t.Fatalf("anon browser = %d loc=%q, want 302 → waitlist", code, loc)
	}
	// Anon API → 401 (authenticate first).
	code, _ = drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", accept: "application/json"})
	if code != 401 {
		t.Fatalf("anon API = %d, want 401", code)
	}
}

// MONEY-CRITICAL: a paid inference request with a Hanzo API key MUST flow through
// Enforce even on the seeded waitlist-ON `api` host — it is possession-gated + billed
// downstream, never waitlist-gated. Without the exemption THE RULE would 401 it and
// break inference cluster-wide.
func TestRule_APIKeyInference_NeverGated(t *testing.T) {
	app := gateApp(t, "pending") // api.hanzo.ai is seeded waitlist ON
	for _, key := range []string{"hk-43f50b6b", "sk-hz-abc", "pk-hz-obs", "fw_live_x", "hz_secret"} {
		// The exact paid-inference shape: Bearer key, JSON accept, NO session/user.
		for _, p := range []string{"/v1/chat/completions", "/v1/models", "/v1/embeddings"} {
			code, _ := drive(t, app, greq{
				host: "api.hanzo.ai", path: p, accept: "application/json",
				authorization: "Bearer " + key,
			})
			if code != 200 {
				t.Fatalf("API key %q on %s = %d, want 200 (paid inference must NOT be waitlist-gated)", key, p, code)
			}
		}
	}
	// The api-key / x-api-key header form is exempt too.
	code, _ := drive(t, app, greq{host: "api.hanzo.ai", path: "/v1/chat/completions", accept: "application/json", apiKeyHeader: "hk-headerform"})
	if code != 200 {
		t.Fatalf("api-key header inference = %d, want 200", code)
	}
	// A NON-key Bearer (a JWT-shaped token) from a pending user IS still gated — only
	// key possession is exempt, not arbitrary bearers.
	code, _ = drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "u", org: "acme", accept: "application/json", authorization: "Bearer eyJhbGciOi.jwt.sig"})
	if code != 403 {
		t.Fatalf("pending JWT bearer on gated host = %d, want 403 (only API keys are exempt)", code)
	}
}

func TestRule_UngovernedHost_PassesThrough(t *testing.T) {
	app := gateApp(t, "pending")
	code, _ := drive(t, app, greq{host: "example.com", path: "/whatever", user: "u", org: "acme", accept: html})
	if code != 200 {
		t.Fatalf("un-governed host = %d, want 200 (not ours to gate)", code)
	}
}

func TestRule_ExemptPaths_NeverGated(t *testing.T) {
	app := gateApp(t, "pending")
	for _, p := range []string{"/health", "/v1/iam/get-account", "/v1/waitlist/join", "/v1/featuregate/mode"} {
		code, _ := drive(t, app, greq{host: "hanzo.chat", path: p, user: "u", org: "acme", accept: html})
		if code != 200 {
			t.Fatalf("exempt path %q = %d, want 200 (never gated)", p, code)
		}
	}
}

func TestRule_ForwardHeaderApproved_ThroughWithoutLookup(t *testing.T) {
	// Injected lookup returns pending, but the validated X-User-Approved header
	// (the forward-perfect path) says approved → the user passes with no lookup.
	app := gateApp(t, "pending")
	code, _ := drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "u", org: "acme",
		accept: html, setApprov: true, approvedHdr: true})
	if code != 200 {
		t.Fatalf("X-User-Approved=true on gated host = %d, want 200", code)
	}
}

func TestEnforce_NoStore_IsPassthrough(t *testing.T) {
	setModuleStore(nil)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(Enforce(EnforceConfig{WaitlistURL: "https://waitlist.hanzo.ai",
		Approvals: newApprovalsWithLookup(func(context.Context, string, string) (string, bool) { return "pending", true }, time.Minute)}))
	app.Get("/*", func(c *zip.Ctx) error { return c.String(200, "ok") })
	code, _ := drive(t, app, greq{host: "hanzo.chat", path: "/dashboard", user: "u", org: "acme", accept: html})
	if code != 200 {
		t.Fatalf("no registry mounted = %d, want 200 (never gate pre-boot)", code)
	}
}
