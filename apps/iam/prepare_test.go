package iam

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
)

// The identity store this host opens itself, encrypted at rest, is prepared on
// the way in: a user an older binary saved without a key is found by any case of
// its name, so it cannot be registered a second time in another case.
func TestTheOpenedStoreIsPrepared(t *testing.T) {
	dir := dataDirWithStore(t)
	db, conn, err := openFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u := orm.New[schema.User](db)
	u.Owner, u.Name = "acme", "Alice"
	u.SetId("acme/Alice")
	if err := u.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	// What a binary that does not know the key leaves behind.
	if _, err := conn.Exec(`UPDATE _entities SET data = json_remove(data, '$.nameKey') WHERE kind = 'users'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := iamstore.GetUserByName(ctx, db, "acme", "ALICE"); got != nil {
		t.Fatal("the unkeyed row was found before the store was opened again")
	}
	_ = conn.Close()

	db, conn, err = openFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got, err := iamstore.GetUserByName(ctx, db, "acme", "ALICE"); err != nil || got == nil || got.Name != "Alice" {
		t.Fatalf("ALICE after openFile: %v, %v", got, err)
	}
}

// A comparison writes to neither store: copy --check opens the file as it is, so an
// unkeyed row stays unkeyed; a copy opens it prepared.
func TestACheckLeavesTheFileAsItIs(t *testing.T) {
	dir := dataDirWithStore(t)
	db, conn, err := openFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u := orm.New[schema.User](db)
	u.Owner, u.Name = "acme", "Bo"
	u.SetId("acme/Bo")
	if err := u.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE _entities SET data = json_remove(data, '$.nameKey') WHERE kind = 'users'`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	unkeyed := func(conn *sql.DB) int {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM _entities WHERE kind = 'users' AND json_extract(data, '$.nameKey') IS NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	_, conn, err = copyFile(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := unkeyed(conn); n == 0 {
		t.Fatal("the check keyed the file it was only to read")
	}
	_ = conn.Close()
	_, conn, err = copyFile(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if n := unkeyed(conn); n != 0 {
		t.Fatalf("a copy left %d rows unkeyed", n)
	}
}

// A check against a volume with no store says so and leaves no file behind: an
// empty iam.db there would be served by the next boot as a store with no one in it.
func TestACheckOfAMissingStoreCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := copyFile(dir, true); err == nil {
		t.Fatal("a check opened a store that is not there")
	}
	if _, err := os.Stat(StorePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("the check left a file at %s: %v", StorePath(dir), err)
	}
}
