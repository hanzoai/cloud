package basedb

import (
	"os"
	"testing"

	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/hanzoai/namespace"
)

// The whole reason this package exists: cek renders the path and opens the file,
// but nothing creates the directory it lives in — and on the pure-Go codec the
// database is written back at CLOSE, so a missing parent does not fail the open,
// it loses the data at the end. Written as a round trip through Close, because
// that is where the loss happens and an open-only assertion would pass either way.
func TestOpenSurvivesAMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	ns := namespace.MustOrgProject("acme", "")

	db, err := Open(ns, "widget", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES ('kept')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path, err := namespace.Path(dir, ns, "widget")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no database at %s after close: %v", path, err)
	}

	again, err := Open(ns, "widget", dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	var v string
	if err := again.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if v != "kept" {
		t.Fatalf("v = %q, want kept", v)
	}
}
