package o11y

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/namespace"
	o11yrt "github.com/hanzoai/o11y/pkg/o11y"
	"github.com/hanzoai/sqlite"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// With no hanzo-sql named the runtime keeps its sqlite file and nothing is opened here.
func TestControlStoreStaysAFileWithoutHanzoSQL(t *testing.T) {
	t.Setenv(locus.Env, "")
	on, err := openControlStore(context.Background(), t.TempDir())
	if err != nil || on {
		t.Fatalf("openControlStore = %v, %v; want false, nil", on, err)
	}
}

// A control plane left in o11y.db is carried to its schema on hanzo-sql: every row, typed
// for the server, counted against the file, the file set aside afterwards, and a second
// start over the carried store does nothing.
func TestControlPlaneMovesFromItsFileToHanzoSQL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, legacyStore)
	ctx := context.Background()

	// the file an earlier run left, written with the DSN unset
	t.Setenv(locus.Env, "")
	file, err := sql.Open("sqlite", sqlite.PragmaDSN(path, []sqlite.Pragma{
		{Name: "busy_timeout", Value: "5000"}, {Name: "journal_mode", Value: "wal"}, {Name: "foreign_keys", Value: "1"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := o11yrt.MigrateSQL(ctx, providerSettings(), file, false); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO organizations (id, name, display_name, created_at, updated_at) VALUES ('0192e5ee-0000-7000-8000-000000000001', 'hanzo', 'Hanzo', '2026-01-02 03:04:05', '2026-01-02 03:04:05')`,
		`INSERT INTO users (id, display_name, email, status, org_id, is_root, created_at, updated_at) VALUES ('0192e5ee-0000-7000-8000-000000000002', 'z', 'z@hanzo.ai', 'active', '0192e5ee-0000-7000-8000-000000000001', 1, '2026-01-02 03:04:05', '2026-01-02 03:04:05')`,
	} {
		if _, err := file.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var want int
	if err := file.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	sqltest.Server(t)
	on, err := openControlStore(ctx, dir)
	if err != nil || !on {
		t.Fatalf("openControlStore = %v, %v; want true, nil", on, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file is still in place: %v", err)
	}
	if _, err := os.Stat(path + ".carried"); err != nil {
		t.Fatalf("the file was not set aside: %v", err)
	}

	db, err := locus.Open(namespace.System(), "o11y", dir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	var created string
	if err := db.QueryRow(`SELECT COUNT(*), MAX(created_at::text) FROM users WHERE email = 'z@hanzo.ai' AND is_root`).Scan(&got, &created); err != nil {
		t.Fatal(err)
	}
	if got != 1 || created == "" {
		t.Fatalf("users carried = %d (%q), want 1", got, created)
	}

	if on, err := openControlStore(ctx, dir); err != nil || !on {
		t.Fatalf("second start = %v, %v", on, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&got); err != nil || got != want {
		t.Fatalf("users after a second start = %d, %v; want %d", got, err, want)
	}
}
