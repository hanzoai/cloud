package company

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
// the store seals its personal-data columns under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func rawColumn(t *testing.T, s *Store, col, owner string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT `+col+` FROM company_formations WHERE org = $1`, owner).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return v
}

func sample(owner string) *Formation {
	return &Formation{
		Org: owner, Stage: StageFounders, Structure: StructureCCorp, Name: "Lovelace Analytical Holdings, Inc.", CreatedAt: 1, UpdatedAt: 2,
		Founders: []Founder{{Name: "Ada Lovelace", Email: "ada@lovelace.example"}},
	}
}

// The people of a formation, and the name it is formed under, are ciphertext in the
// database on both engines; the register still lists by stage and structure.
func TestAFormationIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		f := sample("acme")
		if err := s.Put(ctx, f); err != nil {
			t.Fatal(err)
		}
		for _, col := range []string{"name", "data"} {
			v := rawColumn(t, s, col, "acme")
			if !strings.HasPrefix(v, "seal:") || strings.Contains(v, "Lovelace") || strings.Contains(v, "ada@") {
				t.Errorf("%s is stored as %.30q: not sealed", col, v)
			}
		}
		if rawColumn(t, s, "stage", "acme") != string(StageFounders) || rawColumn(t, s, "structure", "acme") != string(StructureCCorp) {
			t.Error("stage or structure is not plain")
		}
		got, err := s.Get(ctx, "acme")
		if err != nil || got.Name != f.Name || len(got.Founders) != 1 || got.Founders[0].Email != "ada@lovelace.example" {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		if list, err := s.List(ctx, Filter{Stage: StageFounders}); err != nil || len(list) != 1 || list[0].Name != f.Name {
			t.Fatalf("List = %+v, %v", list, err)
		}
		if pending, err := s.Pending(ctx, 10); err != nil || len(pending) != 1 || pending[0].Founders[0].Name != "Ada Lovelace" {
			t.Fatalf("Pending = %+v, %v", pending, err)
		}
		// An update reseals under a fresh nonce and reads back.
		before := rawColumn(t, s, "data", "acme")
		f.UpdatedAt = 9
		if err := s.Put(ctx, f); err != nil {
			t.Fatal(err)
		}
		if rawColumn(t, s, "data", "acme") == before {
			t.Error("an update left the same ciphertext")
		}
		if got, err := s.Get(ctx, "acme"); err != nil || got.Name != f.Name {
			t.Fatalf("after an update: %+v, %v", got, err)
		}
	})
}

// A formation lifted into another org's row, or planted in the clear, is refused.
func TestAFormationIsBoundToItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for _, owner := range []string{"acme", "globex"} {
			if err := s.Put(ctx, sample(owner)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`UPDATE company_formations SET data = $1 WHERE org = 'globex'`, rawColumn(t, s, "data", "acme")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "globex"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("globex read acme's formation: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE company_formations SET name = 'Planted Corp' WHERE org = 'globex'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(ctx, Filter{}); !errors.Is(err, tenure.ErrFieldPlain) {
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
// built by hand (the row is plaintext because the code that wrote them was), so this
// stays on SQLite: hanzo-sql has no such file.
func TestALegacyFormationIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO company_formations (org, stage, structure, name, data, created_at, updated_at)
		 VALUES ('old', 'founders', 'c-corp', 'Old Co', '{"org":"old","name":"Old Co","founders":[{"name":"Old Founder"}]}', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, testMaster)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.Get(ctx, "old")
	if err != nil || got.Name != "Old Co" || len(got.Founders) != 1 || got.Founders[0].Name != "Old Founder" {
		t.Fatalf("a legacy formation: %+v, %v", got, err)
	}
	if list, err := s.List(ctx, Filter{}); err != nil || len(list) != 1 || list[0].Name != "Old Co" {
		t.Fatalf("List: %+v, %v", list, err)
	}
	for _, col := range []string{"name", "data"} {
		if v := rawColumn(t, s, col, "old"); !strings.HasPrefix(v, "seal:") {
			t.Errorf("%s is still %.20q after the upgrade", col, v)
		}
	}
}
