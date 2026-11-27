package tools

import (
	"context"
	"github.com/hanzoai/cloud/internal/kmstest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// An older file gains the columns it lacks and keeps its rows. Only a SQLite file can
// be that old: hanzo-sql was never given the older shapes, so the file is built by hand.
func TestAnOlderFileGainsTheColumnsItLacks(t *testing.T) {

	dir := t.TempDir()
	old := func(name string, statements ...string) {
		t.Helper()
		db, err := locus.Open(namespace.System(), name, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range statements {
			if _, err := db.Exec(s); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	old("tools-activation",
		`CREATE TABLE activation (org TEXT NOT NULL, project TEXT NOT NULL, tool TEXT NOT NULL, source TEXT NOT NULL DEFAULT '',
  by_user TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, PRIMARY KEY (org, project, tool))`,
		`INSERT INTO activation (org, project, tool, created_at) VALUES ('acme', 'default', 'old_tool', 1)`)
	old("tools-skills",
		`CREATE TABLE skills (id TEXT NOT NULL, org TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (org, id))`,
		`INSERT INTO skills (id, org, name, content, created_at) VALUES ('old', 'acme', 'old', 'body', 1)`)
	old("tools-mcp",
		`CREATE TABLE mcp_servers (id TEXT NOT NULL, org TEXT NOT NULL, name TEXT NOT NULL, url TEXT NOT NULL,
  auth_header TEXT NOT NULL DEFAULT '', has_secret INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, PRIMARY KEY (org, id))`,
		`INSERT INTO mcp_servers (id, org, name, url, created_at) VALUES ('old', 'acme', 'old', 'https://old.example/mcp', 1)`)

	ctx := context.Background()
	act, err := OpenActivationStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = act.Close() }()
	if got, err := act.List(ctx, "acme", ""); err != nil || len(got) != 1 || got[0] != "old_tool" {
		t.Fatalf("the old activation read as %v (%v)", got, err)
	}
	if got, err := act.Admitted(ctx, "acme", ""); err != nil || len(got) != 0 {
		t.Fatalf("a row that predates admitted said yes: %v (%v)", got, err)
	}

	skills, err := OpenSkillStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = skills.Close() }()
	got, err := skills.List(ctx, "acme")
	if err != nil || len(got) != 1 || got[0].Name != "old" || got[0].Admitted || got[0].Source != "" {
		t.Fatalf("the old skill read as %+v (%v)", got, err)
	}

	servers, err := OpenMCPServerStore(dir, testMaster, testKMS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = servers.Close() }()
	srv, err := servers.Get(ctx, "acme", "old")
	if err != nil || srv.Listing != "" || srv.Admitted {
		t.Fatalf("the old server read as %+v (%v)", srv, err)
	}
}

// The address in a file from before KMS still reads, and is moved to KMS by the open.
func TestAnOlderServerAddressMovesToKMS(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	db, err := locus.Open(namespace.System(), "tools-mcp", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE mcp_servers (id TEXT NOT NULL, org TEXT NOT NULL, name TEXT NOT NULL, url TEXT NOT NULL,
  auth_header TEXT NOT NULL DEFAULT '', has_secret INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, PRIMARY KEY (org, id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mcp_servers (id, org, name, url, created_at) VALUES ('old', 'acme', 'old', 'https://mcp.example/sse?key=legacy-key-123', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	k := kmstest.New()
	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		servers, err := OpenMCPServerStore(dir, testMaster, k)
		if err != nil {
			t.Fatal(err)
		}
		srv, err := servers.Get(context.Background(), "acme", "old")
		if err != nil || srv.URL != "https://mcp.example/sse?key=legacy-key-123" {
			t.Fatalf("pass %d: the old server read as %+v (%v)", pass, srv, err)
		}
		var raw string
		if err := servers.db.QueryRow(`SELECT url FROM mcp_servers WHERE id = 'old'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw != "kms:orgs/acme/tools-mcp/old/url@default" || strings.Contains(raw, "legacy-key") {
			t.Fatalf("pass %d: the address column holds %.60q after the open, want its KMS path", pass, raw)
		}
		_ = servers.Close()
	}
}
