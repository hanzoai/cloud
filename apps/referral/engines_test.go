package referral

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// engines_test.go holds what the port to hanzo-sql changed that no behaviour test
// above reaches by itself: the idempotent writes, raced, on both engines.

func openStoreOn(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// An org's code is minted once however many callers ask at once: every one of them is
// handed the same code, and the directory holds one row for the org.
func TestAnOrgsCodeIsMintedOnceUnderRacing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openStoreOn(t)
		ctx := context.Background()
		codes := make(chan string, 16)
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, err := s.EnsureCode(ctx, "acme")
				if err != nil {
					t.Error(err)
				}
				codes <- code
			}()
		}
		wg.Wait()
		close(codes)
		seen := map[string]bool{}
		for c := range codes {
			seen[c] = true
		}
		if len(seen) != 1 {
			t.Fatalf("sixteen callers were handed %d different codes: %v", len(seen), seen)
		}
		var rows int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM referral_codes WHERE org = $1`, "acme").Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("the directory holds %d rows for the org, %v; want 1", rows, err)
		}
	})
}

// An org is referred at most once, however many claims race for it: one creates the
// edge, the rest are handed it, and the first referrer keeps the attribution.
func TestOneClaimCreatesTheEdgeUnderRacing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openStoreOn(t)
		ctx := context.Background()
		var created atomic.Int32
		var wg sync.WaitGroup
		for i := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, made, err := s.Claim(ctx, fmt.Sprintf("ref_%d", i), fmt.Sprintf("referrer%d", i), "referee", "CODE")
				if err != nil {
					t.Error(err)
				}
				if made {
					created.Add(1)
				}
			}()
		}
		wg.Wait()
		if created.Load() != 1 {
			t.Fatalf("%d claims created the edge, want 1", created.Load())
		}
		var rows int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM referrals WHERE referee_org = $1`, "referee").Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("%d edges for the referee, %v; want 1", rows, err)
		}
	})
}

// The tally counts every referrer named in one grouped read, past the handful a
// test usually names.
func TestTheTallyNamesManyReferrers(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openStoreOn(t)
		ctx := context.Background()
		var orgs []string
		for i := range 40 {
			org := fmt.Sprintf("org%d", i)
			orgs = append(orgs, org)
			if _, _, err := s.Claim(ctx, fmt.Sprintf("r%d", i), org, fmt.Sprintf("referee%d", i), "C"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.LatchQualified(ctx, "r3", 7); err != nil {
			t.Fatal(err)
		}
		got, err := s.Tally(ctx, orgs)
		if err != nil || len(got) != 40 {
			t.Fatalf("tally of 40 referrers = %d entries, %v", len(got), err)
		}
		if c := got["org3"]; c.Total != 1 || c.Qualified != 1 || c.Signup != 0 {
			t.Fatalf("org3 = %+v, want one qualified", c)
		}
		if c := got["org4"]; c.Total != 1 || c.Signup != 1 {
			t.Fatalf("org4 = %+v, want one signup", c)
		}
	})
}

// A SQLite file from the credit era keeps its rows under the one vocabulary and still
// takes an insert that names only the live columns, whatever the retired ones were
// declared. Hand-built, so SQLite's alone.
func TestAFileFromTheCreditEraOpensAndTakesClaims(t *testing.T) {
	dir := t.TempDir()
	db, err := locus.System("referrals", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE referral_codes (code TEXT PRIMARY KEY, org TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL);
CREATE TABLE referrals (
  id TEXT PRIMARY KEY, referrer_org TEXT NOT NULL, referee_org TEXT NOT NULL UNIQUE, code TEXT NOT NULL,
  status TEXT NOT NULL, created_at INTEGER NOT NULL, qualified_at INTEGER NOT NULL DEFAULT 0,
  grant_micro INTEGER NOT NULL DEFAULT 0, txn TEXT NOT NULL DEFAULT ''
);
INSERT INTO referrals (id, referrer_org, referee_org, code, status, created_at) VALUES
  ('a', 'r', 'one', 'C', 'signed_up', 1), ('b', 'r', 'two', 'C', 'credited', 2), ('c', 'r', 'three', 'C', 'signup', 3);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	for i := range 2 { // the second open finds nothing left to change
		s, err := openStore(dir)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		want := map[string]string{"a": StatusSignup, "b": StatusQualified, "c": StatusSignup}
		for id, status := range want {
			got, err := s.Get(context.Background(), id)
			if err != nil || got.Status != status {
				t.Fatalf("open #%d: referral %s = %+v, %v; want status %s", i, id, got, err, status)
			}
		}
		if _, made, err := s.Claim(context.Background(), fmt.Sprintf("new%d", i), "r", fmt.Sprintf("fresh%d", i), "C"); err != nil || !made {
			t.Fatalf("open #%d: a claim over the old table = %v, %v", i, made, err)
		}
		_ = s.Close()
	}
}
