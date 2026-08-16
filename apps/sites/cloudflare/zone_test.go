package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
)

// A zone id is now a thing the credential can answer, so the test that matters is
// that it IS asked, asked ONCE, and that a failure to answer degrades rather than
// escalates.
func TestZoneIsDiscoveredOnceAndPurgeUsesIt(t *testing.T) {
	var mu sync.Mutex
	var zoneCalls, purgeCalls int
	var purgedZone string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/zones") && r.Method == http.MethodGet:
			zoneCalls++
			if got := r.URL.Query().Get("name"); got != "hanzo.app" {
				t.Errorf("looked up zone %q, want the apex", got)
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"zone-123"}]}`))
		case strings.HasSuffix(r.URL.Path, "/purge_cache"):
			purgeCalls++
			purgedZone = strings.Split(strings.TrimPrefix(r.URL.Path, "/zones/"), "/")[0]
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := testEdge("tok", "", srv.URL, srv.Client()) // no zone supplied — it must be found
	defer e.Stop()

	for i := 0; i < 3; i++ {
		if err := e.PurgeTags(context.Background(), "site-hanzo-a"); err != nil {
			t.Fatalf("purge: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if zoneCalls != 1 {
		t.Errorf("zone looked up %d times, want exactly 1 — the answer is kept for the process", zoneCalls)
	}
	if purgeCalls == 0 {
		t.Fatal("no purge reached the API")
	}
	if purgedZone != "zone-123" {
		t.Errorf("purged zone %q, want the discovered zone-123", purgedZone)
	}
}

// A token that cannot read zones is the two-token gap: it connects, and the
// specific call is refused. That must read as "stale until TTL", not as a failed
// deploy — and it must not retry into a shared quota.
func TestAZoneThatCannotBeFoundDegradesQuietly(t *testing.T) {
	var mu sync.Mutex
	var zoneCalls, purgeCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/purge_cache") {
			purgeCalls++
			return
		}
		zoneCalls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"Zone:Read required"}]}`))
	}))
	defer srv.Close()

	e := testEdge("tok", "", srv.URL, srv.Client())
	defer e.Stop()

	for i := 0; i < 3; i++ {
		if err := e.PurgeTags(context.Background(), "site-hanzo-a"); err != nil {
			t.Fatalf("a refused lookup must not fail the caller, got %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if zoneCalls != 1 {
		t.Errorf("looked up %d times after a refusal, want 1 — a retry loop hammers a shared quota", zoneCalls)
	}
	if purgeCalls != 0 {
		t.Errorf("%d purges reached the API with no zone; that POSTs to /zones//purge_cache", purgeCalls)
	}
}

// Configured asks for a TOKEN, not a zone: an edge one lookup away from working
// must not report itself unconfigured, because that report is what /v1/edge
// serves and what an operator reads to decide whether a publish is live.
func TestConfiguredAsksForTheCredentialNotTheZone(t *testing.T) {
	log := luxlog.New("test")
	if !With("tok", "", log).Configured() {
		t.Error("a token with no zone reports unconfigured; the zone is discoverable from it")
	}
	if With("", "zone-123", log).Configured() {
		t.Error("a zone with no token reports configured; nothing can be purged without a credential")
	}
}
