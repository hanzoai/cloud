package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

const (
	reason = "clawback of pay_1: Alice Smith disputed the charge from her card ending 4242"
	other  = "restore of e_a: the customer asked in writing, ticket 8841"
)

// memoed writes one balanced entry carrying memo.
func memoed(t *testing.T, s *Store, id, kind, ref, memo string, at int64) {
	t.Helper()
	if err := s.Tx(context.Background(), func(tx ledger.Tx) error {
		return tx.Insert(ledger.JournalEntry{
			ID: id, Kind: kind, Ref: ref, Memo: memo, Amount: money.FromCents(100), CreatedAt: at,
		}, []ledger.Posting{
			{Account: "wallet:a", Amount: money.FromCents(-100)},
			{Account: "revenue:platform", Amount: money.FromCents(100)},
		})
	}); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// stored is the memo column of one entry as the engine holds it.
func stored(t *testing.T, s *Store, id string) string {
	t.Helper()
	var memo string
	if err := s.db.QueryRow(`SELECT memo FROM treasury_entries WHERE id = $1`, id).Scan(&memo); err != nil {
		t.Fatalf("read memo of %s: %v", id, err)
	}
	return memo
}

// The memo is a person's words about a person's money: it rests as ciphertext on both
// engines, and every read the ledger offers returns it whole.
func TestMemoRestsSealedAndReadsBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := openOn(t, open, "usage")
		memoed(t, s, "e_a", "reverse", "r_a", reason, 10)
		memoed(t, s, "e_b", "usage", "r_b", "zen-4", 20)
		memoed(t, s, "e_c", "deposit", "r_c", "", 30)

		raw := stored(t, s, "e_a")
		if !strings.HasPrefix(raw, "seal:") || strings.Contains(raw, "Alice") || raw == reason {
			t.Fatalf("the memo rests as %q, want ciphertext", raw)
		}
		if raw2 := stored(t, s, "e_b"); !strings.HasPrefix(raw2, "seal:") || strings.Contains(raw2, "zen-4") {
			t.Fatalf("a short memo rests as %q, want ciphertext", raw2)
		}
		if empty := stored(t, s, "e_c"); empty != "" {
			t.Fatalf("an empty memo rests as %q, want empty", empty)
		}

		all, err := s.Entries(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		memos := map[string]string{}
		for _, e := range all {
			memos[e.ID] = e.Memo
		}
		if memos["e_a"] != reason || memos["e_b"] != "zen-4" || memos["e_c"] != "" {
			t.Fatalf("Entries memos = %q", memos)
		}
		kind, err := s.EntriesOfKind(ctx, "reverse")
		if err != nil || len(kind) != 1 || kind[0].Memo != reason {
			t.Fatalf("EntriesOfKind = %+v, %v", kind, err)
		}
		on, err := s.EntriesOn(ctx, 0, "wallet:a")
		if err != nil || len(on) != 3 {
			t.Fatalf("EntriesOn = %d, %v", len(on), err)
		}
		if err := s.Tx(ctx, func(tx ledger.Tx) error {
			byRef, ok, err := tx.EntryByRef("reverse", "", "r_a")
			if err != nil || !ok || byRef.Memo != reason {
				t.Fatalf("EntryByRef = %+v %v %v", byRef, ok, err)
			}
			byID, ok, err := tx.EntryByID("e_b")
			if err != nil || !ok || byID.Memo != "zen-4" {
				t.Fatalf("EntryByID = %+v %v %v", byID, ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// A debit posted before the usage log existed is listed from the journal, with its
		// memo as the model it was billed for.
		uses, err := s.Uses(ctx, "usage", 0)
		if err != nil || len(uses) != 1 || uses[0].Model != "zen-4" {
			t.Fatalf("Uses = %+v, %v", uses, err)
		}
	})
}

// A ciphertext is bound to its entry: carried to another entry, or replaced by a value
// written around the store, it is refused rather than read as that entry's memo.
func TestMemoIsBoundToItsEntry(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := openOn(t, open, "")
		memoed(t, s, "e_a", "reverse", "r_a", reason, 10)
		memoed(t, s, "e_b", "reverse", "r_b", other, 20)

		if _, err := s.db.Exec(`UPDATE treasury_entries SET memo = $1 WHERE id = 'e_b'`, stored(t, s, "e_a")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Entries(ctx, 0); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a memo carried to another entry read as %v, want ErrFieldAuth", err)
		}
		if _, err := s.db.Exec(`UPDATE treasury_entries SET memo = 'plain words' WHERE id = 'e_b'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Entries(ctx, 0); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a plaintext memo read as %v, want ErrFieldPlain", err)
		}
	})
}

// The ledger does not open without a key, and the key is the org's: a book opened under
// another org's namespace cannot read this one's memos.
func TestMemoNeedsTheOrgsKey(t *testing.T) {
	for _, master := range [][]byte{nil, []byte("short")} {
		if _, err := Opener(master, "treasury", "")(namespace.System(), nil); err == nil {
			t.Fatalf("a ledger opened with a %d-byte master", len(master))
		}
	}
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := openOn(t, open, "")
		memoed(t, s, "e_a", "reverse", "r_a", reason, 10)

		ns, err := namespace.Org("acme")
		if err != nil {
			t.Fatal(err)
		}
		c, err := tenure.NewCipher(testMaster)
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := c.Fields(ns, "treasury")
		if err != nil {
			t.Fatal(err)
		}
		s.fields = foreign
		if _, err := s.Entries(ctx, 0); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("another org's key read the memo as %v, want ErrFieldAuth", err)
		}
	})
}

// A SQLite file whose memos were written in the clear is sealed in place by the open, and
// a second open leaves the bytes as they are.
func TestPlainMemosAreSealedOnOpen(t *testing.T) {
	ctx := context.Background()
	db, err := cek.Open(namespace.System(), "plainmemo", t.TempDir())
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Store{db: db, fields: testFields(t)}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	memoed(t, s, "e_a", "reverse", "r_a", reason, 10)
	if _, err := db.Exec(`UPDATE treasury_entries SET memo = $1 WHERE id = 'e_a'`, reason); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	sealed := stored(t, s, "e_a")
	if !strings.HasPrefix(sealed, "seal:") || strings.Contains(sealed, "Alice") {
		t.Fatalf("the open left the memo as %q", sealed)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if again := stored(t, s, "e_a"); again != sealed {
		t.Fatal("a second open rewrote a sealed memo")
	}
	es, err := s.Entries(ctx, 0)
	if err != nil || len(es) != 1 || es[0].Memo != reason {
		t.Fatalf("Entries = %+v, %v", es, err)
	}
}
