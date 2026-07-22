package team

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/team/token"
)

func newAccountStore(t *testing.T) *accountStore {
	t.Helper()
	s, err := openAccountStore(filepath.Join(t.TempDir(), "account.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestEnsureWorkspaceIdempotent proves a second EnsureWorkspace for the same
// (org, account) returns the SAME workspace (not a duplicate) — the workspace
// picker is seeded exactly once.
func TestEnsureWorkspaceIdempotent(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"

	w1, err := s.EnsureWorkspace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.EnsureWorkspace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if w1.UUID != w2.UUID || w1.ID != w2.ID {
		t.Fatalf("ensure not idempotent: %s != %s", w1.UUID, w2.UUID)
	}
	wss, err := s.WorkspacesOf(ctx, org, acct)
	if err != nil || len(wss) != 1 {
		t.Fatalf("workspacesOf = %d (%v), want 1", len(wss), err)
	}
	// The owner member row exists with role owner.
	role, ok := s.Membership(ctx, w1.ID, acct)
	if !ok || role != "owner" {
		t.Fatalf("owner membership = %q,%v, want owner", role, ok)
	}
}

// TestEnsureWorkspaceConcurrentSingleRow proves two (or more) concurrent logins for
// the same (org, account) converge to exactly ONE personal workspace. The prior
// check-then-insert (WorkspacesOf → INSERT) had no uniqueness on the personal-
// workspace identity, so racing logins could each see "none" and mint a duplicate;
// the (owner_org, owner) unique index + idempotent upsert closes it.
func TestEnsureWorkspaceConcurrentSingleRow(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"

	const n = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	got := make([]workspace, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all at once to maximize the check-then-insert interleave
			got[i], errs[i] = s.EnsureWorkspace(ctx, org, acct, "Ada")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureWorkspace #%d: %v", i, err)
		}
	}
	// Every concurrent caller must resolve the SAME workspace row.
	for i := 1; i < n; i++ {
		if got[i].ID != got[0].ID {
			t.Fatalf("concurrent logins minted different workspaces: %s vs %s", got[0].ID, got[i].ID)
		}
	}
	// And the store holds exactly one personal workspace for the account.
	wss, err := s.WorkspacesOf(ctx, org, acct)
	if err != nil {
		t.Fatal(err)
	}
	if len(wss) != 1 {
		t.Fatalf("want exactly 1 personal workspace after %d concurrent logins, got %d", n, len(wss))
	}
}

// TestSeatsCountsActiveMember proves Seats counts the org's distinct active,
// non-bot members — so a just-seeded owner yields at least one seat. (The wallet's
// "0 members" symptom is a MISSING member row for the viewed org, never the count
// logic; establishSession now seeds one per verified org.) A bot and a deactivated
// row are excluded; a guest counts as both a seat and a guest; the count is
// tenant-scoped.
func TestSeatsCountsActiveMember(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org = "acme"
	owner := "aaaaaaaa-0000-4000-8000-000000000001"

	// A freshly ensured workspace seeds the owner member row → at least one seat.
	w, err := s.EnsureWorkspace(ctx, org, owner, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if seats, _, _ := s.Seats(ctx, org); seats < 1 {
		t.Fatalf("seats after seeding one member = %d, want >= 1", seats)
	}

	seed := func(user, role string, bot, active int) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
			 VALUES (?,?,?,?,?,?,1)`, w.ID, user, role, "m", bot, active); err != nil {
			t.Fatal(err)
		}
	}
	seed("bbbbbbbb-0000-4000-8000-000000000002", "member", 1, 1)  // bot — never a seat
	seed("cccccccc-0000-4000-8000-000000000003", "member", 0, 0)  // deactivated — excluded
	seed("dddddddd-0000-4000-8000-000000000004", roleGuest, 0, 1) // guest — a seat AND a guest

	seats, guests, err := s.Seats(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if seats != 2 {
		t.Fatalf("seats = %d, want 2 (owner + guest; bot and inactive excluded)", seats)
	}
	if guests != 1 {
		t.Fatalf("guests = %d, want 1 (the guest)", guests)
	}

	// Tenant-scoped: another org sees none of acme's seats.
	if seats, _, _ := s.Seats(ctx, "other"); seats != 0 {
		t.Fatalf("other-org seats = %d, want 0", seats)
	}
}

// TestEnsureWorkspaceHealsMigratedMember reproduces the live "Seats: 0 for
// maxpower" shape: the caller (Dave) OWNS a workspace in the org, but a team-go
// migration left his member row as is_bot=1 / active=0, so Seats excludes him even
// though getUserWorkspaces still lists the workspace. Re-authenticating (which runs
// EnsureWorkspace) must heal the caller's own row back to an active human seat.
func TestEnsureWorkspaceHealsMigratedMember(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org = "maxpower"
	const acct = "113d4dd4-2486-40de-be2b-88d6e3e0b718" // Dave's real account uuid shape

	w, err := s.EnsureWorkspace(ctx, org, acct, "Dave Lorenzini")
	if err != nil {
		t.Fatal(err)
	}
	// A freshly created workspace already counts one seat.
	if seats, _, _ := s.Seats(ctx, org); seats != 1 {
		t.Fatalf("seats after create = %d, want 1", seats)
	}

	// Corrupt the owner's row exactly as the migration did: bot + inactive.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE members SET is_bot = 1, active = 0 WHERE workspace_id = ? AND user_id = ?`,
		w.ID, acct); err != nil {
		t.Fatal(err)
	}
	if seats, _, _ := s.Seats(ctx, org); seats != 0 {
		t.Fatalf("seats with migrated bot/inactive owner = %d, want 0 (the live defect)", seats)
	}
	// The workspace still lists for the user (getUserWorkspaces does not filter flags).
	if ws, err := s.WorkspacesOf(ctx, org, acct); err != nil || len(ws) != 1 {
		t.Fatalf("WorkspacesOf = %d (%v), want 1 (still listed)", len(ws), err)
	}

	// Re-login → EnsureWorkspace heals the caller's own row → the seat returns.
	if _, err := s.EnsureWorkspace(ctx, org, acct, "Dave Lorenzini"); err != nil {
		t.Fatal(err)
	}
	if seats, _, _ := s.Seats(ctx, org); seats != 1 {
		t.Fatalf("seats after re-login heal = %d, want 1 (owner is an active human seat)", seats)
	}
}

// TestSeatsSurfacesReadError proves a real seat-read failure is PROPAGATED, not
// swallowed into a false "0 members": a broken store (closed handle) errors rather
// than silently reporting 0 seats (which would masquerade as "no members" and
// under-bill). An org with no members legitimately returns (0, 0, nil) — that path
// is exercised by the tenant-scoped "other" org above; here the read itself fails.
func TestSeatsSurfacesReadError(t *testing.T) {
	s := newAccountStore(t)
	_ = s.db.Close() // simulate an unreadable store — the query must error, not report 0
	seats, guests, err := s.Seats(context.Background(), "acme")
	if err == nil {
		t.Fatal("Seats must surface a read error, not a silent 0-seat count")
	}
	if seats != 0 || guests != 0 {
		t.Fatalf("on error Seats must return 0,0 alongside the error, got %d,%d", seats, guests)
	}
}

// TestMembershipReadFromRow proves Membership is read from the members row (never
// self-asserted): a non-member gets ("", false).
func TestMembershipReadFromRow(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	w, _ := s.EnsureWorkspace(ctx, "acme", "aaaaaaaa-0000-4000-8000-000000000001", "Owner")
	if _, ok := s.Membership(ctx, w.ID, "bbbbbbbb-0000-4000-8000-000000000002"); ok {
		t.Fatal("non-member must not resolve a role")
	}
}

// TestWorkspaceBySlugTenantScoped is the cross-tenant isolation bar: org B cannot
// resolve org A's workspace by its slug — the slug lookup is scoped by owner_org,
// so selectWorkspace can never select a foreign tenant's workspace.
func TestWorkspaceBySlugTenantScoped(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	wA, _ := s.EnsureWorkspace(ctx, "org-a", "aaaaaaaa-0000-4000-8000-00000000000a", "Alice")

	// Same slug, resolved in org-a → found; in org-b → errNoWorkspace.
	if _, err := s.WorkspaceBySlug(ctx, "org-a", wA.Slug); err != nil {
		t.Fatalf("org-a should resolve its own slug: %v", err)
	}
	if _, err := s.WorkspaceBySlug(ctx, "org-b", wA.Slug); !errors.Is(err, errNoWorkspace) {
		t.Fatalf("org-b resolving org-a's slug = %v, want errNoWorkspace (cross-tenant leak)", err)
	}
}

// TestMembersForWorkspaceTenantScoped proves the roster source is org-scoped: a
// foreign tenant's uuid returns no members (never mis-files another org's roster).
func TestMembersForWorkspaceTenantScoped(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	w, _ := s.EnsureWorkspace(ctx, "org-a", "aaaaaaaa-0000-4000-8000-00000000000a", "Alice")

	got, err := s.MembersForWorkspaceUUID(ctx, "org-a", w.UUID)
	if err != nil || len(got) != 1 {
		t.Fatalf("org-a members = %d (%v), want 1 (the owner)", len(got), err)
	}
	if got[0].Role != "owner" || got[0].IsBot {
		t.Fatalf("owner member row wrong: %+v", got[0])
	}
	foreign, err := s.MembersForWorkspaceUUID(ctx, "org-b", w.UUID)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("org-b reading org-a's workspace uuid = %d members, want 0", len(foreign))
	}
}

// TestSelectWorkspaceCore exercises the selectWorkspace spine at the store+token
// layer: resolve the workspace org-scoped, gate on the members row, mint a
// workspace token carrying extra.org, and confirm it decodes back to the same
// (account, workspace, org). This is the exact minting selectWorkspace does before
// returning the transactor endpoint.
func TestSelectWorkspaceCore(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const org, acct, secret = "acme", "550e8400-e29b-41d4-a716-446655440000", "server-secret"
	w, _ := s.EnsureWorkspace(ctx, org, acct, "Ada")

	ws, err := s.WorkspaceBySlug(ctx, org, w.Slug)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	role, ok := s.Membership(ctx, ws.ID, acct)
	if !ok || role != "owner" {
		t.Fatalf("membership gate: role=%q ok=%v", role, ok)
	}
	wsTok, err := token.Generate(acct, ws.UUID, map[string]any{"org": org}, expUnix(workspaceTokenTTL), secret)
	if err != nil {
		t.Fatalf("mint workspace token: %v", err)
	}
	dec, err := token.Decode(wsTok, secret, true)
	if err != nil {
		t.Fatalf("decode workspace token: %v", err)
	}
	if dec.Account != acct || dec.Workspace != ws.UUID || dec.Extra["org"] != org {
		t.Fatalf("workspace token claims = %+v, want acct=%s ws=%s org=%s", dec, acct, ws.UUID, org)
	}
}
