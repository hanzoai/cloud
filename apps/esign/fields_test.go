package esign

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// rawIndex reads every (key, org) of the index as the database holds it, past the store.
func rawIndex(t *testing.T, x *tokenIndex) map[string]string {
	t.Helper()
	rows, err := x.db.Query(`SELECT blind, org FROM token_index`)
	if err != nil {
		t.Fatalf("read the index: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, o string
		if err := rows.Scan(&k, &o); err != nil {
			t.Fatal(err)
		}
		out[k] = o
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The signing token is a bearer credential. The index keeps its blind index and resolves
// by it, so the table holds no token that opens a signing link.
func TestTheTokenIsNotStoredOnlyItsBlind(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		x, err := openTokenIndex(t.TempDir(), testMaster)
		if err != nil {
			t.Fatalf("openTokenIndex: %v", err)
		}
		t.Cleanup(func() { _ = x.Close() })
		const tok, other = "tok-3f9a7c1e5d2b4a60-secret", "tok-0000-another"
		if err := x.put(tok, "acme"); err != nil {
			t.Fatal(err)
		}
		if err := x.put(other, "globex"); err != nil {
			t.Fatal(err)
		}

		held := rawIndex(t, x)
		if len(held) != 2 {
			t.Fatalf("index holds %d rows: %v", len(held), held)
		}
		for k := range held {
			if strings.Contains(k, tok[:8]) || strings.Contains(k, other[:8]) || k == tok || len(k) != 64 {
				t.Errorf("the index is keyed by %q: not a blind index", k)
			}
		}
		if held[x.blind(tok)] != "acme" || held[x.blind(other)] != "globex" {
			t.Fatalf("rows are not under their blind: %v", held)
		}

		if got, ok, err := x.org(tok); err != nil || !ok || got != "acme" {
			t.Fatalf("org(token) = %q, %v, %v", got, ok, err)
		}
		if _, ok, err := x.org("tok-never-minted"); err != nil || ok {
			t.Fatalf("an unknown token resolved: %v, %v", ok, err)
		}
		// The blind itself is not a token: presenting a stored key opens nothing.
		for k := range held {
			if _, ok, err := x.org(k); err != nil || ok {
				t.Fatalf("a stored key resolved as a token: %v, %v", ok, err)
			}
		}
		// Re-pointing keeps one row.
		if err := x.put(tok, "initech"); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := x.org(tok); got != "initech" || len(rawIndex(t, x)) != 2 {
			t.Fatalf("re-pointing: org=%q rows=%d", got, len(rawIndex(t, x)))
		}
	})
}

func TestTheIndexDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if x, err := openTokenIndex(t.TempDir(), master); err == nil {
				_ = x.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A file from before the tokens were blinded keys its rows by the token itself. It is
// built by hand (the rows are plaintext because the code that wrote them was), so this
// stays on SQLite: hanzo-sql has no such file.
func TestALegacyIndexIsRekeyedAndStillResolves(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	db, err := locus.Open(namespace.System(), "token_index", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE token_index (token TEXT PRIMARY KEY, org TEXT NOT NULL)`,
		`INSERT INTO token_index (token, org) VALUES ('legacy-token-aaaa', 'acme')`,
		`INSERT INTO token_index (token, org) VALUES ('legacy-token-bbbb', 'globex')`,
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		x, err := openTokenIndex(dir, testMaster)
		if err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		if got, ok, err := x.org("legacy-token-aaaa"); err != nil || !ok || got != "acme" {
			t.Fatalf("pass %d: org(aaaa) = %q, %v, %v", pass, got, ok, err)
		}
		if got, ok, err := x.org("legacy-token-bbbb"); err != nil || !ok || got != "globex" {
			t.Fatalf("pass %d: org(bbbb) = %q, %v, %v", pass, got, ok, err)
		}
		for k := range rawIndex(t, x) {
			if strings.Contains(k, "legacy") {
				t.Errorf("pass %d: the file still holds %q", pass, k)
			}
		}
		var n int
		if err := x.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'token_index_plain'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("pass %d: the plaintext table is still there: %d, %v", pass, n, err)
		}
		if err := x.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
