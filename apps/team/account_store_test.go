package team

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/internal/planetest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/team/token"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

func newAccountStore(t *testing.T) *accountStore {
	t.Helper()
	planetest.ServeIdentity(t)
	s, err := openAccountStore(t.TempDir())
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestEnsureSpaceIdempotent proves a second EnsureSpace for the same
// (org, account) returns the SAME space (not a duplicate) — the space
// picker is seeded exactly once.
func TestEnsureSpaceIdempotent(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"

	w1, err := s.EnsureSpace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.EnsureSpace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if w1.UUID != w2.UUID || w1.ID != w2.ID {
		t.Fatalf("ensure not idempotent: %s != %s", w1.UUID, w2.UUID)
	}
	wss, err := s.SpacesOf(ctx, org, acct)
	if err != nil || len(wss) != 1 {
		t.Fatalf("spacesOf = %d (%v), want 1", len(wss), err)
	}
	// The owner member row exists with role owner.
	role, ok := s.Membership(ctx, org, w1.UUID, acct)
	if !ok || role != "owner" {
		t.Fatalf("owner membership = %q,%v, want owner", role, ok)
	}
}

// TestEnsureSpaceConcurrentSingleRow proves two (or more) concurrent logins for
// the same (org, account) converge to exactly ONE personal space. The prior
// check-then-insert (SpacesOf → INSERT) had no uniqueness on the personal-
// space identity, so racing logins could each see "none" and mint a duplicate;
// the (owner_org, owner) unique index + idempotent upsert closes it.
func TestEnsureSpaceConcurrentSingleRow(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"

	const n = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	got := make([]space, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all at once to maximize the check-then-insert interleave
			got[i], errs[i] = s.EnsureSpace(ctx, org, acct, "Ada")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureSpace #%d: %v", i, err)
		}
	}
	// Every concurrent caller must resolve the SAME space row.
	for i := 1; i < n; i++ {
		if got[i].ID != got[0].ID {
			t.Fatalf("concurrent logins minted different spaces: %s vs %s", got[0].ID, got[i].ID)
		}
	}
	// And the store holds exactly one personal space for the account.
	wss, err := s.SpacesOf(ctx, org, acct)
	if err != nil {
		t.Fatal(err)
	}
	if len(wss) != 1 {
		t.Fatalf("want exactly 1 personal space after %d concurrent logins, got %d", n, len(wss))
	}
}

