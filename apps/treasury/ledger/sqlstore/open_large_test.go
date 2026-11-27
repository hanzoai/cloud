package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/namespace"
)

// A book of half a million plaintext memos (every metered call is an entry whose memo is its
// model) opens on the first boot of this build: the memos are sealed in a walk of small
// transactions that is linear in the book, and not in the migration's one minute.
func TestLargeBookOpens(t *testing.T) {
	master := bytes.Repeat([]byte{7}, 32)
	ns, _ := namespace.Org("acme")
	path := filepath.Join(t.TempDir(), "book.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	open := Opener(master, "finance", "")
	if _, err := open(ns, db); err != nil { // fresh book, current schema
		t.Fatal(err)
	}
	ctx := context.Background()
	const n = 500000
	tx, _ := db.BeginTx(ctx, nil)
	for i := 0; i < n; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO treasury_entries (seq,id,kind,program,ref,memo,amount,created_at) VALUES ($1,$2,'finance.usage','w',$3,'zen-1/chat','1',1)`, i+1, fmt.Sprintf("e%09d", i), fmt.Sprintf("r%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	s, err := open(ns, db) // the next boot: the sealing pass runs
	t.Logf("open of a %d-entry book took %s, err = %v", n, time.Since(began).Round(time.Second), err)
	if err != nil {
		t.Fatalf("the book does not open: %v", err)
	}
	var plain int
	if err := db.QueryRow(`SELECT COUNT(*) FROM treasury_entries WHERE substr(memo, 1, 5) <> 'seal:'`).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("%d memos still in plaintext: %v", plain, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT memo FROM treasury_entries WHERE id = $1`, "e000000007").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if got, err := openMemo(s.fields, "e000000007", stored); err != nil || got != "zen-1/chat" {
		t.Fatalf("a memo reads %q, %v", got, err)
	}
}
