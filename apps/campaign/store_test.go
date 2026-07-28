package campaign

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "campaign.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPerOrgIsolation is the load-bearing tenant-isolation test: two orgs write
// campaigns; neither can list, get, save, or delete the other's rows.
func TestPerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	mp, err := s.CreateCampaign(ctx, Campaign{
		ID: "cmp_mp", Org: "maxpower", Name: "Spring Launch", Status: StatusDraft,
		Content: []string{"creative-a"}, Channels: []ChannelSpec{{Kind: KindPaid, Platform: "meta", Status: chanPending}},
		Budget: 50000, CreatedAt: 1, UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("create maxpower campaign: %v", err)
	}
	if _, err := s.CreateCampaign(ctx, Campaign{
		ID: "cmp_acme", Org: "acme", Name: "Acme Blast", Status: StatusDraft, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create acme campaign: %v", err)
	}

	// maxpower lists exactly its own, with channels + content round-tripped.
	list, err := s.ListCampaigns(ctx, "maxpower", "", 100)
	if err != nil {
		t.Fatalf("list maxpower: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Spring Launch" {
		t.Fatalf("maxpower must see [Spring Launch], got %+v", list)
	}
	if len(list[0].Channels) != 1 || list[0].Channels[0].Kind != KindPaid || list[0].Channels[0].Platform != "meta" {
		t.Fatalf("channels not round-tripped: %+v", list[0].Channels)
	}
	if len(list[0].Content) != 1 || list[0].Content[0] != "creative-a" {
		t.Fatalf("content not round-tripped: %+v", list[0].Content)
	}

	// acme cannot GET maxpower's campaign (cross-tenant read → not found).
	if _, err := s.GetCampaign(ctx, "acme", mp.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("acme GET maxpower campaign want errNotFound, got %v", err)
	}

	// acme cannot SAVE over maxpower's campaign (cross-tenant write → not found).
	hijack := mp
	hijack.Org = "acme"
	hijack.Name = "HIJACK"
	if _, err := s.Save(ctx, hijack); !errors.Is(err, errNotFound) {
		t.Fatalf("acme SAVE maxpower campaign want errNotFound, got %v", err)
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

// TestCampaignCRUD exercises the full campaign lifecycle round-trip incl the
// JSON channel/content columns and the summary roll-up.
func TestCampaignCRUD(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	created, err := s.CreateCampaign(ctx, Campaign{
		ID: "cmp_1", Org: "hanzo", Name: "Q3 Growth", Status: StatusDraft, Audience: "warm-leads",
		Content:  []string{"hero-a", "hero-b"},
		Channels: []ChannelSpec{{Kind: KindPaid, Platform: "meta", Status: chanPending}, {Kind: KindEmail, Platform: "sendgrid", Status: chanPending}},
		Budget:   100000, CreatedAt: 10, UpdatedAt: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetCampaign(ctx, "hanzo", created.ID)
	if err != nil || got.Name != "Q3 Growth" || got.Audience != "warm-leads" || len(got.Channels) != 2 {
		t.Fatalf("get after create: %+v err=%v", got, err)
	}

	// Save: flip to live, mutate a channel outcome.
	got.Status = StatusLive
	got.Channels[0].Status = chanLive
	got.Channels[0].ExternalID = "ext_meta_1"
	got.UpdatedAt = 20
	if _, err := s.Save(ctx, got); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _ = s.GetCampaign(ctx, "hanzo", created.ID)
	if got.Status != StatusLive || got.Channels[0].Status != chanLive || got.Channels[0].ExternalID != "ext_meta_1" {
		t.Fatalf("save not applied: %+v", got)
	}

	// Status filter narrows the list.
	if rows, _ := s.ListCampaigns(ctx, "hanzo", StatusLive, 100); len(rows) != 1 {
		t.Fatalf("status=live want 1, got %d", len(rows))
	}
	if rows, _ := s.ListCampaigns(ctx, "hanzo", StatusDraft, 100); len(rows) != 0 {
		t.Fatalf("status=draft want 0, got %d", len(rows))
	}

	// Summary roll-up reflects the live campaign + summed budget.
	total, live, budget, err := s.Counts(ctx, "hanzo")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if total != 1 || live != 1 || budget != 100000 {
		t.Fatalf("summary want {1,1,100000}, got {%d,%d,%d}", total, live, budget)
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