// Seats is IAM's count, forwarded. WHICH people count — machines out, a person in
// three spaces once — is IAM's rule and is tested in its store; what team owes
// is that it asks and does not compute.
func TestSeatsForwardsIAMsCount(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org = "acme"
	const owner = "aaaaaaaa-0000-4000-8000-000000000001"

	w, err := s.EnsureSpace(ctx, org, owner, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, org, w.UUID, "bbbbbbbb-0000-4000-8000-000000000002", roleGuest); err != nil {
		t.Fatal(err)
	}
	seats, guests, err := s.Seats(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if seats != 2 || guests != 1 {
		t.Fatalf("seats=%d guests=%d, want 2/1 (the owner and one guest)", seats, guests)
	}
}

// A seat read that fails is PROPAGATED: a broken read reported as "0 members"
// under-bills silently, where an error retries.
func TestSeatsSurfacesReadError(t *testing.T) {
	s := newAccountStore(t)
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // no identity peer answers here
	seats, guests, err := s.Seats(context.Background(), "acme")
	if err == nil {
		t.Fatal("Seats must surface a read error, not a silent 0-seat count")
	}
	if seats != 0 || guests != 0 {
		t.Fatalf("on error Seats must return 0,0 alongside the error, got %d,%d", seats, guests)
	}
}

func TestMembershipReadFromRow(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	w, _ := s.EnsureSpace(ctx, "acme", "aaaaaaaa-0000-4000-8000-000000000001", "Owner")
	if _, ok := s.Membership(ctx, "acme", w.UUID, "bbbbbbbb-0000-4000-8000-000000000002"); ok {
		t.Fatal("non-member must not resolve a role")
	}
}

// TestSpaceBySlugTenantScoped is the cross-tenant isolation bar: org B cannot
// resolve org A's space by its slug — the slug lookup is scoped by owner_org,
// so selectWorkspace can never select a foreign tenant's space.
func TestSpaceBySlugTenantScoped(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	wA, _ := s.EnsureSpace(ctx, "org-a", "aaaaaaaa-0000-4000-8000-00000000000a", "Alice")

	// Same slug, resolved in org-a → found; in org-b → errNoSpace.
	if _, err := s.SpaceBySlug(ctx, "org-a", wA.Slug); err != nil {
		t.Fatalf("org-a should resolve its own slug: %v", err)
	}
	if _, err := s.SpaceBySlug(ctx, "org-b", wA.Slug); !errors.Is(err, errNoSpace) {
		t.Fatalf("org-b resolving org-a's slug = %v, want errNoSpace (cross-tenant leak)", err)
	}
}

// TestMembersForSpaceTenantScoped proves the roster source is org-scoped: a
// foreign tenant's uuid returns no members (never mis-files another org's roster).
func TestMembersForSpaceTenantScoped(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	w, _ := s.EnsureSpace(ctx, "org-a", "aaaaaaaa-0000-4000-8000-00000000000a", "Alice")

	got, err := s.MembersForSpaceUUID(ctx, "org-a", w.UUID)
	if err != nil || len(got) != 1 {
		t.Fatalf("org-a members = %d (%v), want 1 (the owner)", len(got), err)
	}
	if got[0].Role != "owner" || got[0].IsBot {
		t.Fatalf("owner member row wrong: %+v", got[0])
	}
	foreign, err := s.MembersForSpaceUUID(ctx, "org-b", w.UUID)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("org-b reading org-a's space uuid = %d members, want 0", len(foreign))
	}
}

// TestSelectSpaceCore exercises the selectWorkspace spine at the store+token
// layer: resolve the space org-scoped, gate on the members row, mint a
// space token carrying extra.org, and confirm it decodes back to the same
// (account, space, org). This is the exact minting selectWorkspace does before
// returning the transactor endpoint.
func TestSelectSpaceCore(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org, acct, secret = "acme", "550e8400-e29b-41d4-a716-446655440000", "server-secret"
	w, _ := s.EnsureSpace(ctx, org, acct, "Ada")

	ws, err := s.SpaceBySlug(ctx, org, w.Slug)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	role, ok := s.Membership(ctx, org, ws.UUID, acct)
	if !ok || role != "owner" {
		t.Fatalf("membership gate: role=%q ok=%v", role, ok)
	}
	wsTok, err := token.Generate(acct, ws.UUID, map[string]any{"org": org}, expUnix(spaceTokenTTL), secret)
	if err != nil {
		t.Fatalf("mint space token: %v", err)
	}
	dec, err := token.Decode(wsTok, secret, true)
	if err != nil {
		t.Fatalf("decode space token: %v", err)
	}
	if dec.Account != acct || dec.Space != ws.UUID || dec.Org() != org {
		t.Fatalf("space token claims = %+v, want acct=%s ws=%s org=%s", dec, acct, ws.UUID, org)
	}
}

// AddMember never downgrades: re-adding somebody who is already an owner leaves
// them one. The grant is IAM's, so this pins that team asks for the right thing.
func TestAddMemberNeverDowngrades(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org = "acme"
	const acct = "aaaaaaaa-0000-4000-8000-000000000001"

	w, err := s.EnsureSpace(ctx, org, acct, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, org, w.UUID, acct, "member"); err != nil {
		t.Fatal(err)
	}
	role, ok := s.Membership(ctx, org, w.UUID, acct)
	if !ok || role != "owner" {
		t.Fatalf("role = %q,%v — re-adding an owner as a member stripped their authority", role, ok)
	}
}
