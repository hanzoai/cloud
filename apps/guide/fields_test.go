package guide

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
)

func noteOf(t *testing.T, st *Store, step string) string {
	t.Helper()
	var v string
	if err := st.db.QueryRow(`SELECT note FROM guide_progress WHERE step_id = $1`, step).Scan(&v); err != nil {
		t.Fatalf("read note of %s: %v", step, err)
	}
	return v
}

// What a person wrote about their own progress rests as ciphertext on both engines, and
// every read gives it back.
func TestAStepNoteIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		const note = "waiting on Ada Lovelace to sign the term sheet"
		if err := st.SetState(ctx, "s1", StateDone, "manual", note, 10); err != nil {
			t.Fatal(err)
		}
		if err := st.SetState(ctx, "s2", StateDone, "auto", "", 11); err != nil {
			t.Fatal(err)
		}
		if raw := noteOf(t, st, "s1"); !strings.HasPrefix(raw, "seal:") || strings.Contains(raw, "Lovelace") {
			t.Fatalf("the note rests as %.30q, want ciphertext", raw)
		}
		if raw := noteOf(t, st, "s2"); raw != "" {
			t.Fatalf("an empty note rests as %q, want empty", raw)
		}
		got, err := st.States(ctx)
		if err != nil || got["s1"].Note != note || got["s2"].Note != "" || got["s1"].State != StateDone {
			t.Fatalf("States = %+v, %v", got, err)
		}
		// An update seals again, under the same row.
		if err := st.SetState(ctx, "s1", StateDone, "manual", "signed", 12); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.States(ctx); got["s1"].Note != "signed" {
			t.Fatalf("the updated note reads %q", got["s1"].Note)
		}
	})
}

// A note carried to another step, or planted in the clear, is refused.
func TestANoteIsBoundToItsStep(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		for _, id := range []string{"a", "b"} {
			if err := st.SetState(ctx, id, StateDone, "manual", "note "+id, 1); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.db.Exec(`UPDATE guide_progress SET note = $1 WHERE step_id = 'b'`, noteOf(t, st, "a")); err != nil {
			t.Fatal(err)
		}
		if _, err := st.States(ctx); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("one step's note opened on another: %v", err)
		}
		if _, err := st.db.Exec(`UPDATE guide_progress SET note = 'planted' WHERE step_id = 'b'`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.States(ctx); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
	})
}

// A note is bound to its org, and the store does not open without a key.
func TestANoteBelongsToItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		stores := cloud.NewOrgStoreAt(cloud.Base{DataDir: t.TempDir()}, subsystem, openStore(testMaster))
		t.Cleanup(func() { _ = stores.CloseAll() })
		acme, err := stores.For(principal.MustOrgNamespace("acme", ""))
		if err != nil {
			t.Fatal(err)
		}
		globex, err := stores.For(principal.MustOrgNamespace("globex", ""))
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range []*Store{acme, globex} {
			if err := st.SetState(ctx, "s", StateDone, "manual", "private", 1); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := globex.db.Exec(`UPDATE guide_progress SET note = $1`, noteOf(t, acme, "s")); err != nil {
			t.Fatal(err)
		}
		if _, err := globex.States(ctx); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("acme's note opened in globex's store: %v", err)
		}
		for _, master := range [][]byte{nil, testMaster[:31]} {
			none := cloud.NewOrgStoreAt(cloud.Base{DataDir: t.TempDir()}, subsystem, openStore(master))
			if _, err := none.For(principal.MustOrgNamespace("acme", "")); err == nil {
				t.Fatalf("opened with a %d-byte master", len(master))
			}
			_ = none.CloseAll()
		}
	})
}

// A SQLite file written before the note was sealed holds plaintext. It is built by hand
// (the rows are plaintext because the code that wrote them was), so this stays on SQLite.
func TestALegacyNoteIsSealedInPlace(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	ns := principal.MustOrgNamespace("acme", "")
	open := func() *Store {
		t.Helper()
		db, err := cloud.OrgDB(dir, ns, subsystem)
		if err != nil {
			t.Fatal(err)
		}
		st, err := openStore(testMaster)(ns, db)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return st
	}
	st := open()
	if _, err := st.db.Exec(`INSERT INTO guide_progress (step_id, state, source, note, updated_at) VALUES ('old', 'done', 'manual', 'old note', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		st = open()
		if got, err := st.States(context.Background()); err != nil || got["old"].Note != "old note" {
			t.Fatalf("pass %d: a legacy note = %+v, %v", pass, got, err)
		}
		if raw := noteOf(t, st, "old"); !strings.HasPrefix(raw, "seal:") {
			t.Fatalf("pass %d: the note is still %.20q after the upgrade", pass, raw)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
