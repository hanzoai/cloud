package translate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// TestMemorySeparatesModelFormatAndSource: the same text through another model or
// another format is another entry; two declared source languages are two entries;
// an entry made with the source detected answers a call that declares one, and an
// entry made under a declared source never answers a call that declares none.
func TestMemorySeparatesModelFormatAndSource(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper}
		app := mount(t, m)
		translate(t, app, "acme", Brief{Text: "Gift", Target: "es"})
		for name, b := range map[string]Brief{
			"another model":  {Text: "Gift", Target: "es", Model: "acme-llama"},
			"another format": {Text: "Gift", Target: "es", Format: FormatHTML},
		} {
			if out := translate(t, app, "acme", b); out.Translations[0].Cached {
				t.Errorf("%s read the default entry: %+v", name, out.Translations[0])
			}
		}
		if out := translate(t, app, "acme", Brief{Text: "Gift", Target: "es", Source: "de"}); !out.Translations[0].Cached {
			t.Error("an entry made with the source detected must answer a call that declares one")
		}

		// A model's own rendering, reviewed, answers that model and no other.
		if code, raw := call(t, app, http.MethodPut, "/v1/translate/memory", "acme", ReviewRequest{
			Source: "Gift", Target: "es", Model: "acme-llama", Text: "Regalo", State: StateApproved,
		}); code != http.StatusOK {
			t.Fatalf("approve: %d %s", code, raw)
		}
		if out := translate(t, app, "acme", Brief{Text: "Gift", Target: "es", Model: "acme-llama"}); out.Translations[0].Text != "Regalo" {
			t.Errorf("the model's reviewed entry = %+v", out.Translations[0])
		}
		if out := translate(t, app, "acme", Brief{Text: "Gift", Target: "es"}); out.Translations[0].Text == "Regalo" {
			t.Error("another model's reviewed entry answered the default model")
		}

		// Declared languages are distinct entries.
		translate(t, app, "acme", Brief{Text: "Bank", Target: "es", Source: "de"})
		if out := translate(t, app, "acme", Brief{Text: "Bank", Target: "es", Source: "en"}); out.Translations[0].Cached {
			t.Error("a German source answered an English one")
		}
		if out := translate(t, app, "acme", Brief{Text: "Bank", Target: "es"}); out.Translations[0].Cached {
			t.Error("an entry made under a declared source answered a call that declares none")
		}
	})
}

