package cek

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInMemoryNeverTouchesDisk pins the distinction cek has to make: an in-memory
// database is not a path. Treating ":memory:" as a filename created a real, durable,
// encrypted file of that name in the working directory — every opener in that
// directory silently sharing one store, and an ephemeral store outliving its process.
func TestInMemoryNeverTouchesDisk(t *testing.T) {
	EnsureDevKey()
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	for _, dsn := range []string{":memory:", "file::memory:", "file:x?mode=memory&cache=shared"} {
		db, err := Open(dsn)
		if err != nil {
			t.Fatalf("Open(%q): %v", dsn, err)
		}
		if _, err := db.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
			t.Fatalf("Open(%q): unusable: %v", dsn, err)
		}
		_ = db.Close()
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range ents {
		t.Errorf("in-memory open left %q on disk", filepath.Join(dir, e.Name()))
	}
}

// TestInMemoryPredicate pins which DSNs are in-memory — a real path must never be
// mistaken for one, or it would open unencrypted.
func TestInMemoryPredicate(t *testing.T) {
	for dsn, want := range map[string]bool{
		":memory:":                        true,
		"  :memory:  ":                    true,
		"file::memory:":                   true,
		"file:x?mode=memory":              true,
		"file:x?cache=shared&mode=memory": true,
		"/var/lib/cloud/orgs/acme/kms.db": false,
		"memory.db":                       false,
		"file:/var/lib/x.db":              false,
		"file:x?mode=ro":                  false,
		"":                                false,
	} {
		if got := inMemory(dsn); got != want {
			t.Errorf("inMemory(%q) = %v, want %v", dsn, got, want)
		}
	}
}
