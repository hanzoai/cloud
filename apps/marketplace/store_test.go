package marketplace

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// TestLegacyCentsStoreOpens: a store written before prices were exact must still
// open, and its cents must arrive as the same money.
//
// This is not a nicety. Open is called from Mount, Mount's error fails the
// composition root, and the listings table is created by the FIRST release — so a
// migration that cannot run does not degrade the marketplace, it stops the binary
// on every deployment that ever published a listing. `CREATE TABLE IF NOT EXISTS`
// is a no-op against a table that already exists, which is exactly why the new
// column has to be added by the migration and cannot be assumed present.
func TestLegacyCentsStoreOpens(t *testing.T) {
	path := t.TempDir() + "/marketplace.db"
	db, err := cek.Open(cek.Global, path)
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

	s, err := Open(path)
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

	// The old column is gone, so nothing can read a second answer about the price.
	l, priced, err := s.CheapestPublicForTool(context.Background(), "t_paid")
	if err != nil || !priced {
		t.Fatalf("migrated listing must still be priced: priced=%v err=%v", priced, err)
	}
	if l.Price.String() != "7.5" {
		t.Fatalf("the price table reads %q after migration, want 7.5", l.Price.String())
	}

	// Re-opening a migrated store is a no-op, not a second migration.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open a migrated store: %v", err)
	}
	_ = again.Close()
}
