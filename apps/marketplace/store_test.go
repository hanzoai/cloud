package marketplace

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// TestLegacyCentsStoreOpens: a store written before prices were exact must still
// open, and its cents must arrive as the same money — and a tool listing written
// before a tool's ownership was proved leaves the shop.
//
// This is not a nicety. Open is called from Mount, Mount's error fails the
// composition root, and the listings table is created by the FIRST release — so a
// migration that cannot run does not degrade the marketplace, it stops the binary
// on every deployment that ever published a listing. `CREATE TABLE IF NOT EXISTS`
// is a no-op against a table that already exists, which is exactly why the new
// column has to be added by the migration and cannot be assumed present.
func TestLegacyCentsStoreOpens(t *testing.T) {
	// A file written by an older release exists on SQLite alone.
	t.Setenv(locus.Env, "")
	// Seeded at the SAME (namespace, subsystem, dir) Open uses, so this IS the file
	// Open reads back.
	dir := t.TempDir()
	db, err := cek.Open(namespace.System(), "marketplace", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE listings (
  id TEXT NOT NULL, publisher_org TEXT NOT NULL, tool TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '', price_cents INTEGER NOT NULL DEFAULT 0,
  currency TEXT NOT NULL DEFAULT 'USD', recipient TEXT NOT NULL DEFAULT '',
  public INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
  PRIMARY KEY (publisher_org, id));
INSERT INTO listings VALUES ('lst_paid','acme','t_paid','Paid','','',750,'USD','wal_1',1,1);
INSERT INTO listings VALUES ('lst_free','acme','t_free','Free','','',0,'USD','',1,2);`); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := Open(dir, testMaster, testKMS)
	if err != nil {
		t.Fatalf("Open a legacy store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	rows, err := s.ListByOrg(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ListByOrg: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("legacy rows survived: %d, want 2", len(rows))
	}
	byTool := map[string]string{}
	for _, l := range rows {
		byTool[l.Tool] = l.Price.String()
	}
	if got := byTool["t_paid"]; got != "7.5" {
		t.Fatalf("750 legacy cents migrated to %q, want 7.5", got)
	}
	if got := byTool["t_free"]; got != "0" {
		t.Fatalf("a free legacy listing migrated to %q, want 0", got)
	}

	// A tool listing from before ownership was proved — any org's, keyed by a name —
	// is withdrawn from the shop and prices no call: it was a toll on a tool its
	// publisher never owned. Its publisher still reads it, price and all.
	for _, l := range rows {
		if l.Public {
			t.Fatalf("an unproved tool listing %s is still in the shop", l.ID)
		}
	}
	for _, resource := range []string{"t_paid", "tool:connector:t_paid"} {
		if _, priced, err := s.CheapestPublicForTool(context.Background(), resource); err != nil || priced {
			t.Fatalf("an unproved listing prices %s: priced=%v err=%v", resource, priced, err)
		}
	}

	// Re-opening a migrated store is a no-op, not a second migration.
	again, err := Open(dir, testMaster, testKMS)
	if err != nil {
		t.Fatalf("re-Open a migrated store: %v", err)
	}
	_ = again.Close()
}

// The shop finds a listing by its own words, whatever their case and whatever
// script: they are folded in Go when written, as a query's words are when asked,
// so "Élan" finds "Élan" and "élan" does too. A listing written before the folded
// words existed is folded when the store opens, and an edit folds again.
func TestTheShopFoldsWordsAsItsBuyersDo(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	db, err := cek.Open(namespace.System(), "marketplace", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE listings (
  id TEXT NOT NULL, publisher_org TEXT NOT NULL, tool TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '', price TEXT NOT NULL DEFAULT '0',
  currency TEXT NOT NULL DEFAULT 'USD', recipient TEXT NOT NULL DEFAULT '',
  public INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'tool', ref TEXT NOT NULL DEFAULT '', docs TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (publisher_org, id));
INSERT INTO listings (id, publisher_org, tool, title, description, public, created_at, kind)
  VALUES ('lst_old','acme','analyste','Élan Analyste','Über Straße',1,1,'agent');`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, testMaster, testKMS)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	found := func(q string) int {
		t.Helper()
		_, n, _, err := s.catalog(ctx, search{words: strings.Fields(strings.ToLower(q))}, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, q := range []string{"Élan", "élan", "ÉLAN", "straße", "ÜBER", "analyste"} {
		if n := found(q); n != 1 {
			t.Fatalf("%q found %d, want the listing written before words were folded", q, n)
		}
	}
	l, ok, err := s.Get(ctx, "acme", "lst_old")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	l.Title = "Ödön Øresund"
	if _, err := s.Update(ctx, l); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Create(ctx, Listing{PublisherOrg: "globex", Kind: KindAgent, Tool: "ŒUVRE", Title: "Çà et là", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"ödön": 1, "ØRESUND": 1, "élan": 0, "œuvre": 1, "ÇÀ": 1} {
		if n := found(q); n != want {
			t.Fatalf("%q found %d, want %d (%s)", q, n, want, fresh.ID)
		}
	}
}

// A word the shop is asked for is text, not a pattern: a percent sign or an underscore
// in a query matches that character and no other, on either engine, and a word is found
// in a listing's own case-folded words whatever script they are in.
func TestTheShopSearchesWordsAsText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := Open(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		for _, l := range []Listing{
			{PublisherOrg: "acme", Kind: KindAgent, Tool: "analyste", Title: "Élan Analyste", Public: true},
			{PublisherOrg: "acme", Kind: KindApp, Tool: "pure", Title: "100% pure", Public: true},
			{PublisherOrg: "acme", Kind: KindSkill, Tool: "snake", Title: "snake_case tool", Public: true},
			{PublisherOrg: "acme", Kind: KindSkill, Tool: "plain", Title: "plain tool", Public: true},
		} {
			if _, err := s.Create(ctx, l); err != nil {
				t.Fatalf("create %s: %v", l.Title, err)
			}
		}
		for q, want := range map[string]int{
			"élan": 1, "100%": 1, "%": 1, "_": 1, "snake_case": 1, "s_ake": 0, "snake": 1, "tool": 2, "pure": 1, "100": 1, "nothing": 0,
			"acme": 4, "\\": 0,
		} {
			_, n, _, err := s.catalog(ctx, search{words: strings.Fields(q)}, 10, 0)
			if err != nil {
				t.Fatalf("%q: %v", q, err)
			}
			if n != want {
				t.Errorf("%q found %d, want %d", q, n, want)
			}
		}
	})
}

// Text SQLite stores and PostgreSQL's TEXT refuses (a NUL, bytes that are not UTF-8) is
// refused at the store on both, and nothing of it is written.
func TestTextTheEnginesDisagreeOnIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := Open(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		for name, title := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			if _, err := s.Create(ctx, Listing{PublisherOrg: "acme", Kind: KindApp, Tool: "t", Title: title}); err == nil {
				t.Errorf("a title with %s was written", name)
			}
		}
		fine, err := s.Create(ctx, Listing{PublisherOrg: "acme", Kind: KindApp, Tool: "t", Title: "fine"})
		if err != nil {
			t.Fatal(err)
		}
		fine.Description = "a\x00b"
		if _, err := s.Update(ctx, fine); err == nil {
			t.Error("a description with a NUL was written")
		}
		if n, err := s.counted(ctx, "acme"); err != nil || n != 1 {
			t.Errorf("acme holds %d listings (%v), want the one that was fine", n, err)
		}
		job := jobRow{Job: Job{ID: "job_1", Title: "t\x00", BuyerOrg: "b", SellerOrg: "s", Status: StatusOpen}}
		if err := s.putJob(ctx, job, "b", func() error { return nil }); err == nil {
			t.Error("a job with a NUL in its title was written")
		}
	})
}
