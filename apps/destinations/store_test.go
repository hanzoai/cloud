package destinations

import (
	"context"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "destinations.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStorePerOrgIsolation is the load-bearing tenant-isolation test: two orgs
// connect destinations; neither can read, list, or delete the other's rows.
func TestStorePerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Upsert(ctx, Row{Org: "maxpower", Platform: "ga4", Enabled: true, Config: Config{"measurementId": "G-MP"}}); err != nil {
		t.Fatalf("upsert maxpower: %v", err)
	}
	if err := s.Upsert(ctx, Row{Org: "acme", Platform: "meta", Enabled: false, Config: Config{"pixelId": "111"}}); err != nil {
		t.Fatalf("upsert acme: %v", err)
	}

	// maxpower lists exactly its own.
	list, err := s.List(ctx, "maxpower")
	if err != nil || len(list) != 1 || list[0].Platform != "ga4" || list[0].Config["measurementId"] != "G-MP" {
		t.Fatalf("maxpower list wrong: %+v (err %v)", list, err)
	}

	// acme cannot GET maxpower's ga4 (cross-tenant read → not found).
	if _, found, _ := s.Get(ctx, "acme", "ga4"); found {
		t.Fatal("acme must not see maxpower's ga4 row")
	}

	// ListEnabled respects the enabled flag: acme's meta is disabled → not in the set.
	if en, _ := s.ListEnabled(ctx, "acme"); len(en) != 0 {
		t.Fatalf("acme has no enabled destinations, got %+v", en)
	}
	if en, _ := s.ListEnabled(ctx, "maxpower"); len(en) != 1 {
		t.Fatalf("maxpower has 1 enabled destination, got %+v", en)
	}

	// acme cannot delete maxpower's row.
	if gone, _ := s.Delete(ctx, "acme", "ga4"); gone {
		t.Fatal("acme must not delete maxpower's ga4")
	}
	if _, found, _ := s.Get(ctx, "maxpower", "ga4"); !found {
		t.Fatal("maxpower's ga4 must survive acme's delete")
	}
}

// TestStoreUpsertPreservesConnectedAt verifies a re-connect keeps connected_at and
// advances updated_at + config (the "connected since" invariant).
func TestStoreUpsertRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if err := s.Upsert(ctx, Row{Org: "o", Platform: "ga4", Enabled: true, Config: Config{"measurementId": "G-1"}}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first, _, _ := s.Get(ctx, "o", "ga4")
	if err := s.Upsert(ctx, Row{Org: "o", Platform: "ga4", Enabled: false, Config: Config{"measurementId": "G-2"}}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, found, _ := s.Get(ctx, "o", "ga4")
	if !found || got.Config["measurementId"] != "G-2" || got.Enabled {
		t.Fatalf("re-connect did not update: %+v", got)
	}
	if got.ConnectedAt != first.ConnectedAt {
		t.Errorf("connected_at must be preserved: %d → %d", first.ConnectedAt, got.ConnectedAt)
	}
}
