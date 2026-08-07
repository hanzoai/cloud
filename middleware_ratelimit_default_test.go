package cloud

// The static rate floor, asserted where it bites.
//
// The property under test is the one that was missing: an org nobody configured
// is still bounded on a service class that names a default. "No rule configured"
// used to mean "no limit", which on an unpriced service means a free denial of
// service — nothing downstream ever says stop.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// floorApp wires an app with ONLY the scope rate limiter — no commerce, no
// gateway policy store. That is the UNCONFIGURED deployment, which is the whole
// point: the floor has to bind without an operator having done anything.
func floorApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(ScopeRateLimit(nil, nil))
	app.Post("/v1/audio/transcriptions", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "true"}) })
	app.Post("/v1/chat/completions", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "true"}) })
	return app
}

// floorReq sends one request as a validated principal of org, the identity
// ScopeRateLimit keys on (the gateway mints these headers).
func floorReq(t *testing.T, app *zip.App, org, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return resp
}

// audioRPM is the floor under test, read from the table so the test cannot
// drift from the value that ships.
func audioRPM(t *testing.T) int {
	t.Helper()
	rpm := defaultServiceRPM["audio"]
	if rpm <= 0 {
		t.Fatal(`defaultServiceRPM["audio"] is not positive — the speech endpoints have no floor`)
	}
	return rpm
}

// TestAudioHasNonZeroDefault states the invariant directly: the speech endpoints
// carry a real limit without an operator configuring one.
func TestAudioHasNonZeroDefault(t *testing.T) {
	if got := audioRPM(t); got <= 0 {
		t.Fatalf(`defaultServiceRPM["audio"] = %d, want > 0`, got)
	}
	// canonicalService must actually derive "audio" from the shipped paths, or
	// the table entry is dead weight that looks like protection.
	for _, p := range []string{"/v1/audio/transcriptions", "/v1/audio/speech"} {
		if svc := canonicalService(p); svc != "audio" {
			t.Errorf("canonicalService(%q) = %q, want \"audio\" — the default would never bind", p, svc)
		}
	}
}

// TestDefaultRPMBindsUnconfigured drives the middleware with NO commerce and NO
// gateway policy — the unconfigured deployment — and proves the floor throttles
// /v1/audio/* anyway.
func TestDefaultRPMBindsUnconfigured(t *testing.T) {
	rpm := audioRPM(t)
	app := floorApp(t)

	for i := 0; i < rpm; i++ {
		if resp := floorReq(t, app, "acme", "/v1/audio/transcriptions"); resp.StatusCode == 429 {
			t.Fatalf("request %d of %d was throttled early — the floor is tighter than it states", i+1, rpm)
		}
	}
	if resp := floorReq(t, app, "acme", "/v1/audio/transcriptions"); resp.StatusCode != 429 {
		t.Fatalf("request %d returned %d, want 429 — an unconfigured org is UNBOUNDED on the speech endpoints",
			rpm+1, resp.StatusCode)
	}
}

// TestDefaultRPMIsPerOrg proves the floor buckets by org: one org exhausting it
// cannot throttle another. A shared bucket would turn the fix into the outage.
func TestDefaultRPMIsPerOrg(t *testing.T) {
	rpm := audioRPM(t)
	app := floorApp(t)

	for i := 0; i <= rpm; i++ {
		floorReq(t, app, "acme", "/v1/audio/transcriptions")
	}
	if resp := floorReq(t, app, "acme", "/v1/audio/transcriptions"); resp.StatusCode != 429 {
		t.Fatalf("org acme was not throttled after %d requests", rpm+1)
	}
	if resp := floorReq(t, app, "other", "/v1/audio/transcriptions"); resp.StatusCode == 429 {
		t.Fatal("org other was throttled by org acme's usage — the floor shares a bucket across orgs")
	}
}

// TestDefaultRPMLeavesOtherClassesAlone proves the blast radius: a class with no
// entry keeps today's behaviour exactly, so this change cannot throttle traffic
// it was not aimed at.
func TestDefaultRPMLeavesOtherClassesAlone(t *testing.T) {
	rpm := audioRPM(t)
	app := floorApp(t)

	for i := 0; i < rpm+10; i++ {
		if resp := floorReq(t, app, "acme", "/v1/chat/completions"); resp.StatusCode == 429 {
			t.Fatalf("an unlisted class was throttled at request %d; the default is not narrow", i+1)
		}
	}
}