// oldKey is the key before it carried the source language, format and model:
// (source, target, glossary version, tier), NUL-separated, sha256.
func oldKey(source, target, glossary string, tier Tier) string {
	h := sha256.New()
	for _, part := range []string{source, target, glossary, string(tier)} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestAnOldMemoryMovesToTheNewKey opens a file in the shape the memory had before
// the key grew, and proves every row answers under the key it has now — no source
// declared, text, the default model — with its text, state and actor intact; that
// a row an older binary writes afterwards moves on the next open; and that a human
// row meeting a machine row under its new key wins.
func TestAnOldMemoryMovesToTheNewKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		db, err := cloud.OrgDB(t.TempDir(), principal.MustOrgNamespace("acme", ""), "translate")
		if err != nil {
			t.Fatalf("org db: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		ctx := context.Background()
		if _, err := db.Exec(`
CREATE TABLE entry (
  key        TEXT PRIMARY KEY,
  source     TEXT NOT NULL,
  target     TEXT NOT NULL,
  glossary   TEXT NOT NULL DEFAULT '',
  tier       TEXT NOT NULL,
  text       TEXT NOT NULL,
  state      TEXT NOT NULL DEFAULT 'machine',
  actor      TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
CREATE INDEX ix_entry_target ON entry(target, updated_at DESC);
CREATE INDEX ix_entry_state ON entry(state, updated_at DESC);`); err != nil {
			t.Fatalf("old shape: %v", err)
		}
		old := func(source string, tier Tier, text string, st State, actor string, at int64) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO entry (key, source, target, glossary, tier, text, state, actor, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, oldKey(source, "es", "", tier), source, "es", "", string(tier), text, string(st), actor, at); err != nil {
				t.Fatalf("old row: %v", err)
			}
		}
		old("Hello", TierQuality, "¡Hola!", StateApproved, "u-acme", 5)
		old("Hello", TierBulk, "madlad:Hola", StateMachine, "", 6)

		mem, err := open(db)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		now := func(source string, tier Tier) MemoryEntry {
			return MemoryEntry{Source: source, Target: "es", Tier: tier, Format: FormatText}
		}
		got, found, err := mem.get(ctx, now("Hello", TierQuality).key())
		if err != nil || !found || got.Text != "¡Hola!" || got.State != StateApproved || got.Actor != "u-acme" || got.UpdatedAt != 5 {
			t.Fatalf("moved quality row = %+v found=%v err=%v", got, found, err)
		}
		if got, found, _ := mem.get(ctx, now("Hello", TierBulk).key()); !found || got.Text != "madlad:Hola" || got.Format != FormatText || got.Model != "" {
			t.Fatalf("moved bulk row = %+v found=%v", got, found)
		}
		if got, found, _ := mem.recall(ctx, MemoryEntry{Source: "Hello", Lang: "en", Target: "es", Tier: TierQuality, Format: FormatText}); !found || got.Text != "¡Hola!" {
			t.Fatalf("a call declaring its source lost the approved row: %+v found=%v", got, found)
		}
		if n := count(t, db); n != 6 {
			t.Fatalf("rows = %d after the move, want 6 (each old row under three formats, none under an old key)", n)
		}

		// An older binary, mid-rollout, writes the old shape: a human row, under the old
		// key, for a string the new binary has a machine row for.
		if err := mem.put(ctx, MemoryEntry{Source: "Bye", Target: "es", Tier: TierQuality, Format: FormatText, Text: "ADIOS", State: StateMachine, UpdatedAt: 7}, false); err != nil {
			t.Fatalf("put: %v", err)
		}
		old("Bye", TierQuality, "¡Adiós!", StateApproved, "u-acme", 8)
		if _, err := open(db); err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if got, _, _ := mem.get(ctx, now("Bye", TierQuality).key()); got.Text != "¡Adiós!" || got.State != StateApproved {
			t.Fatalf("the reviewed row lost to the machine row it met: %+v", got)
		}
		if n := count(t, db); n != 9 {
			t.Fatalf("rows = %d, want 9", n)
		}
		// Opening again moves nothing: the migration is idempotent.
		if _, err := open(db); err != nil {
			t.Fatalf("third open: %v", err)
		}
		if n := count(t, db); n != 9 {
			t.Fatalf("rows = %d after an idempotent open, want 9", n)
		}
	})
}

