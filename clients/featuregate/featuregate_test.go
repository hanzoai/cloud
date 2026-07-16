// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0.

package featuregate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func newControlPlane(t *testing.T) (*zip.App, *Store) {
	t.Helper()
	st := newTestStore(t)
	if _, err := st.Seed(context.Background(), []SeedService{
		{Service: "chat", DisplayName: "Chat", Hosts: []string{"hanzo.chat"}, WaitlistMode: true},
		{Service: "api", DisplayName: "API", Hosts: []string{"api.hanzo.ai"}, WaitlistMode: true},
	}, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &svc{store: st, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/admin/services", s.guard(s.listServices))
	app.Post("/v1/admin/services", s.guard(s.upsertService))
	app.Post("/v1/admin/services/:service/mode", s.guard(s.setMode))
	app.Get("/v1/featuregate/mode", s.modeForHost)
	return app, st
}

func call(t *testing.T, app *zip.App, method, path string, admin bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if admin {
		hr.Header.Set("X-User-Id", "z")
		hr.Header.Set("X-Org-Id", "admin")
		hr.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestControlPlane_ListRequiresGlobalAdmin(t *testing.T) {
	app, _ := newControlPlane(t)
	// Non-admin → 403.
	if code, _ := call(t, app, "GET", "/v1/admin/services", false, nil); code != 403 {
		t.Fatalf("non-admin list = %d, want 403", code)
	}
	// Admin → 200 with both services.
	code, body := call(t, app, "GET", "/v1/admin/services", true, nil)
	if code != 200 {
		t.Fatalf("admin list = %d, want 200", code)
	}
	var env struct {
		Data struct {
			Services []Service `json:"services"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(env.Data.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(env.Data.Services))
	}
}

func TestControlPlane_ToggleTakesEffectImmediately(t *testing.T) {
	app, _ := newControlPlane(t)

	// Before: hanzo.chat reads mode ON.
	code, body := call(t, app, "GET", "/v1/featuregate/mode?host=hanzo.chat", false, nil)
	if code != 200 {
		t.Fatalf("mode read = %d", code)
	}
	if got := modeBit(t, body); !got {
		t.Fatalf("before toggle waitlistMode=%v, want true", got)
	}

	// Admin opens chat (mode OFF).
	if code, _ := call(t, app, "POST", "/v1/admin/services/chat/mode", true, modeRequest{WaitlistMode: false}); code != 200 {
		t.Fatalf("toggle = %d, want 200", code)
	}

	// After: the SAME mode read reflects it immediately (no redeploy, no cache).
	_, body = call(t, app, "GET", "/v1/featuregate/mode?host=hanzo.chat", false, nil)
	if got := modeBit(t, body); got {
		t.Fatalf("after toggle waitlistMode=%v, want false", got)
	}

	// A non-admin cannot toggle.
	if code, _ := call(t, app, "POST", "/v1/admin/services/api/mode", false, modeRequest{WaitlistMode: false}); code != 403 {
		t.Fatalf("non-admin toggle = %d, want 403", code)
	}
	// Unknown service → 404.
	if code, _ := call(t, app, "POST", "/v1/admin/services/nope/mode", true, modeRequest{WaitlistMode: false}); code != 404 {
		t.Fatalf("toggle unknown = %d, want 404", code)
	}
}

func TestControlPlane_UpsertOnboardsHost(t *testing.T) {
	app, _ := newControlPlane(t)
	// Onboard a new gated service at runtime.
	code, _ := call(t, app, "POST", "/v1/admin/services", true, upsertRequest{
		Service: "studio", DisplayName: "Studio", Hosts: []string{"studio.hanzo.ai"}, WaitlistMode: true,
	})
	if code != 200 {
		t.Fatalf("upsert = %d, want 200", code)
	}
	// Its host now resolves to mode ON.
	_, body := call(t, app, "GET", "/v1/featuregate/mode?host=studio.hanzo.ai", false, nil)
	if !modeBit(t, body) {
		t.Fatalf("onboarded host not gated")
	}
	// Non-admin cannot onboard.
	if code, _ := call(t, app, "POST", "/v1/admin/services", false, upsertRequest{Service: "x", Hosts: []string{"x.hanzo.ai"}}); code != 403 {
		t.Fatalf("non-admin upsert = %d, want 403", code)
	}
}

func TestControlPlane_ModeRead_UnknownHostHonest(t *testing.T) {
	app, _ := newControlPlane(t)
	_, body := call(t, app, "GET", "/v1/featuregate/mode?host=example.com", false, nil)
	var out struct {
		WaitlistMode bool `json:"waitlistMode"`
		Known        bool `json:"known"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Known || out.WaitlistMode {
		t.Fatalf("unknown host = %+v, want known:false waitlistMode:false", out)
	}
}

func modeBit(t *testing.T, body []byte) bool {
	t.Helper()
	var out struct {
		WaitlistMode bool `json:"waitlistMode"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode mode: %v (%s)", err, body)
	}
	return out.WaitlistMode
}
