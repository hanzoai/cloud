package todo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
)

// THE ROWS ARE UNDER `tracker`, AND THAT IS NOT A LEFTOVER.
//
// The product renamed from tracker to Todo and this string did not move with it,
// which reads exactly like a rename somebody abandoned — so this is the test that
// says otherwise, because the next person to "finish the job" empties every board
// in the fleet and gets no error for it.
//
// cek keys each database with HKDF over the namespace AND the subsystem
// (cek.DeriveKey), and namespace.Path renders the same string into the filename.
// So the constant is a KEY BINDING. Changing it does not move a file and does not
// fail: the rows stay where they are, an empty database opens beside them under a
// key nothing has ever written with, and the board renders zero issues.
//
// What that would have cost, on the deployment this was found on: the org's
// roadmap — the epics assigned to named agents — lives only in this store. It is
// the half of the board the forge cannot answer for.
func TestStoreKeepsTheNameItsRowsWereWrittenUnder(t *testing.T) {
	if store != "tracker" {
		t.Fatalf(`store = %q.

The rows in production were written under "tracker" and the key is derived from
this string. Renaming it opens a new EMPTY database beside the real one — no
error, no log, an empty board. If the intent is to move the data, that is a
migration: open the old file under the old binding, copy it into one opened under
the new binding, and only then change this.`, store)
	}
}

// And what the rename actually does, demonstrated rather than described — so the
// claim above cannot rot into folklore if cek's derivation ever changes.
func TestRenamingTheStoreLosesTheRowsSilently(t *testing.T) {
	dir := t.TempDir()
	ns := cloud.MustOrgNamespace("hanzo", "default")

	db, err := cloud.OrgDB(dir, ns, store)
	if err != nil {
		t.Fatalf("open %s: %v", store, err)
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT); INSERT INTO issues VALUES('roadmap')`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	renamed, err := cloud.OrgDB(dir, ns, "todo")
	if err != nil {
		t.Fatalf("open under the renamed binding: %v", err)
	}
	defer func() { _ = renamed.Close() }()

	var n int
	if err := renamed.QueryRow(`SELECT count(*) FROM issues`).Scan(&n); err == nil {
		t.Fatalf("the renamed binding read %d rows. This exercises the DERIVED-key path "+
			"only — a temp dir has no .dek sidecar — so this says nothing about a "+
			"sidecar-backed store, where the wrapping key is derived from the namespace "+
			"and a file id and NOT from the subsystem. Re-derive the argument before "+
			"acting on it either way.", n)
	}

	// The rows are not gone; they are simply not where the renamed binding looks.
	if _, err := os.Stat(filepath.Join(dir, "orgs", "hanzo", "projects", "default", store+".db")); err != nil {
		t.Fatalf("the original store should be untouched on disk: %v", err)
	}
}
