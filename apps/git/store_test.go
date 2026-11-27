package git

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// A file made before repos carried public and origin gains both on open, keeps its
// rows (private, with no link), and drops the importer's table. SQLite only: the file
// is the old shape, which a PostgreSQL schema never had.
func TestALegacyFileGainsItsColumns(t *testing.T) {
	ctx := context.Background()
	ns := namespace.System()
	db, err := cek.Open(ns, "git", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE repos (id TEXT PRIMARY KEY, org TEXT NOT NULL, project TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
		   description TEXT NOT NULL DEFAULT '', default_branch TEXT NOT NULL DEFAULT 'main', size_bytes INTEGER NOT NULL DEFAULT 0,
		   created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE UNIQUE INDEX ux_repos_org_project_name ON repos(org, project, name)`,
		`CREATE TABLE inbound_conflicts (id TEXT PRIMARY KEY, org TEXT NOT NULL, project TEXT NOT NULL, repo TEXT NOT NULL)`,
		`INSERT INTO repos (id,org,name,created_at,updated_at) VALUES ('r1','acme','svc',1,2)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	s, err := openStore(db)
	if err != nil {
		t.Fatalf("open the legacy file: %v", err)
	}
	defer func() { _ = s.Close() }()
	r, err := s.Get(ctx, "acme", "", "svc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r.Public || r.Origin != "" || r.SizeBytes != 0 || r.CreatedAt != 1 {
		t.Fatalf("the legacy row = %+v; want it private, unlinked, as written", r)
	}
	if err := s.SetPublic(ctx, "acme", "", "svc", true, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrigin(ctx, "acme", "", "svc", "https://host.example/acme/svc.git"); err != nil {
		t.Fatal(err)
	}
	if r, _ = s.Get(ctx, "acme", "", "svc"); !r.Public || r.Origin == "" {
		t.Fatalf("after setting both = %+v", r)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'inbound_conflicts'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("inbound_conflicts survived the open: %d, %v", n, err)
	}
}

// A name taken is a conflict on both engines, and so is a pull proposed twice.
func TestATakenNameIsAConflictOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, err := openStore(open(namespace.System(), "git"))
		if err != nil {
			t.Fatal(err)
		}
		r := Repo{ID: "r1", Org: "acme", Name: "svc", DefaultBranch: "main", CreatedAt: 1, UpdatedAt: 1}
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		r.ID = "r2"
		if err := s.Create(ctx, r); err != errConflict {
			t.Fatalf("second create = %v; want errConflict", err)
		}
		p := Pull{ID: "p1", Org: "acme", Repo: "svc", Title: "t", Base: "main", Head: "x", State: pullOpen, CreatedAt: 1, UpdatedAt: 1}
		if _, err := s.CreatePull(ctx, p); err != nil {
			t.Fatal(err)
		}
		p.ID = "p2"
		if _, err := s.CreatePull(ctx, p); err != errConflict {
			t.Fatalf("second open pull = %v; want errConflict", err)
		}
		if err := s.Create(ctx, Repo{ID: "r3", Org: "acme", Name: "a\x00b", DefaultBranch: "main"}); err == nil {
			t.Fatal("a NUL in a name was stored")
		}
	})
}
