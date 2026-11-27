package host

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/sdk/env"
)

// The ops listener is the unauthenticated in-cluster read — what a rollout check
// asks, on the health port, with no token and no product API in the way. Every
// route it serves carries the revision, including the 503 a draining pod answers,
// because "which commit is refusing me" is exactly when it is worth knowing.
func TestOpsListenerReportsTheRevision(t *testing.T) {
	srv := httptest.NewServer(healthMux())
	t.Cleanup(srv.Close)

	// The routes this listener SERVES, which is what the sentence above claims.
	// "/health" was here and is not a route any more: d3a32b4447 made /healthz the
	// one liveness address and left this list asking for the other, so the test
	// read ServeMux's own `404 page not found` — a body that begins with a digit,
	// which decodes as the NUMBER 404 and fails as "cannot unmarshal number into
	// map". A test that names an address nobody serves measures the 404 handler.
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: decode: %v", path, err)
		}
		rev, ok := body["revision"]
		if !ok {
			t.Fatalf("GET %s: %v has no revision field — this listener is how a rollout "+
				"check asks what is serving it", path, body)
		}
		if rev != env.Revision() || rev == "" {
			t.Fatalf("GET %s: revision = %q, want %q", path, rev, env.Revision())
		}
	}
}

// One payload, every status. The bodies on the ops listener used to be fixed byte
// strings and the product API built its own map, which is how a payload comes to
// carry a field on one surface and not another — so the field is asserted for
// every status the one builder produces, not just the happy one.
func TestHealthBodyAlwaysCarriesTheRevision(t *testing.T) {
	for _, status := range []string{"ok", "degraded", "draining"} {
		body := healthBody(status)
		if body["status"] != status {
			t.Errorf("healthBody(%q) status = %v", status, body["status"])
		}
		if body["revision"] != env.Revision() || body["revision"] == "" {
			t.Errorf("healthBody(%q) revision = %v, want %q", status, body["revision"], env.Revision())
		}
	}
}
