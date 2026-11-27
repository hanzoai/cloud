package index

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// A term cut at maxTerm bytes must never split a rune: a split leaves bytes that are
// not UTF-8, which the server's text refuses and the store's move to it then fails on.
func TestTokenizeNeverEmitsInvalidUTF8(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("日", 100),
		"a" + strings.Repeat("日", 100),
		"aa" + strings.Repeat("日", 50),
		strings.Repeat("😀", 40),
		"abc\xc3def \xff\xfe ok",
	} {
		for _, term := range tokenize(in) {
			if !utf8.ValidString(term) || term == "" || len(term) > maxTerm {
				t.Fatalf("tokenize(%q) emitted %q (valid=%v, %d bytes)", in, term, utf8.ValidString(term), len(term))
			}
		}
	}
	if got := tokenize(strings.Repeat("日", 100)); len(got) != 1 || got[0] != strings.Repeat("日", 21) {
		t.Fatalf("a long term is cut to its whole runes, got %q", got)
	}
}

// A file written before the tokenizer cut on a rune boundary holds a term that is not
// UTF-8. Opening it re-derives that document's terms from the document, so the file
// moves to the server, and the document is still found.
func TestAFileWithAnInvalidTermMoves(t *testing.T) {
	dsn := sqltest.Server(t)
	t.Setenv(locus.Env, "") // the file is written first
	dir := t.TempDir()
	ctx := context.Background()

	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndex(ctx, "acme", "docs", "id"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("日", 40) // 120 bytes: the old cut landed mid-rune (letters do not fold away)
	if err := s.Upsert(ctx, "acme", "docs", "id", []map[string]any{
		{"id": "1", "title": "kubernetes " + long},
		{"id": "2", "title": "unrelated words"},
	}); err != nil {
		t.Fatal(err)
	}
	// What the old tokenizer wrote: the term cut at 64 bytes, 63 of them whole runes.
	bad := []byte(strings.Repeat("e", 63))
	bad = append(bad, 0xc3)
	if _, err := s.db.Exec(`INSERT INTO terms(org, uid, term, pk) VALUES('acme','docs',CAST($1 AS TEXT),'1')`, bad); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv(locus.Env, dsn)
	t.Setenv(locus.MoveEnv, string(locus.Lazy))
	s, err = openStore(dir)
	if err != nil {
		t.Fatalf("a file holding an invalid term must open and move: %v", err)
	}
	defer s.Close()
	place, err := locus.Where(namespace.System(), "index", dir)
	if err != nil || place != locus.OnServer {
		t.Fatalf("the index is %v (%v), want the server", place, err)
	}
	hits, err := s.Search(ctx, "acme", "docs", "kubernetes", nil, 10, 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("the repaired document is found: %d hits (%v)", len(hits), err)
	}
	rows, err := s.db.Query(`SELECT term FROM terms`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil || !utf8.ValidString(term) {
			t.Fatalf("term %q: %v", term, err)
		}
		n++
	}
	if n == 0 {
		t.Fatal("the terms were not derived again")
	}
}
