package link

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkLink(org, user, machine, provider, account, kind string) Link {
	now := time.Now().Unix()
	id, _ := genID("link")
	return Link{
		ID: id, Org: org, User: user, Machine: machine, Host: machine + "-host", OS: "linux",
		Provider: provider, Account: account, Kind: kind, Status: StatusLinked,
		LastSeen: now, CreatedAt: now, UpdatedAt: now,
	}
}

// TestUpsertIdempotent: a repeat report of the same identity updates in place,
// keeps the original id + created_at, refreshes labels, merges usage (an empty
// heartbeat keeps the last snapshot), and re-links a revoked account.
func TestUpsertIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	a := mkLink("acme", "alice", "m1", "claude", "alice@x", KindSubscription)
	a.Usage = `{"sessionPct":10}`
	first, err := s.Upsert(ctx, a)
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}

	// A second report of the SAME identity with a different id/host/usage.
	b := mkLink("acme", "alice", "m1", "claude", "alice@x", KindSubscription)
	b.Host = "renamed"
	b.Usage = `{"sessionPct":80}`
	second, err := s.Upsert(ctx, b)
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("upsert must keep the original id: %s vs %s", second.ID, first.ID)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Fatalf("upsert must preserve created_at")
	}
	if second.Host != "renamed" {
		t.Fatalf("upsert must refresh the device label, got %q", second.Host)
	}
	if second.Usage != `{"sessionPct":80}` {
		t.Fatalf("upsert must replace usage with the fresh snapshot, got %q", second.Usage)
	}
	if got, _ := s.List(ctx, "acme", "alice"); len(got) != 1 {
		t.Fatalf("upsert of the same identity must NOT dup: want 1 row, got %d", len(got))
	}

	// A heartbeat with no usage keeps the last good snapshot (keep-stale rule).
	c := mkLink("acme", "alice", "m1", "claude", "alice@x", KindSubscription)
	c.Usage = ""
	third, err := s.Upsert(ctx, c)
	if err != nil {
		t.Fatalf("upsert 3: %v", err)
	}
	if third.Usage != `{"sessionPct":80}` {
		t.Fatalf("empty-usage heartbeat must keep the last snapshot, got %q", third.Usage)
	}

	// Revoke, then re-report → re-linked (log back in).
	if _, found, err := s.Revoke(ctx, "acme", "alice", first.ID, time.Now().Unix()); err != nil || !found {
		t.Fatalf("revoke: found=%v err=%v", found, err)
	}
	relink, err := s.Upsert(ctx, mkLink("acme", "alice", "m1", "claude", "alice@x", KindSubscription))
	if err != nil {
		t.Fatalf("re-report: %v", err)
	}
	if relink.Status != StatusLinked {
		t.Fatalf("a re-report must re-link a revoked account, got %q", relink.Status)
	}
}

// TestOrgAndUserIsolation: (org, subject) scopes EVERY read/write. A foreign org,
// AND a foreign user in the SAME org, sees/gets/revokes nothing of another's.
func TestOrgAndUserIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	al, _ := s.Upsert(ctx, mkLink("acme", "alice", "m1", "claude", "a", KindSubscription))
	_, _ = s.Upsert(ctx, mkLink("acme", "bob", "m2", "codex", "b", KindSubscription)) // same org, other user
	_, _ = s.Upsert(ctx, mkLink("evil", "alice", "m1", "claude", "a", KindSubscription))

	aliceLinks, _ := s.List(ctx, "acme", "alice")
	if len(aliceLinks) != 1 || aliceLinks[0].ID != al.ID {
		t.Fatalf("alice must see only her own link, got %+v", aliceLinks)
	}

	// Alice cannot Get bob's link by its id (same org, different user).
	bobLinks, _ := s.List(ctx, "acme", "bob")
	if len(bobLinks) != 1 {
		t.Fatalf("bob setup: want 1, got %d", len(bobLinks))
	}
	if _, err := s.Get(ctx, "acme", "alice", bobLinks[0].ID); err != errNotFound {
		t.Fatalf("alice must not Get bob's link: err=%v", err)
	}

	// The evil-org row of the same (user, machine, provider, account) is DISTINCT
	// (org is part of the identity) — no cross-org collision.
	evilLinks, _ := s.List(ctx, "evil", "alice")
	if len(evilLinks) != 1 || evilLinks[0].ID == al.ID {
		t.Fatalf("evil-org link must be a distinct row, got %+v", evilLinks)
	}
	if _, err := s.Get(ctx, "evil", "alice", al.ID); err != errNotFound {
		t.Fatalf("alice's acme link must not resolve under evil org")
	}

	// Bob revoking alice's id is a no-op (found=false) — never a cross-user write.
	if _, found, _ := s.Revoke(ctx, "acme", "bob", al.ID, time.Now().Unix()); found {
		t.Fatalf("bob must not be able to revoke alice's link")
	}
	if got, _ := s.Get(ctx, "acme", "alice", al.ID); got.Status != StatusLinked {
		t.Fatalf("alice's link must remain linked after bob's attempt, got %q", got.Status)
	}
}

// TestRevokeDeviceScoped: revoking a device revokes exactly that machine's linked
// accounts (within the caller's scope), leaves other machines untouched, and is
// idempotent.
func TestRevokeDeviceScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.Upsert(ctx, mkLink("acme", "alice", "m1", "claude", "a1", KindSubscription))
	_, _ = s.Upsert(ctx, mkLink("acme", "alice", "m1", "codex", "a2", KindSubscription))
	_, _ = s.Upsert(ctx, mkLink("acme", "alice", "m2", "claude", "a3", KindSubscription))

	if d, _ := s.ListDevice(ctx, "acme", "alice", "m1"); len(d) != 2 {
		t.Fatalf("device m1 want 2 accounts, got %d", len(d))
	}
	rev, err := s.RevokeDevice(ctx, "acme", "alice", "m1", time.Now().Unix())
	if err != nil || len(rev) != 2 {
		t.Fatalf("revoke device m1 want 2 revoked, got %d (%v)", len(rev), err)
	}
	linked, _ := s.ListLinked(ctx, "acme", "alice")
	if len(linked) != 1 || linked[0].Machine != "m2" {
		t.Fatalf("only m2 stays linked, got %+v", linked)
	}
	// Idempotent: nothing left linked on m1.
	if rev2, _ := s.RevokeDevice(ctx, "acme", "alice", "m1", time.Now().Unix()); len(rev2) != 0 {
		t.Fatalf("re-revoke device want 0, got %d", len(rev2))
	}
	// A foreign user cannot revoke alice's device.
	if rev3, _ := s.RevokeDevice(ctx, "acme", "bob", "m2", time.Now().Unix()); len(rev3) != 0 {
		t.Fatalf("bob revoking alice's device must be a no-op, got %d", len(rev3))
	}
}
