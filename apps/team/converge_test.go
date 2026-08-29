package team

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/orm/query"

	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// A deployment written before the rename holds its rows in `workspaces` and its
// documents under `<root>/workspaces`. Both are carried on the boot that finds
// them, and the failure both would otherwise have is SILENT: the table and the
// tree are simply not found, so an org reads as having no spaces and a space
// reads as having no documents, on a deployment that reports itself healthy.

// TestConvergeCarriesTheTable proves the rows survive, that the indexes end up
// named for the table they sit on, and that a second boot is a no-op.
func TestConvergeCarriesTheTable(t *testing.T) {
	planetest.ServeIdentity(t)
	dir := t.TempDir()

	// A store as the old code left it: the table and its four indexes under the
	// names that version wrote, holding one real row.
	pre, err := openAccountStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pre.db.NewQuery(`
DROP TABLE IF EXISTS spaces;
CREATE TABLE workspaces (
  id TEXT PRIMARY KEY, slug TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
  uuid TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '', owner_org TEXT NOT NULL,
  data_id TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL);
CREATE UNIQUE INDEX ux_workspaces_uuid     ON workspaces(uuid);
CREATE UNIQUE INDEX ux_workspaces_org_slug ON workspaces(owner_org, slug);
CREATE INDEX        ix_workspaces_org      ON workspaces(owner_org);
CREATE UNIQUE INDEX ux_workspaces_org_owner ON workspaces(owner_org, owner) WHERE owner <> '';
`).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := pre.db.Insert("workspaces", query.Params{
		"id": "w1", "slug": "acme-eng", "name": "Engineering", "uuid": "u-1",
		"owner": "acct-1", "owner_org": "acme", "created_at": 1,
	}).Execute(); err != nil {
		t.Fatal(err)
	}
	_ = pre.Close()

	// The boot that finds it.
	s, err := openAccountStore(dir)
	if err != nil {
		t.Fatalf("open over a pre-space store: %v", err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.SpaceBySlug(context.Background(), "acme", "acme-eng")
	if err != nil {
		t.Fatalf("the row did not survive the carry: %v", err)
	}
	if got.UUID != "u-1" || got.Name != "Engineering" {
		t.Fatalf("row came back changed: %+v", got)
	}

	// The indexes are named for the table they are on, so the file is identical
	// to one created fresh rather than carrying the old names forward.
	var stale, fresh int
	if err := s.db.NewQuery(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE '%workspaces%'`).
		Row(&stale); err != nil {
		t.Fatal(err)
	}
	if err := s.db.NewQuery(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'ux_spaces%' OR name LIKE 'ix_spaces%'`).
		Row(&fresh); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Errorf("%d index(es) still named for the old table", stale)
	}
	if fresh != 4 {
		t.Errorf("indexes on spaces = %d, want 4", fresh)
	}
	if s.hasTable("workspaces") {
		t.Error("the old table is still present")
	}

	// Idempotent: the next boot finds the new table and does nothing.
	if err := s.converge(); err != nil {
		t.Fatalf("second converge: %v", err)
	}
	if _, err := s.SpaceBySlug(context.Background(), "acme", "acme-eng"); err != nil {
		t.Fatalf("row lost on the second boot: %v", err)
	}
}

// TestConvergeCarriesTheTree proves the document tree moves WHOLE — every
// per-space database and the `.dek` cek keeps beside it — because a rename that
// took the files and left the keys would leave each space encrypted with a key
// nothing can find.
func TestConvergeCarriesTheTree(t *testing.T) {
	root := t.TempDir()
	was := filepath.Join(root, "workspaces", "orgs", "acme", "projects", "u-1")
	if err := os.MkdirAll(was, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"docs.db": "rows", "docs.db.dek": "key"} {
		if err := os.WriteFile(filepath.Join(was, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := converge(root); err != nil {
		t.Fatalf("converge: %v", err)
	}

	is := filepath.Join(root, "spaces", "orgs", "acme", "projects", "u-1")
	for name, want := range map[string]string{"docs.db": "rows", "docs.db.dek": "key"} {
		got, err := os.ReadFile(filepath.Join(is, name))
		if err != nil {
			t.Fatalf("%s did not survive the carry: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "workspaces")); !os.IsNotExist(err) {
		t.Error("the old tree is still there")
	}
}

// TestConvergeLeavesEveryOtherDeploymentAlone: a fresh deployment has nothing to
// carry, and a converged one must not be carried over a second time — which is
// the case that would OVERWRITE live documents with an abandoned tree if the
// guard read only the old name.
func TestConvergeLeavesEveryOtherDeploymentAlone(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		root := t.TempDir()
		if err := converge(root); err != nil {
			t.Fatalf("converge on a fresh deployment: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "spaces")); !os.IsNotExist(err) {
			t.Error("converge invented a tree")
		}
	})

	t.Run("both present", func(t *testing.T) {
		root := t.TempDir()
		for name, body := range map[string]string{"workspaces": "abandoned", "spaces": "live"} {
			if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name, "docs.db"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := converge(root); err != nil {
			t.Fatalf("converge: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, "spaces", "docs.db"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "live" {
			t.Fatalf("the live tree was overwritten by the abandoned one: %q", got)
		}
	})
}
