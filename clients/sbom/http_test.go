// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package sbom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// DatastoreEnabled() is false in the harness, so Mount skips the DDL and the
	// data endpoints answer 503 — the honest, no-fabrication path.
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// do issues a request. admin sets the SanitizeIdentity-minted X-User-IsAdmin (set
// only for owner == AdminOrg). In the harness there is no middleware, so
// c.IsAdmin() reads this header directly.
func do(t *testing.T, app *zip.App, method, path, body string, admin bool) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if admin {
		req.Header.Set("X-User-Id", "z")
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestIngestRequiresAdmin: a non-admin principal is refused 403 before any parse.
func TestIngestRequiresAdmin(t *testing.T) {
	app := mountApp(t)
	code, _ := do(t, app, http.MethodPost, "/v1/sbom", `{"imageDigest":"sha256:abc"}`, false)
	if code != http.StatusForbidden {
		t.Fatalf("non-admin ingest want 403, got %d", code)
	}
}

// TestIngestMissingImageDigest: an admin caller with a body that omits imageDigest
// is a 400 — validated before the datastore is consulted.
func TestIngestMissingImageDigest(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/sbom", `{"imageRef":"ghcr.io/hanzoai/foo:v1","document":{}}`, true)
	if code != http.StatusBadRequest {
		t.Fatalf("missing imageDigest want 400, got %d (%s)", code, body)
	}
}

// TestIngestDatastoreDown503: an admin caller with a valid body, but the store is
// not connected → honest 503 (never a fake 201).
func TestIngestDatastoreDown503(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/sbom",
		`{"imageDigest":"sha256:abc","document":{"components":[{"type":"library","name":"a","version":"1"}]}}`, true)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("datastore-down ingest want 503, got %d (%s)", code, body)
	}
}

// TestResolveDatastoreDown503: resolve honestly 503s when the store is down (and
// proves the greedy wildcard route matches a digest-shaped ref).
func TestResolveDatastoreDown503(t *testing.T) {
	app := mountApp(t)
	code, _ := do(t, app, http.MethodGet, "/v1/sbom/sha256:abc", "", false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("datastore-down resolve want 503, got %d", code)
	}
}

// TestHealthLiveness: /v1/sbom/health is the subsystem's own probe (service=sbom),
// always 200, datastore=false when disconnected. Not JWT-gated, and NOT shadowed
// by the greedy resolve wildcard (it is a static route registered first).
func TestHealthLiveness(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/sbom/health", "", false)
	if code != http.StatusOK {
		t.Fatalf("health want 200, got %d (%s)", code, body)
	}
	var h map[string]any
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatalf("health json: %v (%s)", err, body)
	}
	if h["service"] != "sbom" {
		t.Fatalf("health service want sbom, got %v", h["service"])
	}
	if h["datastore"] != false {
		t.Fatalf("health datastore want false when disconnected, got %v", h["datastore"])
	}
}

// TestEnsureTableRetriesOnDisconnectedDatastore is the regression guard for the
// init-order bug: the datastore connects AFTER Mount, so ensureTable must lazily
// (re)create the table on a later request and must NEVER latch a failure. With the
// store disconnected (the harness default) it returns an error, does not panic,
// leaves tableReady false, and a second call re-attempts (retryable) rather than
// caching the failure. Safe to call repeatedly.
func TestEnsureTableRetriesOnDisconnectedDatastore(t *testing.T) {
	// The harness never connects a datastore, so DatastoreEnabled() is false.
	tableMu.Lock()
	tableReady = false
	tableMu.Unlock()

	if err := ensureTable(context.Background()); err == nil {
		t.Fatal("ensureTable want error when datastore disconnected, got nil")
	}

	tableMu.Lock()
	ready := tableReady
	tableMu.Unlock()
	if ready {
		t.Fatal("ensureTable must NOT latch tableReady on failure (would 'succeed' against a missing table forever)")
	}

	// Retryable and safe to call repeatedly: each call re-attempts, again errors,
	// never panics.
	for i := 0; i < 3; i++ {
		if err := ensureTable(context.Background()); err == nil {
			t.Fatalf("ensureTable retry %d want error (retryable), got nil", i)
		}
	}
}

// TestEnsureTableConcurrentSafe proves ensureTable is race-free under concurrent
// callers (every request path calls it). Run with -race to exercise the mutex.
func TestEnsureTableConcurrentSafe(t *testing.T) {
	tableMu.Lock()
	tableReady = false
	tableMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ensureTable(context.Background())
		}()
	}
	wg.Wait()
}
