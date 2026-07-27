package prompts

import (
	"context"
	"github.com/hanzoai/cloud/cek"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMigrateSelfHealsFromLegacyPromptSchema reproduces the removed
// clients/prompt facade's schema — a `prompts` table in the SAME prompts.db
// with NO `id` column — and proves openStore drops it and rebuilds forward so
// List/Upsert work instead of 500'ing "no such column: id" (the live 1.786.5
// regression on the persisted PVC).
func TestMigrateSelfHealsFromLegacyPromptSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.db")

	legacy, err := cek.Open(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE prompts (
	  org TEXT NOT NULL, name TEXT NOT NULL, version INTEGER NOT NULL,
	  type TEXT, labels TEXT, tags TEXT, prompt TEXT, config TEXT,
	  commit_message TEXT, created_at INTEGER, updated_at INTEGER);`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO prompts(org,name,version,created_at,updated_at)
	  VALUES('maxpower','old',1,0,0)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	_ = legacy.Close()

	s, err := openStore(path) // opens the SAME file — must self-heal, not error
	if err != nil {
		t.Fatalf("openStore over legacy file: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	if _, err := s.List(ctx, "maxpower"); err != nil {
		t.Fatalf("List after self-heal: %v", err)
	}
	if _, err := s.Upsert(ctx, mk("maxpower", "greeting", "hello")); err != nil {
		t.Fatalf("Upsert after self-heal: %v", err)
	}
	got, err := s.List(ctx, "maxpower")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "greeting" {
		t.Fatalf("want exactly [greeting] post-heal, got %+v", got)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mk(org, name, content string) Prompt {
	return Prompt{
		// id is the global PK; production genID makes it collision-resistant.
		// Include org+name here so two orgs' same-named prompts get distinct ids.
		ID: org + "-" + name + "-id", Org: org, Name: name, Type: "text", Content: content,
		Labels: []string{"prod"}, Tags: []string{"a"}, UpdatedAt: time.Now().Unix(),
	}
}

func TestUpsertCreatesV1ThenVersions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	p, err := s.Upsert(ctx, mk("maxpower", "greeting", "hello v1"))
	if err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	if p.Version != 1 {
		t.Fatalf("want version 1, got %d", p.Version)
	}

	p2, err := s.Upsert(ctx, mk("maxpower", "greeting", "hello v2"))
	if err != nil {
		t.Fatalf("upsert v2: %v", err)
	}
	if p2.Version != 2 {
		t.Fatalf("want version 2, got %d", p2.Version)
	}
	if p2.CreatedAt != p.CreatedAt {
		t.Fatalf("createdAt must be stable across versions: %d vs %d", p2.CreatedAt, p.CreatedAt)
	}

	got, err := s.Get(ctx, "maxpower", "greeting")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Content != "hello v2" || got.Version != 2 {
		t.Fatalf("current should be v2 content, got %q v%d", got.Content, got.Version)
	}
	// Version history is metadata-only now (Red MED-1) — bounded, newest-first.
	vs, err := s.Versions(ctx, "maxpower", "greeting")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 {
		t.Fatalf("want 2 versions newest-first, got %+v", vs)
	}
	if n, _ := s.CountVersions(ctx, "maxpower", "greeting"); n != 2 {
		t.Fatalf("CountVersions should be 2, got %d", n)
	}
}

// TestTenantIsolation is the security invariant: one org can never read, list,
// or delete another org's prompts, even with an identical prompt name.
func TestTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.Upsert(ctx, mk("maxpower", "shared", "maxpower-secret")); err != nil {
		t.Fatalf("seed maxpower: %v", err)
	}
	if _, err := s.Upsert(ctx, mk("acme", "shared", "acme-secret")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}

	// Same name, different orgs — must be two independent records.
	mp, err := s.Get(ctx, "maxpower", "shared")
	if err != nil || mp.Content != "maxpower-secret" {
		t.Fatalf("maxpower must see its own content, got %q err %v", mp.Content, err)
	}
	ac, err := s.Get(ctx, "acme", "shared")
	if err != nil || ac.Content != "acme-secret" {
		t.Fatalf("acme must see its own content, got %q err %v", ac.Content, err)
	}

	// List is org-scoped: exactly one row each, never the sibling's.
	mpList, _ := s.List(ctx, "maxpower")
	if len(mpList) != 1 || mpList[0].Content != "maxpower-secret" {
		t.Fatalf("maxpower list leaked or missing: %+v", mpList)
	}
	acList, _ := s.List(ctx, "acme")
	if len(acList) != 1 || acList[0].Content != "acme-secret" {
		t.Fatalf("acme list leaked or missing: %+v", acList)
	}

	// A cross-tenant delete must NOT touch the other org's data.
	deleted, err := s.Delete(ctx, "acme", "shared")
	if err != nil || !deleted {
		t.Fatalf("acme delete own: %v deleted=%v", err, deleted)
	}
	if _, err := s.Get(ctx, "maxpower", "shared"); err != nil {
		t.Fatalf("maxpower prompt must survive acme's delete: %v", err)
	}

	// Deleting a name that only exists in another org is a no-op miss.
	deleted, err = s.Delete(ctx, "acme", "shared")
	if err != nil || deleted {
		t.Fatalf("second acme delete should be a miss, deleted=%v err=%v", deleted, err)
	}
}

func TestGetMissReturnsNotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.Get(context.Background(), "maxpower", "nope"); err != errNotFound {
		t.Fatalf("want errNotFound, got %v", err)
	}
}

// TestNameValidation guards the prompt-name boundary. (There is intentionally
// no org normalization anymore — the org isolation key is used EXACTLY as the
// trust boundary mints it; see tenant(). Cross-tenant isolation on distinct-but-
// similar orgs is proven at the HTTP layer in http_test.go / red_*_test.go.)
func TestNameValidation(t *testing.T) {
	for _, bad := range []string{"", "../etc", "a b", "name!", strings.Repeat("x", 65)} {
		if nameRE.MatchString(bad) {
			t.Fatalf("nameRE should reject %q", bad)
		}
	}
	for _, good := range []string{"greeting", "greet.v2", "a_b-c", "X1"} {
		if !nameRE.MatchString(good) {
			t.Fatalf("nameRE should accept %q", good)
		}
	}
}
