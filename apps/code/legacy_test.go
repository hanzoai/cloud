package code

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/hanzoai/sqlite" // registers "sqlite" under both build tags
)

// oldShape is an org's code file as it stood before the port: INTEGER columns,
// AUTOINCREMENT ids, and the FTS5 table with a chunk_id column of its own. A file
// in production is this, and SQLite is where it is, so the upgrade is SQLite's
// alone and the test builds the file by hand.
const oldShape = `
CREATE TABLE blobs (hash TEXT PRIMARY KEY, content TEXT NOT NULL);
CREATE TABLE files (
  repo TEXT NOT NULL, path TEXT NOT NULL, lang TEXT NOT NULL, hash TEXT NOT NULL,
  size INTEGER NOT NULL, indexed_at INTEGER NOT NULL, PRIMARY KEY (repo, path)
);
CREATE TABLE symbols (
  id INTEGER PRIMARY KEY AUTOINCREMENT, repo TEXT NOT NULL, path TEXT NOT NULL, name TEXT NOT NULL,
  kind TEXT NOT NULL, line INTEGER NOT NULL, end_line INTEGER NOT NULL,
  signature TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL DEFAULT '', lang TEXT NOT NULL DEFAULT ''
);
CREATE TABLE edges (
  id INTEGER PRIMARY KEY AUTOINCREMENT, repo TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL,
  line INTEGER NOT NULL, from_symbol TEXT NOT NULL DEFAULT ''
);
CREATE TABLE chunks (
  id INTEGER PRIMARY KEY AUTOINCREMENT, repo TEXT NOT NULL, path TEXT NOT NULL,
  start_line INTEGER NOT NULL, end_line INTEGER NOT NULL, symbol TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '', hash TEXT NOT NULL, lang TEXT NOT NULL DEFAULT ''
);
CREATE VIRTUAL TABLE fts USING fts5(body, chunk_id UNINDEXED, tokenize='trigram');
CREATE TABLE vectors (chunk_id INTEGER PRIMARY KEY, dim INTEGER NOT NULL, vec BLOB NOT NULL);
`

// TestALegacyFileIsUpgradedInPlace: a file in the old shape opens, keeps what it
// indexed (found by substring through its own FTS table), takes a re-index and a
// delete through the same table, and a second open changes nothing.
func TestALegacyFileIsUpgradedInPlace(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "code.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(oldShape); err != nil {
		t.Fatalf("old shape: %v", err)
	}
	// What the old writer did for one chunk, statement for statement.
	body := "xylophone"
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO files (repo,path,lang,hash,size,indexed_at) VALUES ($1,$2,$3,$4,$5,$6)`, []any{"r", "old.go", "go", "h", 9, 1}},
		{`INSERT INTO blobs (hash,content) VALUES ($1,$2)`, []any{"b", "func Xylophone() {}"}},
		{`INSERT INTO chunks (repo,path,start_line,end_line,symbol,kind,hash,lang) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, []any{"r", "old.go", 1, 1, "Xylophone", "func", "b", "go"}},
		{`INSERT INTO fts (rowid,body,chunk_id) VALUES ($1,$2,$3)`, []any{1, body, 1}},
	} {
		if _, err := db.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed %s: %v", stmt.sql, err)
		}
	}

	for range 2 {
		st, err := openStore(db)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		rows, err := st.ftsSearch(context.Background(), "r", []string{"ylopho"}, false, 10)
		if err != nil || len(rows) != 1 || rows[0].Path != "old.go" {
			t.Fatalf("the old file's chunk is not found by a substring: %v, %v", rows, err)
		}
	}

	st, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, st, "r", "new.go", goFixture)
	if rows, _ := st.ftsSearch(context.Background(), "r", []string{"greet"}, false, 10); len(rows) == 0 {
		t.Fatal("a file indexed after the upgrade is not found")
	}
	if err := st.deleteFile(context.Background(), "r", "old.go"); err != nil {
		t.Fatalf("deleteFile: %v", err)
	}
	if rows, _ := st.ftsSearch(context.Background(), "r", []string{"ylopho"}, false, 10); len(rows) != 0 {
		t.Fatalf("a deleted file is still found: %v", rows)
	}
}
