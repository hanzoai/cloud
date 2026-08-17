package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
)

// A site is reachable on TWO apexes — the site plane's own and the first-party
// one — so a purge that reaches one zone and not the other is the defect this
// pins. It is not hypothetical: hanzo.ai and cloud.hanzo.ai served pre-deploy
// bytes with cf-cache-status HIT while the origin had the new ones, and the
// purge returned 200 the whole time. Right call, wrong zone.
func TestPurgeReachesEveryZoneTheSiteIsServedOn(t *testing.T) {
	var mu sync.Mutex
	looked := map[string]int{}
	purged := []string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/zones") && r.Method == http.MethodGet:
			name := r.URL.Query().Get("name")
			looked[name]++
			switch name {
			case "hanzo.app":
				_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"zone-app"}]}`))
			case "hanzo.ai":
				_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"zone-ai"}]}`))
			default:
				_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/purge_cache"):
			purged = append(purged, strings.Split(strings.TrimPrefix(r.URL.Path, "/zones/"), "/")[0])
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := testEdge("tok", "", srv.URL, srv.Client()) // no zone pinned — both must be found
	defer e.Stop()

	if err := e.PurgeTags(context.Background(), "site-hanzo-a"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(purged)
	if len(purged) != 2 || purged[0] != "zone-ai" || purged[1] != "zone-app" {
		t.Errorf("purged %v, want both zone-ai and zone-app", purged)
	}
	// Both apexes asked exactly once: discovery is per process, and a retry loop
	// here hammers a quota shared by every tenant.
	if looked["hanzo.app"] != 1 || looked["hanzo.ai"] != 1 {
		t.Errorf("lookups = %v, want one apiece", looked)
	}
}

// An apex the account does not hold is not an error — a deployment need not own
// every apex it is configured with — and it must not stop the zone it does own.
func TestAnApexTheAccountDoesNotHoldIsSkipped(t *testing.T) {
	var mu sync.Mutex
	purged := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/purge_cache") {
			purged = append(purged, strings.Split(strings.TrimPrefix(r.URL.Path, "/zones/"), "/")[0])
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		if r.URL.Query().Get("name") == "hanzo.app" {
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"zone-app"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`)) // no such zone here
	}))
	defer srv.Close()

	e := testEdge("tok", "", srv.URL, srv.Client())
	defer e.Stop()
	if err := e.PurgeTags(context.Background(), "site-hanzo-a"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(purged) != 1 || purged[0] != "zone-app" {
		t.Errorf("purged %v, want only the zone the account holds", purged)
	}
}

// A pinned CF_ZONE_ID is the whole answer: an operator naming one zone is saying
// which zone they mean, and discovering more behind their back would purge zones
// they never asked for.
func TestAPinnedZoneIsTheWholeAnswer(t *testing.T) {
	var mu sync.Mutex
	var lookups int
	purged := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/purge_cache") {
			purged = append(purged, strings.Split(strings.TrimPrefix(r.URL.Path, "/zones/"), "/")[0])
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		lookups++
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"discovered"}]}`))
	}))
	defer srv.Close()

	e := testEdge("tok", "pinned-zone", srv.URL, srv.Client())
	defer e.Stop()
	if err := e.PurgeTags(context.Background(), "site-hanzo-a"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if lookups != 0 {
		t.Errorf("%d lookups with a pinned zone; a pin is the answer, not a hint", lookups)
	}
	if len(purged) != 1 || purged[0] != "pinned-zone" {
		t.Errorf("purged %v, want only the pinned zone", purged)
	}
}

// A token that cannot read zones is the two-token gap: it connects, and the call
// is refused. That must read as "stale until TTL", not a failed deploy, and must
// not retry into a shared quota.
func TestNoZoneFoundDegradesQuietly(t *testing.T) {
	var mu sync.Mutex
	var lookups, purges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/purge_cache") {
			purges++
			return
		}
		lookups++
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
	// One attempt per apex, for the whole process — not per purge.
	if lookups != len(zoneNames()) {
		t.Errorf("%d lookups across three purges, want %d (one per apex, once)", lookups, len(zoneNames()))
	}
	if purges != 0 {
		t.Errorf("%d purges with no zone; that POSTs to /zones//purge_cache", purges)
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

// Both apexes, deduplicated — a deployment may point both env vars at one name,
// and purging a zone twice is two calls against a shared quota.
func TestZoneNamesCoverBothApexesWithoutRepeating(t *testing.T) {
	names := zoneNames()
	if len(names) != 2 || names[0] != "hanzo.app" || names[1] != "hanzo.ai" {
		t.Fatalf("zoneNames() = %v, want the site apex then the first-party apex", names)
	}

	old := getenv
	defer func() { getenv = old }()
	getenv = func(k string) string { return "same.example" } // both point at one apex
	if got := zoneNames(); len(got) != 1 {
		t.Errorf("zoneNames() = %v, want one entry when the apexes coincide", got)
	}
}