func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entry`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestAMigrationKilledMidwayLosesNothing: a process stopped after one batch, and
// a batch that fails part way, leave every row either moved whole or not at all,
// and the next open finishes the move with every row under every format, its
// state intact.
func TestAMigrationKilledMidwayLosesNothing(t *testing.T) {
	dir := t.TempDir()
	ns := principal.MustOrgNamespace("acme", "")
	db, err := cloud.OrgDB(dir, ns, "translate")
	if err != nil {
		t.Fatal(err)
	}
	oldShape(t, db)
	const total = 3*rekeyBatch + 7
	for i := range total {
		src := fmt.Sprintf("s%04d", i)
		st := StateMachine
		if i%3 == 0 {
			st = StateApproved
		}
		oldRow(t, db, src, "T"+src, st, int64(i))
	}
	for _, c := range grown {
		if _, err := db.Exec(`ALTER TABLE entry ADD COLUMN ` + c.ddl); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	m := &memory{db: db}
	if n, err := m.rekeyBatch(ctx); err != nil || n != rekeyBatch {
		t.Fatalf("one batch = %d, %v", n, err)
	}
	if moved, left := where(t, db, "format IS NOT NULL"), where(t, db, "format IS NULL"); moved != 3*rekeyBatch || left != total-rekeyBatch {
		t.Fatalf("after one batch: %d moved rows, %d left", moved, left)
	}

	var victim string
	if err := db.QueryRow(`SELECT source FROM entry WHERE format IS NULL LIMIT 1 OFFSET 250`).Scan(&victim); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON entry WHEN NEW.format='markdown' AND NEW.source='` + victim + `' BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	before := where(t, db, "format IS NOT NULL")
	if _, err := m.rekeyBatch(ctx); err == nil {
		t.Fatal("a batch that failed part way reported success")
	}
	if after := where(t, db, "format IS NOT NULL"); after != before {
		t.Fatalf("a failed batch left %d rows half moved", after-before)
	}
	if _, err := db.Exec(`DROP TRIGGER boom`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	for round := 1; round <= 2; round++ {
		db, err := cloud.OrgDB(dir, ns, "translate")
		if err != nil {
			t.Fatal(err)
		}
		mem, err := open(db)
		if err != nil {
			t.Fatal(err)
		}
		for i := range total {
			src := fmt.Sprintf("s%04d", i)
			want := StateMachine
			if i%3 == 0 {
				want = StateApproved
			}
			for _, f := range formats {
				got, found, err := mem.recall(ctx, MemoryEntry{Source: src, Target: "es", Tier: TierQuality, Format: f})
				if err != nil || !found || got.State != want || got.Text != "T"+src {
					t.Fatalf("open %d: %s as %s = %+v found=%v err=%v", round, src, f, got, found, err)
				}
			}
		}
		if left := where(t, db, "format IS NULL"); left != 0 {
			t.Fatalf("open %d left %d rows unmoved", round, left)
		}
		_ = db.Close()
	}
}

// TestMovingNeverUndoesNewerWork: an old approved row meeting a newer published
// one under its new key leaves the published one; and a call naming the default
// model explicitly reads what was made without naming it.
func TestMovingNeverUndoesNewerWork(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		db, err := cloud.OrgDB(t.TempDir(), principal.MustOrgNamespace("acme", ""), "translate")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mem, err := open(db)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		newer := MemoryEntry{Source: "Pay", Target: "es", Tier: TierQuality, Format: FormatText, Text: "NEW", State: StatePublished, UpdatedAt: 100}
		if err := mem.put(ctx, newer, true); err != nil {
			t.Fatal(err)
		}
		oldRow(t, db, "Pay", "OLD", StateApproved, 5)
		if _, err := open(db); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := mem.get(ctx, newer.key()); got.Text != "NEW" || got.State != StatePublished {
			t.Fatalf("moving an older approved row undid a newer published one: %+v", got)
		}
		if m, err := modelOf(TierQuality, aimodel.DefaultModel); err != nil || m != "" {
			t.Fatalf("modelOf(quality, %q) = %q, %v; want the default's key", aimodel.DefaultModel, m, err)
		}

		app := mount(t, &model{render: upper})
		translate(t, app, "acme", Brief{Text: "Hello", Target: "es"})
		if out := translate(t, app, "acme", Brief{Text: "Hello", Target: "es", Model: aimodel.DefaultModel}); !out.Translations[0].Cached {
			t.Fatalf("naming the default model missed what was made without naming it: %+v", out.Translations[0])
		}
	})
}

// oldShape makes entry the shape it had before the key grew.
func oldShape(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS entry (key TEXT PRIMARY KEY, source TEXT NOT NULL, target TEXT NOT NULL, glossary TEXT NOT NULL DEFAULT '',
 tier TEXT NOT NULL, text TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'machine', actor TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
}

// oldRow writes a quality row to es as a binary before the key grew writes it.
func oldRow(t *testing.T, db *sql.DB, source, text string, st State, at int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO entry (key, source, target, glossary, tier, text, state, actor, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		oldKey(source, "es", "", TierQuality), source, "es", "", string(TierQuality), text, string(st), "u", at); err != nil {
		t.Fatal(err)
	}
}

func where(t *testing.T, db *sql.DB, cond string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entry WHERE ` + cond).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
