package marketing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "marketing.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPerOrgIsolation is the load-bearing tenant-isolation test: two orgs write
// campaigns; neither can list, get, update, or delete the other's rows.
func TestPerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	mp, err := s.CreateCampaign(ctx, Campaign{ID: "camp_mp", Org: "maxpower", Name: "Spring Launch", Channel: "meta", Status: "active", Budget: 50000, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("create maxpower campaign: %v", err)
	}
	if _, err := s.CreateCampaign(ctx, Campaign{ID: "camp_acme", Org: "acme", Name: "Acme Blast", Channel: "email", Status: "draft", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create acme campaign: %v", err)
	}

	// maxpower lists exactly its own.
	list, err := s.ListCampaigns(ctx, "maxpower", "", 100)
	if err != nil {
		t.Fatalf("list maxpower: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Spring Launch" {
		t.Fatalf("maxpower must see [Spring Launch], got %+v", list)
	}

	// acme cannot GET maxpower's campaign (cross-tenant read → not found).
	if _, err := s.GetCampaign(ctx, "acme", mp.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("acme GET maxpower campaign want errNotFound, got %v", err)
	}

	// acme cannot UPDATE maxpower's campaign (cross-tenant write → not found, no mutation).
	if _, err := s.UpdateCampaign(ctx, Campaign{ID: mp.ID, Org: "acme", Name: "HIJACK", Channel: "email", Status: "paused", UpdatedAt: 2}); !errors.Is(err, errNotFound) {
		t.Fatalf("acme UPDATE maxpower campaign want errNotFound, got %v", err)
	}
	got, _ := s.GetCampaign(ctx, "maxpower", mp.ID)
	if got.Name != "Spring Launch" {
		t.Fatalf("maxpower campaign must be unchanged, got %q", got.Name)
	}

	// acme cannot DELETE maxpower's campaign.
	deleted, err := s.DeleteCampaign(ctx, "acme", mp.ID)
	if err != nil {
		t.Fatalf("acme delete: %v", err)
	}
	if deleted {
		t.Fatalf("acme must not delete maxpower's campaign")
	}
	if _, err := s.GetCampaign(ctx, "maxpower", mp.ID); err != nil {
		t.Fatalf("maxpower campaign must survive acme delete: %v", err)
	}
}

// TestCampaignCRUD exercises the full campaign lifecycle round-trip.
func TestCampaignCRUD(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	created, err := s.CreateCampaign(ctx, Campaign{
		ID: "camp_1", Org: "hanzo", Name: "Q3 Growth", Channel: "google", Status: "draft",
		Objective: "signups", Budget: 100000, Spend: 0, CreatedAt: 10, UpdatedAt: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetCampaign(ctx, "hanzo", created.ID)
	if err != nil || got.Name != "Q3 Growth" || got.Channel != "google" {
		t.Fatalf("get after create: %+v err=%v", got, err)
	}

	// Update: flip to active, add spend.
	if _, err := s.UpdateCampaign(ctx, Campaign{
		ID: created.ID, Org: "hanzo", Name: "Q3 Growth", Channel: "google", Status: "active",
		Objective: "signups", Budget: 100000, Spend: 25000, UpdatedAt: 20,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.GetCampaign(ctx, "hanzo", created.ID)
	if got.Status != "active" || got.Spend != 25000 {
		t.Fatalf("update not applied: %+v", got)
	}

	// Status filter narrows the list.
	if rows, _ := s.ListCampaigns(ctx, "hanzo", "active", 100); len(rows) != 1 {
		t.Fatalf("status=active want 1, got %d", len(rows))
	}
	if rows, _ := s.ListCampaigns(ctx, "hanzo", "draft", 100); len(rows) != 0 {
		t.Fatalf("status=draft want 0, got %d", len(rows))
	}

	// Summary roll-up reflects the active campaign + summed budget/spend.
	total, active, budget, spend, err := s.Counts(ctx, "hanzo")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if total != 1 || active != 1 || budget != 100000 || spend != 25000 {
		t.Fatalf("summary want {1,1,100000,25000}, got {%d,%d,%d,%d}", total, active, budget, spend)
	}

	// Delete removes it.
	deleted, err := s.DeleteCampaign(ctx, "hanzo", created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, err := s.GetCampaign(ctx, "hanzo", created.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("get after delete want errNotFound, got %v", err)
	}
}
