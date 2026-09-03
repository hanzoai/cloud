package projects

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/event"
)

// storeAt opens a fresh projects store in a temp dir — migrations and all.
func storeAt(t *testing.T) *Store {
	t.Helper()
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestCreateMintsKey: setProjectDefaults is the ONE place every create path applies
// wired-by-default settings, so minting there is what makes a key unforgettable —
// POST /v1/project, /v1/project/fork and /v1/project/sites all funnel through it.
func TestCreateMintsKey(t *testing.T) {
	var p Project
	p.Org, p.Slug = "acme", "shop"
	if err := setProjectDefaults(&p, nil); err != nil {
		t.Fatalf("setProjectDefaults: %v", err)
	}
	if !strings.HasPrefix(p.Key, cloud.PublishablePrefix) {
		t.Fatalf("key %q must carry the one publishable prefix %q", p.Key, cloud.PublishablePrefix)
	}
	if len(p.Key) < 40 {
		t.Fatalf("key %q is too short to be unguessable", p.Key)
	}
}

// TestKeysAreDistinctPerProject: one key per project, never a shared one. A shared
// key would make every site in an org indistinguishable and would make revoking one
// site revoke them all.
func TestKeysAreDistinctPerProject(t *testing.T) {
	seen := map[string]string{}
	for _, slug := range []string{"one", "two", "three"} {
		p := Project{Org: "acme", Slug: slug}
		if err := setProjectDefaults(&p, nil); err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if prev, dup := seen[p.Key]; dup {
			t.Fatalf("%s reuses %s's key", slug, prev)
		}
		seen[p.Key] = slug
	}
}

// TestGetReturnsTheKey: the key is readable after create, because the static builder
// reads it from the project to inject the beacon. Shown in full — it is publishable
// by construction, and masking it would only force a second endpoint to fetch the
// thing the page already ships.
func TestGetReturnsTheKey(t *testing.T) {
	st := storeAt(t)
	p := Project{ID: "p1", Org: "acme", Slug: "shop", Name: "Shop", Analytics: true, Key: mustKey(t)}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetProject(context.Background(), "acme", "shop")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Key != p.Key {
		t.Fatalf("stored key = %q, want %q", got.Key, p.Key)
	}
	// ...and it reaches the wire the static builder reads.
	b, err := json.Marshal(toProject(got))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Key       string `json:"key"`
		Analytics bool   `json:"analytics"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wire.Key != p.Key {
		t.Fatalf("wire key = %q, want %q", wire.Key, p.Key)
	}
	if !wire.Analytics {
		t.Fatal("wire analytics must be true beside the key")
	}
}

// TestResolveKeyNamesTheProject: the key resolves to org AND site. The project half
// is what makes the credential an attribution rather than a label — `product` on the
// wire is the caller's, this is the server's.
func TestResolveKeyNamesTheProject(t *testing.T) {
	st := storeAt(t)
	p := Project{ID: "p1", Org: "acme", Slug: "shop", Name: "Shop", Analytics: true, Key: mustKey(t)}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	at, ok, err := (keyResolver{store: st}).Resolve(context.Background(), p.Key)
	if err != nil || !ok {
		t.Fatalf("resolve = (%v,%v,%v), want found", at, ok, err)
	}
	if at.Org != "acme" || at.Project != "shop" {
		t.Fatalf("resolve = %+v, want {acme shop}", at)
	}
}

// TestMissingSiteStopsRecording is the CTO's rule, structurally: delete the project
// and its key resolves to nothing, so the endpoint refuses. Recording stops because
// the site is gone, not because anyone remembered to revoke a credential.
func TestMissingSiteStopsRecording(t *testing.T) {
	st := storeAt(t)
	key := mustKey(t)
	p := Project{ID: "p1", Org: "acme", Slug: "shop", Name: "Shop", Analytics: true, Key: key}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	r := keyResolver{store: st}
	if _, ok, _ := r.Resolve(context.Background(), key); !ok {
		t.Fatal("precondition: the key must resolve while the project exists")
	}
	if _, gone, err := st.DeleteProject(context.Background(), "acme", "shop"); err != nil || !gone {
		t.Fatalf("delete = (%v,%v)", gone, err)
	}
	at, ok, err := r.Resolve(context.Background(), key)
	if err != nil {
		t.Fatalf("resolve after delete errored: %v", err)
	}
	if ok {
		t.Fatalf("a deleted project still attributes: %+v", at)
	}
}

// TestAnalyticsOptOutStopsResolving: analytics:false is honoured at the endpoint, not
// downstream. A project that asked not to be recorded reports unresolvable, so its
// caller is told the write did not land instead of being told it did.
func TestAnalyticsOptOutStopsResolving(t *testing.T) {
	st := storeAt(t)
	key := mustKey(t)
	p := Project{ID: "p1", Org: "acme", Slug: "shop", Name: "Shop", Analytics: false, Key: key}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok, _ := (keyResolver{store: st}).Resolve(context.Background(), key); ok {
		t.Fatal("analytics:false must not attribute")
	}
}

// TestUnknownKeyResolvesToNothing: a key no project holds is a clean miss, never an
// org on its own. An org without a project would be the silent misfiling this whole
// change removes.
func TestUnknownKeyResolvesToNothing(t *testing.T) {
	st := storeAt(t)
	for _, key := range []string{"", "   ", "pk-nope", "not-a-key", cloud.PublishablePrefix} {
		at, ok, err := (keyResolver{store: st}).Resolve(context.Background(), key)
		if err != nil {
			t.Fatalf("resolve %q errored: %v", key, err)
		}
		if ok {
			t.Fatalf("resolve %q = %+v, want a miss", key, at)
		}
	}
}

// TestEmptyKeyNeverMatchesABackfilledRow: ” is the pre-backfill state and the
// partial index permits duplicates there, so a blank lookup is the one input that
// could match rows it has no relationship to. ResolveKey refuses it before the query.
func TestEmptyKeyNeverMatchesABackfilledRow(t *testing.T) {
	st := storeAt(t)
	for _, slug := range []string{"a", "b"} {
		p := Project{ID: "p_" + slug, Org: "acme", Slug: slug, Name: slug, Analytics: true}
		if err := st.CreateProject(context.Background(), p); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}
	if _, err := st.ResolveKey(context.Background(), ""); err == nil {
		t.Fatal("an empty key must not resolve to a row")
	}
}

// TestMigrationBackfillsExistingProjects: every project that predates the column gets
// a key, distinct per row. Without it every site published before this change would
// resolve to nothing and go dark — the correct answer to a missing key and the wrong
// answer to a live site.
func TestMigrationBackfillsExistingProjects(t *testing.T) {
	st := storeAt(t)
	// Simulate pre-migration rows: a project row with no key.
	for _, slug := range []string{"old-one", "old-two"} {
		p := Project{ID: "p_" + slug, Org: "acme", Slug: slug, Name: slug, Analytics: true}
		if err := st.CreateProject(context.Background(), p); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}
	if err := st.backfillKeys(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	seen := map[string]bool{}
	for _, slug := range []string{"old-one", "old-two"} {
		p, err := st.GetProject(context.Background(), "acme", slug)
		if err != nil {
			t.Fatalf("get %s: %v", slug, err)
		}
		if !strings.HasPrefix(p.Key, cloud.PublishablePrefix) {
			t.Fatalf("%s backfilled key = %q", slug, p.Key)
		}
		if seen[p.Key] {
			t.Fatalf("%s shares a backfilled key", slug)
		}
		seen[p.Key] = true
	}
}

// TestBackfillNeverRotatesAServingKey: re-running the migration must not change a key
// a live site is already shipping.
func TestBackfillNeverRotatesAServingKey(t *testing.T) {
	st := storeAt(t)
	key := mustKey(t)
	p := Project{ID: "p1", Org: "acme", Slug: "shop", Name: "Shop", Analytics: true, Key: key}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := range 3 {
		if err := st.backfillKeys(); err != nil {
			t.Fatalf("backfill %d: %v", i, err)
		}
	}
	got, err := st.GetProject(context.Background(), "acme", "shop")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Key != key {
		t.Fatalf("backfill rotated a serving key: %q -> %q", key, got.Key)
	}
}

// keyResolver must satisfy the client the ingest endpoint consults.
var _ event.KeyResolver = keyResolver{}

func mustKey(t *testing.T) string {
	t.Helper()
	k, err := mintKey()
	if err != nil {
		t.Fatalf("mintKey: %v", err)
	}
	return k
}
