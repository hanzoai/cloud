package team

import (
	"context"
	"errors"
	"path/filepath"
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
	wsTok, err := token.Generate(acct, ws.UUID, map[string]any{"org": org}, secret)
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
