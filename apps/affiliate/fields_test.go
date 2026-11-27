package affiliate

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// the store seals a note and a payout's reference under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func rawOf(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// The staff's note on a deal and a payout's reference are ciphertext in the database on
// both engines; every read gives them back, and the public handle stays plain.
func TestANoteAndAReferenceAreSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		a, _, err := s.Apply(ctx, "aff_a", "acme", "u1", "", 1000)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if _, err := s.SetHandle(ctx, a.ID, "ada"); err != nil {
			t.Fatal(err)
		}
		const note = "Ada Lovelace negotiated 15% over a call with Charles"
		got, err := s.SetNote(ctx, a.ID, note)
		if err != nil || got.Note != note {
			t.Fatalf("SetNote = %+v, %v", got, err)
		}
		if v := rawOf(t, s, `SELECT note FROM affiliates WHERE id = $1`, a.ID); !strings.HasPrefix(v, "seal:") || strings.Contains(v, "Lovelace") {
			t.Fatalf("note is stored as %.30q: not sealed", v)
		}
		if rawOf(t, s, `SELECT handle FROM affiliates WHERE id = $1`, a.ID) != "ada" {
			t.Error("the public handle is not plain")
		}
		if byID, err := s.getByID(ctx, a.ID); err != nil || byID.Note != note {
			t.Fatalf("getByID = %+v, %v", byID, err)
		}
		if list, err := s.ListAll(ctx, 10); err != nil || len(list) != 1 || list[0].Note != note {
			t.Fatalf("ListAll = %+v, %v", list, err)
		}
		// Clearing a note leaves it empty.
		if cleared, err := s.SetNote(ctx, a.ID, ""); err != nil || cleared.Note != "" {
			t.Fatalf("clearing = %+v, %v", cleared, err)
		}
		if v := rawOf(t, s, `SELECT note FROM affiliates WHERE id = $1`, a.ID); v != "" {
			t.Fatalf("a cleared note is stored as %.30q", v)
		}
	})
}

// A note lifted into another affiliate, or planted in the clear, is refused.
func TestANoteIsBoundToItsAffiliate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		a, _, err := s.Apply(ctx, "aff_a", "acme", "u1", "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := s.Apply(ctx, "aff_b", "globex", "u2", "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range []Affiliate{a, b} {
			if _, err := s.SetNote(ctx, x.ID, "note of "+x.Org); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`UPDATE affiliates SET note = $1 WHERE id = $2`, rawOf(t, s, `SELECT note FROM affiliates WHERE id = $1`, a.ID), b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.getByID(ctx, b.ID); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("B read A's note: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE affiliates SET note = 'planted' WHERE id = $1`, b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListAll(ctx, 10); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if s, err := openStore(t.TempDir(), master); err == nil {
				_ = s.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A SQLite file written before the columns were sealed holds plaintext in them. It is
// built by hand, so this stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO affiliates (id, org, status, rate_bps, created_at, note) VALUES ('aff_old', 'acme', 'approved', 1000, 1, 'old note')`,
		`INSERT INTO affiliate_payouts (id, affiliate_id, amount_cents, method, reference, created_at) VALUES ('po_old', 'aff_old', 5, 'credits', 'old reference', 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		s, err = openStore(dir, testMaster)
		if err != nil {
			t.Fatal(err)
		}
		if a, err := s.getByID(ctx, "aff_old"); err != nil || a.Note != "old note" {
			t.Fatalf("pass %d: a legacy affiliate = %+v, %v", pass, a, err)
		}
		if list, err := s.ListPayouts(ctx, "aff_old", 10); err != nil || len(list) != 1 || list[0].Reference != "old reference" {
			t.Fatalf("pass %d: a legacy payout = %+v, %v", pass, list, err)
		}
		for _, q := range []string{`SELECT note FROM affiliates`, `SELECT reference FROM affiliate_payouts`} {
			if v := rawOf(t, s, q); !strings.HasPrefix(v, "seal:") {
				t.Errorf("pass %d: %s is still %.20q after the upgrade", pass, q, v)
			}
		}
		_ = s.Close()
	}
}
