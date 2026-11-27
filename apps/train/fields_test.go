package train

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

func noteOf(t *testing.T, st *store, id string) string {
	t.Helper()
	var v string
	if err := st.db.QueryRow(`SELECT note FROM corrections WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read note of %s: %v", id, err)
	}
	return v
}

func corrected(t *testing.T, st *store, id, question, note string) {
	t.Helper()
	if err := st.correct(context.Background(), correction{ID: id, Decision: "dec_" + id, Question: question, Type: "class",
		Labels: []string{"a", "b"}, Answer: "a", Note: note, By: "u", At: 1}); err != nil {
		t.Fatalf("correct %s: %v", id, err)
	}
}

// What a person wrote to explain a correction rests as ciphertext on both engines, and
// every read gives it back; the answer a model learns from stays plain.
func TestACorrectionNoteIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		const note = "Ada Lovelace said this was a billing question, not a complaint"
		corrected(t, st, "c1", "intent", note)
		corrected(t, st, "c2", "intent", "")
		if raw := noteOf(t, st, "c1"); !strings.HasPrefix(raw, "seal:") || strings.Contains(raw, "Lovelace") {
			t.Fatalf("the note rests as %.30q, want ciphertext", raw)
		}
		if raw := noteOf(t, st, "c2"); raw != "" {
			t.Fatalf("an empty note rests as %q, want empty", raw)
		}
		var answer string
		if err := st.db.QueryRow(`SELECT answer FROM corrections WHERE id = 'c1'`).Scan(&answer); err != nil || answer != "a" {
			t.Fatalf("the answer is %q (%v), want it plain", answer, err)
		}
		cs, err := st.corrections(ctx, `ORDER BY id`)
		if err != nil || len(cs) != 2 || cs[0].Note != note || cs[1].Note != "" || cs[0].Answer != "a" {
			t.Fatalf("corrections = %+v, %v", cs, err)
		}
	})
}

// A note carried to another correction, or planted in the clear, is refused.
func TestANoteIsBoundToItsCorrection(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		corrected(t, st, "c1", "intent", "first")
		corrected(t, st, "c2", "intent", "second")
		if _, err := st.db.Exec(`UPDATE corrections SET note = $1 WHERE id = 'c2'`, noteOf(t, st, "c1")); err != nil {
			t.Fatal(err)
		}
		if _, err := st.corrections(ctx, ``); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("one correction's note opened on another: %v", err)
		}
		if _, err := st.db.Exec(`UPDATE corrections SET note = 'planted' WHERE id = 'c2'`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.corrections(ctx, ``); !errors.Is(err, tenure.ErrFieldPlain) {
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
		corrected(t, acme, "c1", "intent", "private")
		corrected(t, globex, "c1", "intent", "other")
		if _, err := globex.db.Exec(`UPDATE corrections SET note = $1`, noteOf(t, acme, "c1")); err != nil {
			t.Fatal(err)
		}
		if _, err := globex.corrections(ctx, ``); !errors.Is(err, tenure.ErrFieldAuth) {
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
	open := func() *store {
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
	if _, err := st.db.Exec(`INSERT INTO corrections (id, decision, question, answer, note, by, at) VALUES ('old', 'dec_old', 'intent', 'a', 'old note', 'u', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		st = open()
		if cs, err := st.corrections(context.Background(), ``); err != nil || len(cs) != 1 || cs[0].Note != "old note" {
			t.Fatalf("pass %d: a legacy correction = %+v, %v", pass, cs, err)
		}
		if raw := noteOf(t, st, "old"); !strings.HasPrefix(raw, "seal:") {
			t.Fatalf("pass %d: the note is still %.20q after the upgrade", pass, raw)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
