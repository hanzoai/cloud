package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	luxlog "github.com/luxfi/log"
)

// testEdge builds a Edge the way these tests need it: a ZERO coalescing window,
// so every call is admitted and the assertions are about the request rather than the
// debounce, but with the two fields only New otherwise supplies. A bare struct
// literal left pending nil, so admit panicked writing to it, and left ceiling zero,
// so takeToken refused every call and nothing was ever sent.
// The tests do not use New itself because it reads the real environment and
// arms a 10s window.
func testEdge(token, zone, api string, c *http.Client) *Edge {
	return &Edge{
		token: token, zoneID: zone, api: api, client: c,
		log:     luxlog.New("test"),
		pending: map[string]*purgeState{},
		ceiling: 120, // New's default; 0 would rate-limit every call away
	}
}

func TestEdgeUnconfiguredIsNoop(t *testing.T) {
	p := testEdge("", "", "http://127.0.0.1:0", &http.Client{Timeout: time.Second})
	if p.Configured() {
		t.Fatal("expected unconfigured")
	}
	// Must not touch the network and must not error — a missing CF token never
	// fails a deploy.
	if err := p.PurgeTags(context.Background(), "site-acme-blog"); err != nil {
		t.Fatalf("unconfigured PurgeTags returned error: %v", err)
	}
}

