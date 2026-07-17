// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/goja"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// mountApp boots the base subsystem (embed ON) into a bare zip.App — no
// SanitizeIdentity middleware, so X-Org-Id + X-User-Id are trusted verbatim (the
// standard cloud leaf test harness). IAMIssuer is left empty so per-org apps run
// without external-auth and the seeded PUBLIC collections are reachable over HTTP
// without a bearer — the test exercises the ROUTING + per-org ISOLATION, which is
// orthogonal to IAM token validation.
func mountApp(t *testing.T) (*zip.App, string) {
	t.Helper()
	t.Setenv("CLOUD_BASE_EMBED", "1")
	t.Setenv("BASE_API_PREFIX", apiPrefix)
	dataDir := t.TempDir()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: dataDir}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, dataDir
}

func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org) // makes principal.Validated true
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// seedPublicNotes creates a PUBLIC "notes" collection (public list/view/create
// rules) in the given org's Base app via the engine's Go API, so record CRUD is
// reachable over HTTP without a bearer.
func seedPublicNotes(t *testing.T, org string) *baseapp.Base {
	t.Helper()
	bapp, err := mounted.pool.appFor(org)
	if err != nil {
		t.Fatalf("appFor(%s): %v", org, err)
	}
	c := core.NewBaseCollection("notes")
	c.Fields.Add(&core.TextField{Name: "title"})
	public := ""
	c.ListRule = &public
	c.ViewRule = &public
	c.CreateRule = &public
	if err := bapp.Save(c); err != nil {
		t.Fatalf("save collection for %s: %v", org, err)
	}
	return bapp
}

// listTitles GETs the notes records for org and returns each record's title.
func listTitles(t *testing.T, app *zip.App, org string) []string {
	t.Helper()
	code, body := req(t, app, http.MethodGet, "/v1/base/collections/notes/records", org, nil)
	if code != http.StatusOK {
		t.Fatalf("list %s want 200, got %d (%s)", org, code, body)
	}
	var page struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode list %s: %v (%s)", org, err, body)
	}
	titles := make([]string, len(page.Items))
	for i, it := range page.Items {
		titles[i] = it.Title
	}
	return titles
}

// TestHealthNoEmbed proves the liveness route answers even with the embed OFF —
// linking the subsystem never depends on CLOUD_BASE_EMBED.
func TestHealthNoEmbed(t *testing.T) {
	t.Setenv("CLOUD_BASE_EMBED", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	code, body := req(t, app, http.MethodGet, "/v1/base/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("health want 200, got %d (%s)", code, body)
	}
	// /v1/base/* hosting is OFF with the embed disabled → no per-org routes.
	if code, _ := req(t, app, http.MethodGet, "/v1/base/collections/notes/records", "acme", nil); code == http.StatusOK {
		t.Fatalf("embed off: /v1/base/* should not serve, got 200")
	}
}

// TestPerOrgIsolatedCRUD is the wire proof of the per-org hosting lane: the
// health route, the principal gate, a collection record create→read-back, and —
// the crux — that a record written under one org is INVISIBLE to another (each
// org is a physically separate Base/SQLite).
func TestPerOrgIsolatedCRUD(t *testing.T) {
	app, dataDir := mountApp(t)

	// Health answers with the embed ON.
	if code, body := req(t, app, http.MethodGet, "/v1/base/health", "", nil); code != http.StatusOK {
		t.Fatalf("health want 200, got %d (%s)", code, body)
	}

	// No validated principal → 403 (the org gate), never reaching a Base app.
	if code, _ := req(t, app, http.MethodGet, "/v1/base/collections/notes/records", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org GET want 403, got %d", code)
	}

	// Seed the same-named collection in two orgs (distinct physical Bases).
	seedPublicNotes(t, "acme")
	seedPublicNotes(t, "globex")

	// Create a record in each org over the REAL HTTP path.
	if code, body := req(t, app, http.MethodPost, "/v1/base/collections/notes/records", "acme",
		map[string]any{"title": "acme-note"}); code >= 300 {
		t.Fatalf("acme create want <300, got %d (%s)", code, body)
	}
	if code, body := req(t, app, http.MethodPost, "/v1/base/collections/notes/records", "globex",
		map[string]any{"title": "globex-note"}); code >= 300 {
		t.Fatalf("globex create want <300, got %d (%s)", code, body)
	}

	// Each org reads back ONLY its own record — cross-org isolation.
	if got := listTitles(t, app, "acme"); len(got) != 1 || got[0] != "acme-note" {
		t.Fatalf("acme list = %v, want [acme-note]", got)
	}
	if got := listTitles(t, app, "globex"); len(got) != 1 || got[0] != "globex-note" {
		t.Fatalf("globex list = %v, want [globex-note]", got)
	}

	// The isolation is physical: distinct on-disk data dirs per org segment.
	acmeDir := filepath.Join(dataDir, "base", goja.TenantSegment("acme"))
	globexDir := filepath.Join(dataDir, "base", goja.TenantSegment("globex"))
	if acmeDir == globexDir {
		t.Fatalf("orgs share a data dir: %s", acmeDir)
	}
	for _, d := range []string{acmeDir, globexDir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("expected per-org dir %s: err=%v", d, err)
		}
	}
}
