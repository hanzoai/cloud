package reference

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/namespace"

	_ "github.com/hanzoai/sqlite" // registers "sqlite" under both build tags
)

var wrote = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func openOn(t *testing.T, o sqltest.Open) *overrides {
	t.Helper()
	s, err := openOverrides(o(namespace.MustOrg("acme"), subsystem))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func keysOf(es []ReferenceOverride) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Key
	}
	return out
}

func TestAnOverrideIsWrittenReadAndCleared(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, o sqltest.Open) {
		s := openOn(t, o)
		n, err := s.put("domain", []ReferenceOverride{
			{Key: "b.example", Verdict: Deny, Note: "spam"},
			{Key: "a.example", Verdict: Allow},
			{Key: "A.example", Verdict: Allow},
		}, "u1", wrote)
		if err != nil || n != 3 {
			t.Fatalf("put = %d, %v", n, err)
		}
		if c, err := s.count("domain"); err != nil || c != 3 {
			t.Fatalf("count = %d, %v", c, err)
		}
		if c, _ := s.count("net"); c != 0 {
			t.Fatalf("another set holds %d", c)
		}
		// Pages in the order of the key's bytes, whichever collation the database has.
		got, err := s.list("domain", "", 10)
		if err != nil || !slices.Equal(keysOf(got), []string{"A.example", "a.example", "b.example"}) {
			t.Fatalf("list = %v, %v", keysOf(got), err)
		}
		if got[2].Verdict != Deny || got[2].Note != "spam" || got[2].By != "u1" || got[2].At != wrote.Format(time.RFC3339) {
			t.Fatalf("a row came back as %+v", got[2])
		}
		if page, _ := s.list("domain", "a.example", 10); !slices.Equal(keysOf(page), []string{"b.example"}) {
			t.Fatalf("after a.example: %v", keysOf(page))
		}
		// An overwrite replaces and does not add.
		if _, err := s.put("domain", []ReferenceOverride{{Key: "b.example", Verdict: Allow, Note: "ok"}}, "u2", wrote); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.count("domain"); c != 3 {
			t.Fatalf("an overwrite changed the count to %d", c)
		}
		e, ok, err := s.pick("domain", []string{"x.b.example", "b.example", "example"})
		if err != nil || !ok || e.Key != "b.example" || e.Verdict != Allow || e.By != "u2" {
			t.Fatalf("pick = %+v, %v, %v", e, ok, err)
		}
		if _, ok, _ := s.pick("domain", []string{"nothing.example"}); ok {
			t.Fatal("picked an override that is not there")
		}
		if gone, err := s.clear("domain", "b.example"); err != nil || !gone {
			t.Fatalf("clear = %v, %v", gone, err)
		}
		if gone, _ := s.clear("domain", "b.example"); gone {
			t.Fatal("cleared twice")
		}
	})
}

func TestTheWriterIsBoundedAndIsText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, o sqltest.Open) {
		s := openOn(t, o)
		one := []ReferenceOverride{{Key: "a.example", Verdict: Deny}}
		if _, err := s.put("domain", one, strings.Repeat("u", maxActor), wrote); err != nil {
			t.Fatalf("a writer at the bound: %v", err)
		}
		for name, by := range map[string]string{"too long": strings.Repeat("u", maxActor+1), "a NUL": "u\x00", "not UTF-8": "u\xff"} {
			if _, err := s.put("domain", one, by, wrote); !errors.Is(err, errActor) {
				t.Errorf("a writer %s: %v, want errActor", name, err)
			}
		}
	})
}

// Text a column cannot hold on hanzo-sql is refused on both engines, and reads of it
// find nothing instead of failing.
func TestWhatIsNotTextIsRefusedAndFindsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, o sqltest.Open) {
		s := openOn(t, o)
		for _, bad := range []string{"a\x00b", "a\xffb"} {
			if _, err := s.put("domain", []ReferenceOverride{{Key: bad, Verdict: Deny}}, "u", wrote); err == nil {
				t.Errorf("a key of %q was stored", bad)
			}
			if _, err := s.put("domain", []ReferenceOverride{{Key: "a", Verdict: Deny, Note: bad}}, "u", wrote); err == nil {
				t.Errorf("a note of %q was stored", bad)
			}
			if got, err := s.list("domain", bad, 10); err != nil || len(got) != 0 {
				t.Errorf("a cursor of %q: %v, %v", bad, got, err)
			}
			if _, ok, err := s.pick("domain", []string{bad, "x"}); err != nil || ok {
				t.Errorf("a candidate of %q: %v, %v", bad, ok, err)
			}
			if gone, err := s.clear("domain", bad); err != nil || gone {
				t.Errorf("clearing %q: %v, %v", bad, gone, err)
			}
		}
		if c, _ := s.count("domain"); c != 0 {
			t.Fatalf("%d entries after refusals", c)
		}
	})
}

// A file in production is the table as the first build made it, and SQLite is where
// it is. It opens, keeps every row, and answers.
func TestALegacyFileKeepsItsOverrides(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "reference.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE override (
    "set"   TEXT NOT NULL,
    key     TEXT NOT NULL,
    verdict TEXT NOT NULL,
    note    TEXT NOT NULL DEFAULT '',
    at      TEXT NOT NULL,
    by      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY ("set", key)
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO override ("set", key, verdict, note, at, by) VALUES ('domain', 'old.example', 'deny', 'why', '2026-01-01T00:00:00Z', 'u0')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := openOverrides(db)
		if err != nil {
			t.Fatal(err)
		}
		e, ok, err := s.pick("domain", []string{"old.example"})
		if err != nil || !ok || e.Verdict != Deny || e.Note != "why" || e.By != "u0" {
			t.Fatalf("pick = %+v, %v, %v", e, ok, err)
		}
	}
}
