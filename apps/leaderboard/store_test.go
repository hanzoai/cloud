package leaderboard

import (
	"context"
	"errors"
	"strings"
	"testing"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

func openTestStore(t *testing.T) *optinStore {
	t.Helper()
	s, err := openOptinStore(t.TempDir(), testMaster)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestOptinStore_PrivateByDefault: an unknown user/org is NOT listed — the opt-in
// default is private. GetUser returns errNotFound; listings are empty.
func TestOptinStore_PrivateByDefault(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openTestStore(t)
		ctx := context.Background()
		if _, err := s.GetUser(ctx, "acme/alice"); err != errNotFound {
			t.Fatalf("unknown user must be errNotFound, got %v", err)
		}
		if _, err := s.GetOrg(ctx, "acme"); err != errNotFound {
			t.Fatalf("unknown org must be errNotFound, got %v", err)
		}
		if h, err := s.ListedHandles(ctx, "acme"); err != nil || len(h) != 0 {
			t.Fatalf("no listed users by default: %v %v", h, err)
		}
		if o, err := s.ListedOrgs(ctx); err != nil || len(o) != 0 {
			t.Fatalf("no listed orgs by default: %v %v", o, err)
		}
	})
}

// TestOptinStore_UserListingIsolatedByOrg: ListedHandles(org) returns ONLY that
// org's opted-in users — never another org's, never a non-listed user.
func TestOptinStore_UserListingIsolatedByOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openTestStore(t)
		ctx := context.Background()
		mustPutUser(t, s, "acme/alice", "acme", "AliceZ", true)  // listed
		mustPutUser(t, s, "acme/bob", "acme", "Bob", false)      // private
		mustPutUser(t, s, "other/carol", "other", "Carol", true) // different tenant

		h, err := s.ListedHandles(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if len(h) != 1 || h["acme/alice"] != "AliceZ" {
			t.Fatalf("acme listing must be {alice}, got %v", h)
		}
		if _, leaked := h["other/carol"]; leaked {
			t.Fatal("CROSS-TENANT LEAK: other org's user in acme listing")
		}
		if _, leaked := h["acme/bob"]; leaked {
			t.Fatal("private (non-listed) user leaked into listing")
		}

		// The other org sees only its own.
		h2, _ := s.ListedHandles(ctx, "other")
		if len(h2) != 1 || h2["other/carol"] != "Carol" {
			t.Fatalf("other listing = %v", h2)
		}

		// Round-trip + un-list.
		u, err := s.GetUser(ctx, "acme/alice")
		if err != nil || !u.Listed || u.Handle != "AliceZ" || u.Org != "acme" {
			t.Fatalf("get alice = %+v %v", u, err)
		}
		mustPutUser(t, s, "acme/alice", "acme", "AliceZ", false)
		if h, _ := s.ListedHandles(ctx, "acme"); len(h) != 0 {
			t.Fatalf("un-listing failed: %v", h)
		}
	})
}

func TestOptinStore_OrgListing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openTestStore(t)
		ctx := context.Background()
		if err := s.PutOrg(ctx, orgOptin{Org: "acme", Display: "AgentCo, LLC", Listed: true}, 1); err != nil {
			t.Fatal(err)
		}
		if err := s.PutOrg(ctx, orgOptin{Org: "quiet", Display: "Quiet Co", Listed: false}, 1); err != nil {
			t.Fatal(err)
		}
		o, err := s.ListedOrgs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(o) != 1 || o["acme"] != "AgentCo, LLC" {
			t.Fatalf("org listing = %v (private org must not appear)", o)
		}
		got, err := s.GetOrg(ctx, "acme")
		if err != nil || !got.Listed || got.Display != "AgentCo, LLC" {
			t.Fatalf("get org = %+v %v", got, err)
		}
	})
}

func mustPutUser(t *testing.T, s *optinStore, id, org, handle string, listed bool) {
	t.Helper()
	if err := s.PutUser(context.Background(), userOptin{UserID: id, Org: org, Handle: handle, Listed: listed}, 1); err != nil {
		t.Fatalf("put user %s: %v", id, err)
	}
}

// The awards the board starts with are written when the store opens, once: opening it
// again leaves them as they were, and the totals sum them as integers on either engine
// (SUM is numeric on PostgreSQL).
func TestSeededBountiesSurviveAReopenAndSumAsIntegers(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openTestStore(t)
		ctx := context.Background()
		if err := s.PutBountyAward(ctx, bountyAward{ID: "bty_extra", Org: "valerypetrov", Kind: "bug_report", Points: 25, CreatedAt: 1}); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := s.migrate(); err != nil {
			t.Fatalf("migrate again: %v", err)
		}
		awards, err := s.ListBountyAwards(ctx, 50)
		if err != nil || len(awards) != 4 {
			t.Fatalf("awards after a reopen: %d %v, want the three seeds and the one added", len(awards), err)
		}
		totals, err := s.BountyTotals(ctx)
		if err != nil {
			t.Fatalf("totals: %v", err)
		}
		if totals["valerypetrov"] != 175 {
			t.Fatalf("totals = %v, want valerypetrov at 175 (3 x 50 + 25)", totals)
		}
	})
}

// Text PostgreSQL would refuse is refused at the store on both engines, whatever the
// value is part of, and the error never carries it.
func TestOptinStoreRefusesTextPostgresRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := openTestStore(t)
		ctx := context.Background()
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			for what, err := range map[string]error{
				"PutUser":        s.PutUser(ctx, userOptin{UserID: "acme/alice", Org: "acme", Handle: bad, Listed: true}, 1),
				"PutOrg":         s.PutOrg(ctx, orgOptin{Org: "acme", Display: bad, Listed: true}, 1),
				"PutBountyAward": s.PutBountyAward(ctx, bountyAward{ID: "bty_x", Org: "acme", Summary: bad}),
			} {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
		}
		if _, err := s.GetUser(ctx, "acme/alice"); !errors.Is(err, errNotFound) {
			t.Fatalf("a refused write left a row: %v", err)
		}
	})
}
