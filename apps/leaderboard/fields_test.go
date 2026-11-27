package leaderboard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// the store seals a reporter's address under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func rawEmail(t *testing.T, s *optinStore, id string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT email FROM bounty_awards WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A reporter's address is ciphertext in the database on both engines; the listing the
// public board reads is untouched.
func TestARecordedAddressIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openTestStore(t)
		ctx := context.Background()
		a := bountyAward{ID: "bty_t1", Org: "acme", UserID: "u1", Handle: "ada", Email: "ada@analytical.example", Kind: "bug_report", Summary: "found one", Points: 10, CreatedAt: 5}
		if err := s.PutBountyAward(ctx, a); err != nil {
			t.Fatal(err)
		}
		if v := rawEmail(t, s, "bty_t1"); !strings.HasPrefix(v, "seal:") || strings.Contains(v, "analytical") {
			t.Fatalf("email is stored as %.30q: not sealed", v)
		}
		if v := rawEmail(t, s, "bty_valerypetrov_index_lock"); v != "" {
			t.Fatalf("an absent address became %q", v)
		}
		list, err := s.ListBountyAwards(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, got := range list {
			if got.ID == "bty_t1" {
				found = got.Handle == "ada" && got.Summary == "found one" && got.Points == 10
			}
		}
		if !found {
			t.Fatalf("ListBountyAwards = %+v", list)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if s, err := openOptinStore(t.TempDir(), master); err == nil {
				_ = s.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A SQLite file written before the address was sealed holds it in plaintext. It is built by
// hand, so this stays on SQLite: hanzo-sql has no such file.
func TestALegacyAddressIsSealedInPlace(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openOptinStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO bounty_awards (id, org, handle, email, created_at) VALUES ('bty_old', 'acme', 'h', 'old@x.example', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		s, err = openOptinStore(dir, testMaster)
		if err != nil {
			t.Fatal(err)
		}
		if v := rawEmail(t, s, "bty_old"); !strings.HasPrefix(v, "seal:") {
			t.Errorf("pass %d: the address is still %.20q after the upgrade", pass, v)
		}
		_ = s.Close()
	}
}
