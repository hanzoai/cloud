package sqlpool

import (
	"database/sql"
	"testing"

	"github.com/hanzoai/cek"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

func TestSinglePinsThePool(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != 0 {
		t.Fatalf("precondition: pool already capped at %d", got)
	}
	Single(db)
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

// This is the test that lets ~50 stores delete their PRAGMA blocks.
//
// Every one of them opened a database and then re-applied busy_timeout,
// journal_mode=WAL and foreign_keys=ON by hand. hanzoai/sqlite already applies
// those — per CONNECTION, which the hand-rolled db.Exec did not: a one-shot Exec
// lands on whichever connection serves it and is lost the moment that connection
// is recycled. So the blocks were not just duplicated, they were the weaker of
// the two mechanisms.
//
// Deleting them is only safe while this holds, so assert it here rather than
// trusting a changelog. If a future hanzoai/sqlite stops applying a default, this
// goes red in ONE place instead of corrupting fifty stores in silence.
func TestCekOpenAlreadyCarriesTheDefaultsStoresUsedToSetByHand(t *testing.T) {
	db, err := cek.Open(namespace.MustOrgProject("acme", ""), "widget", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// PRAGMA readback is numeric for the boolean/enum pragmas: foreign_keys 1 = ON,
	// synchronous 1 = NORMAL. journal_mode answers with its name.
	for _, want := range []struct{ pragma, value string }{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"synchronous", "1"},
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + want.pragma).Scan(&got); err != nil {
			t.Errorf("PRAGMA %s: %v", want.pragma, err)
			continue
		}
		if got != want.value {
			t.Errorf("PRAGMA %s = %s, want %s — a store somewhere is now relying on a default the driver stopped setting", want.pragma, got, want.value)
		}
	}

	// The stores set busy_timeout=5000. The driver sets its own; require only that
	// there IS a wait, because the number is the driver's to choose and a longer
	// one is strictly safer than the value the stores hard-coded.
	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy < 5000 {
		t.Errorf("busy_timeout = %d, want at least the 5000 the stores used to set", busy)
	}
}
