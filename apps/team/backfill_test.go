package team

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/orm/query"
)

// seedLegacy recreates the roster table a deployment is upgrading from and puts
// one row in it.
func seedLegacy(t *testing.T, s *accountStore, wsID, user, role string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.NewQuery(`CREATE TABLE IF NOT EXISTS members (
	  workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'member',
	  display_name TEXT NOT NULL DEFAULT '', is_bot INTEGER NOT NULL DEFAULT 0,
	  active INTEGER NOT NULL DEFAULT 1, joined_at INTEGER NOT NULL,
	  PRIMARY KEY (workspace_id, user_id))`).WithContext(ctx).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Insert("members", query.Params{
		"workspace_id": wsID, "user_id": user, "role": role,
		"display_name": "n", "is_bot": 0, "active": 1, "joined_at": 1,
	}).WithContext(ctx).Execute(); err != nil {
		t.Fatal(err)
	}
}

// Every roster row reaches IAM, and the table goes with it. Without this an
// upgrade silently empties every workspace: the rows are still on disk and
// nothing reads them.
func TestBackfillMovesTheRosterAndDropsTheTable(t *testing.T) {
	id := planetest.ServeIdentity(t)
	s := newAccountStore(t)
	ctx := context.Background()
	const org = "acme"

	w, err := s.EnsureWorkspace(ctx, org, "aaaaaaaa-0000-4000-8000-000000000001", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, s, w.ID, "bbbbbbbb-0000-4000-8000-000000000002", "member")

	if err := backfill(ctx, s); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	role, ok := s.Membership(ctx, org, w.UUID, "bbbbbbbb-0000-4000-8000-000000000002")
	if !ok || role != "member" {
		t.Fatalf("the legacy member is %q,%v in IAM — the roster did not move", role, ok)
	}
	if _, err := s.pendingGrants(ctx); err == nil {
		t.Error("the members table survived a completed backfill")
	}
	// Twice is a no-op, not a second write.
	if err := backfill(ctx, s); err != nil {
		t.Errorf("second backfill: %v", err)
	}
	n := 0
	for _, g := range id.Grants() {
		if g.User == "bbbbbbbb-0000-4000-8000-000000000002" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the member holds %d grants, want 1", n)
	}
}

// A backfill that cannot reach IAM leaves the table ALONE. Dropping rows nothing
// took is the one outcome that loses a roster.
func TestBackfillKeepsTheTableWhenIAMRefuses(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	w, err := s.EnsureWorkspace(ctx, "acme", "aaaaaaaa-0000-4000-8000-000000000001", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, s, w.ID, "bbbbbbbb-0000-4000-8000-000000000002", "member")

	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // no peer answers here
	if err := backfill(ctx, s); err == nil {
		t.Fatal("backfill reported success with no IAM to take the rows")
	}
	rows, err := s.pendingGrants(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending = %d (%v), want the row still there for the next boot", len(rows), err)
	}
}
