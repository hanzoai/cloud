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

	"sync/atomic"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// compose installs what a host installs. A subsystem never installs cloud.Bridge:
// the program's composer owns it — serve.go at the root of the fused host, the
// plugin constructor for a plugin program. In a test the test is the composer, so
// it owes the same install; skipping it drives a program where every org-scoped op
// answers 403 for a reason production callers never see.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	// DatastoreEnabled() is false in the harness, so Mount skips the DDL and the
	// data endpoints answer 503 — the honest, no-fabrication path.
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// caller is who a request comes from. Three states, because this surface answers
// differently to each: nobody, somebody, and platform sudo.
type caller int

const (
	// anon carries no identity at all — SanitizeIdentity minted nothing.
	anon caller = iota
	// member is an attested caller with no platform authority: the ordinary tenant.
	member
	// sudo is the SanitizeIdentity-minted X-User-IsAdmin, set only for owner ==
	// AdminOrg. In the harness there is no middleware, so c.IsAdmin() reads the
	// header directly.
	sudo
)

// do issues a request as who.
func do(t *testing.T, app *zip.App, method, path, body string, who caller) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if who >= member {
		req.Header.Set("X-User-Id", "z")
		req.Header.Set("X-Org-Id", "acme")
	}
	if who == sudo {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(req)
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
	code, _ := do(t, app, http.MethodPost, "/v1/sbom", `{"imageDigest":"sha256:abc"}`, member)
	if code != http.StatusForbidden {
		t.Fatalf("non-admin ingest want 403, got %d", code)
	}
}

// TestIngestMissingImageDigest: an admin caller with a body that omits imageDigest
// is a 400 — validated before the datastore is consulted.
func TestIngestMissingImageDigest(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/sbom", `{"imageRef":"ghcr.io/hanzoai/foo:v1","document":{}}`, sudo)
	if code != http.StatusBadRequest {
		t.Fatalf("missing imageDigest want 400, got %d (%s)", code, body)
	}
}

// TestIngestDatastoreDown503: an admin caller with a valid body, but the store is
// not connected → honest 503 (never a fake 201).
func TestIngestDatastoreDown503(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/sbom",
		`{"imageDigest":"sha256:abc","document":{"components":[{"type":"library","name":"a","version":"1"}]}}`, sudo)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("datastore-down ingest want 503, got %d (%s)", code, body)
	}
}

// TestResolveDatastoreDown503: resolve honestly 503s when the store is down (and
// proves the greedy wildcard route matches a digest-shaped ref).
func TestResolveDatastoreDown503(t *testing.T) {
	app := mountApp(t)
	code, _ := do(t, app, http.MethodGet, "/v1/sbom/sha256:abc", "", member)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("datastore-down resolve want 503, got %d", code)
	}
}

// TestHealthLiveness: /v1/sbom/health is the subsystem's own probe (service=sbom),
// always 200, datastore=false when disconnected. Not JWT-gated, and NOT shadowed
// by the greedy resolve wildcard (it is a static route registered first).
func TestHealthLiveness(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/sbom/health", "", anon)
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
	for i := range 3 {
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
	for range 16 {
		wg.Go(func() {
			_ = ensureTable(context.Background())
		})
	}
	wg.Wait()
}

// TestResolveRefusesAStranger: global is not public. The read is cross-tenant
// because a bill of materials belongs to a digest rather than to an org, and that
// is a statement about the ANSWER, never about who may ask — the same 403 every
// other attested read in the estate answers, and before the ref is even looked at.
func TestResolveRefusesAStranger(t *testing.T) {
	app := mountApp(t)
	for _, path := range []string{
		"/v1/sbom/sha256:abc",
		"/v1/sbom/ghcr.io/hanzoai/cloud:v1",
		"/v1/sbom/evil.example/x:latest",
	} {
		code, body := do(t, app, http.MethodGet, path, "", anon)
		if code != http.StatusForbidden {
			t.Fatalf("anonymous GET %s: got %d (%s), want 403", path, code, body)
		}
		if !strings.Contains(string(body), "a validated principal is required") {
			t.Errorf("GET %s refused with %s; want the estate's one refusal", path, body)
		}
	}
	// And the probe beside it stays answerable without a credential, which is the
	// whole reason the check is on the handler and not on the group.
	if code, _ := do(t, app, http.MethodGet, "/v1/sbom/health", "", anon); code != http.StatusOK {
		t.Fatalf("anonymous health probe: got %d, want 200", code)
	}
}

// TestThePullOnlyReadsOurRegistries holds the rule that makes ONE shared table
// trustworthy for every tenant: an attached document is whoever holds that
// repository speaking, so only repositories we hold may write into the answer
// everyone reads. It is also what decides which hosts this pod will dial.
func TestThePullOnlyReadsOurRegistries(t *testing.T) {
	for ref, want := range map[string]bool{
		"ghcr.io/hanzoai/cloud:v1":       true,
		"ghcr.io/luxfi/node:v1.36.15":    true,
		"ghcr.io/zooai/zoo:latest":       true,
		"oci.hanzo.ai/hanzoai/ai:v1":     true,
		"git.hanzo.ai/hanzoai/iam:v1":    true,
		"ghcr.io/hanzoaix/cloud:v1":      false, // a neighbour of the name, not the name
		"ghcr.io/attacker/cloud:v1":      false,
		"evil.example/hanzoai/cloud:v1":  false, // our org name under somebody else's host
		"docker.io/library/nginx:latest": false,
		"nginx":                          false, // the default registry is still not ours
	} {
		parsed, err := name.ParseReference(ref)
		if err != nil {
			t.Fatalf("parse %q: %v", ref, err)
		}
		if got := ours(parsed.Context()) == nil; got != want {
			t.Errorf("ours(%s) = %v, want %v", parsed.Context().Name(), got, want)
		}
	}
}

// TestAForeignRefIsRefusedBeforeTheNetwork proves the decision is taken BEFORE a
// connection: the listener that would serve the ref records every request it gets,
// and it must record none. A check that ran after the fetch would still return an
// error and would already have made the call.
func TestAForeignRefIsRefusedBeforeTheNetwork(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	if _, err := pullSBOM(t.Context(), host+"/hanzoai/cloud:v1"); err == nil {
		t.Fatal("pullSBOM accepted a ref outside our registries")
	} else if !strings.Contains(err.Error(), "not a registry this store pulls from") {
		t.Fatalf("refused with %v; want the registry refusal", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("the refused ref still reached its host %d time(s)", n)
	}
}