func TestEdgePostsTagsWithAuth(t *testing.T) {
	var gotAuth, gotPath, gotCT string
	var gotTags []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		var body struct {
			Tags []string `json:"tags"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotTags = body.Tags
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	p := testEdge("tok-secret", "zone123", srv.URL, srv.Client())
	if !p.Configured() {
		t.Fatal("expected configured")
	}
	if err := p.PurgeTags(context.Background(), "site-acme-blog", "site-acme-docs"); err != nil {
		t.Fatalf("PurgeTags: %v", err)
	}
	if gotAuth != "Bearer tok-secret" {
		t.Errorf("auth = %q, want Bearer tok-secret", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotPath != "/zones/zone123/purge_cache" {
		t.Errorf("path = %q, want /zones/zone123/purge_cache", gotPath)
	}
	if len(gotTags) != 2 || gotTags[0] != "site-acme-blog" || gotTags[1] != "site-acme-docs" {
		t.Errorf("tags = %v", gotTags)
	}
}

func TestEdgePropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()
	p := testEdge("t", "z", srv.URL, srv.Client())
	if err := p.PurgeTags(context.Background(), "site-x-y"); err == nil {
		t.Fatal("expected error on non-2xx")
	}
}

func TestEdgeEmptyTagsIsNoop(t *testing.T) {
	p := testEdge("t", "z", "http://127.0.0.1:0", &http.Client{Timeout: time.Second})
	if err := p.PurgeTags(context.Background()); err != nil {
		t.Fatalf("empty PurgeTags: %v", err)
	}
}

// TestEnsureVerbatimPatchesOnlyDrift is the regression guard for the
// savor.hanzo.app outage: the zone had email_obfuscation ON, so Cloudflare
// rewrote every email address in the served markup and React's hydration
// compared markup it never rendered — #418/#425 → #423 → "Application error"
// on the slow loads. The assertion must turn a drifted rewriter OFF and must
// leave a compliant one alone (a needless PATCH burns the shared account's
// quota on every pod start).
func TestEnsureVerbatimPatchesOnlyDrift(t *testing.T) {
	state := map[string]string{"email_obfuscation": "on", "rocket_loader": "off"}
	var patched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		if r.Method == http.MethodPatch {
			var body struct {
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("patch %s: decode: %v", id, err)
			}
			state[id] = body.Value
			patched = append(patched, id+"="+body.Value)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "result": map[string]string{"id": id, "value": state[id]},
		})
	}))
	defer srv.Close()

	p := testEdge("tok", "zone1", srv.URL, srv.Client())
	p.EnsureVerbatim(context.Background())

	if len(patched) != 1 || patched[0] != "email_obfuscation=off" {
		t.Fatalf("patched = %v, want exactly [email_obfuscation=off]", patched)
	}
	for id, want := range rewriters {
		if state[id] != want {
			t.Errorf("zone %s = %q, want %q", id, state[id], want)
		}
	}
}

// An unconfigured or unauthorized zone must never be fatal: the site server has
// to boot and serve even when it cannot read Cloudflare — the same degradation
// PurgeTags documents.
func TestEnsureVerbatimDegradesSoftly(t *testing.T) {
	var reads, writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads.Add(1)
		} else {
			writes.Add(1)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	// Unconfigured: Cloudflare is not asked at all.
	testEdge("", "", srv.URL, srv.Client()).EnsureVerbatim(context.Background())
	if n := reads.Load() + writes.Load(); n != 0 {
		t.Fatalf("an unconfigured edge made %d requests, want 0", n)
	}

	// Unauthorized: every setting is asked for once, none is written, and one
	// refusal does not stop the rest from being read.
	testEdge("tok", "zone1", srv.URL, srv.Client()).EnsureVerbatim(context.Background())
	if got, want := int(reads.Load()), len(rewriters); got != want {
		t.Errorf("a refused zone was read %d times, want once per setting (%d)", got, want)
	}
	if n := writes.Load(); n != 0 {
		t.Errorf("a zone that could not be read was written %d times, want 0", n)
	}
}

// TestTheCeilingIsTheDeploymentsAcrossReplicas: the Cloudflare account is one however
// many replicas call it, so a ceiling of 3 purges a minute admits 3 in all when two
// replicas are asked in turn, and the other three are served stale until TTL. The
// account's quota is the platform's; a ceiling each pod kept to itself would spend
// it once per replica.
//
// It runs on the in-process KV; the KV driver's own tests (internal/driver/kv) hold
// the same counts to a real server.
//
// MUTATION: count calls in the Edge again (a minute and a counter under its mutex)
// and each replica admits its own 3.
func TestTheCeilingIsTheDeploymentsAcrossReplicas(t *testing.T) {
	t.Run("in-process KV", func(t *testing.T) { shareTheCeiling(t, coordtest.Memory(t)) })
}

func shareTheCeiling(t *testing.T, pod func(id string) *coord.Coord) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	replicas := []*Edge{
		testEdge("tok", "zone", srv.URL, srv.Client()),
		testEdge("tok", "zone", srv.URL, srv.Client()),
	}
	for i, e := range replicas {
		e.ceiling = 3
		e.pods = pod(fmt.Sprintf("cloud-%d", i))
	}
	for i := range 6 {
		if err := replicas[i%2].PurgeTags(context.Background(), fmt.Sprintf("site-acme-%d", i)); err != nil {
			t.Fatalf("purge %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("two replicas made %d API calls under a ceiling of 3 a minute, want 3 in all", got)
	}
}

// A prefix purge is the tag purge with another field: one call to the zone, the
// prefixes as given, and never taken for a repeat of a tag purge of the same words.
func TestPurgePrefixesNamesURLsByPrefix(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string][]string
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	p := testEdge("tok", "zone123", srv.URL, srv.Client())
	defer p.Stop()
	if err := p.PurgePrefixes(context.Background(), "api.hanzo.ai/v1/pricing", "api.hanzo.ai/v1/models"); err != nil {
		t.Fatalf("PurgePrefixes: %v", err)
	}
	// The same words as tags are another purge, not a repeat inside the window.
	if err := p.PurgeTags(context.Background(), "api.hanzo.ai/v1/pricing", "api.hanzo.ai/v1/models"); err != nil {
		t.Fatalf("PurgeTags: %v", err)
	}
	if err := p.PurgePrefixes(context.Background()); err != nil {
		t.Fatalf("empty PurgePrefixes: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("calls = %d, want 2 (one by prefix, one by tag): %v", len(bodies), bodies)
	}
	if got := bodies[0]["prefixes"]; len(got) != 2 || got[0] != "api.hanzo.ai/v1/pricing" || len(bodies[0]["tags"]) != 0 {
		t.Errorf("first body = %v, want the two prefixes and no tags", bodies[0])
	}
	if got := bodies[1]["tags"]; len(got) != 2 || len(bodies[1]["prefixes"]) != 0 {
		t.Errorf("second body = %v, want the two tags and no prefixes", bodies[1])
	}
}
