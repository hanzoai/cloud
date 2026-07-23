package guide

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// testBlueprintStore opens a real shared blueprint store on a temp dir (cek is keyed by
// TestMain, so this exercises the production open path).
func testBlueprintStore(t *testing.T) *BlueprintStore {
	t.Helper()
	store, err := openBlueprintStore(filepath.Join(t.TempDir(), "guide-blueprint.db"))
	if err != nil {
		t.Fatalf("open blueprint store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func stepIDs(c Curriculum) []string {
	out := make([]string, len(c.Steps))
	for i, s := range c.Steps {
		out[i] = s.ID
	}
	return out
}

// TestEnableDisablePrecedence proves the projection rule (reconciliation c): a disabled
// SECTION drops all its steps; a disabled STEP drops out; and a surviving step's
// dependency on a disabled/absent step is DROPPED (not dangling), so the enabled journey
// is always a valid engine curriculum.
func TestEnableDisablePrecedence(t *testing.T) {
	off := false
	bp := Blueprint{
		Version: "t",
		Sections: []Section{
			{ID: "s-on", Title: "On"},
			{ID: "s-off", Title: "Off", Enabled: &off}, // disabled section
		},
		Steps: []Step{
			{ID: "a", Section: "s-on", Title: "A"},
			{ID: "b", Section: "s-on", Title: "B", Dependencies: []string{"a"}, Enabled: &off}, // disabled step
			{ID: "c", Section: "s-on", Title: "C", Dependencies: []string{"b"}},                // dep on disabled b
			{ID: "d", Section: "s-off", Title: "D"},                                            // in disabled section
			{ID: "e", Section: "s-on", Title: "E", Dependencies: []string{"d"}},                // dep on section-disabled d
		},
	}
	// The authored blueprint is valid (b and d exist, so no dangling in the graph).
	if err := bp.Validate(); err != nil {
		t.Fatalf("authored blueprint must be valid: %v", err)
	}
	cur := bp.Curriculum()

	// Only the enabled, in-enabled-section steps survive: a, c, e.
	got := stepIDs(cur)
	want := map[string]bool{"a": true, "c": true, "e": true}
	if len(got) != 3 {
		t.Fatalf("projection want 3 enabled steps [a c e], got %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected step %q in the enabled projection %v", id, got)
		}
	}
	if _, ok := cur.stepByID("b"); ok {
		t.Fatal("disabled step b must drop from the journey")
	}
	if _, ok := cur.stepByID("d"); ok {
		t.Fatal("section-disabled step d must drop from the journey")
	}

	// c's dep on disabled b and e's dep on section-disabled d are DROPPED — both are
	// available immediately, and the projection is a valid (dangling-free) curriculum.
	if err := Validate(cur); err != nil {
		t.Fatalf("enabled projection must be a valid engine curriculum: %v", err)
	}
	states := map[string]State{}
	if !cur.Available(states, "c") {
		t.Fatal("c must be available: its only dep (disabled b) is dropped")
	}
	if !cur.Available(states, "e") {
		t.Fatal("e must be available: its only dep (section-disabled d) is dropped")
	}
	if c, _ := cur.stepByID("c"); len(c.Dependencies) != 0 {
		t.Fatalf("c's dep on disabled b must be filtered out, got %v", c.Dependencies)
	}
}

// TestResolutionTiers proves the three decomplected tiers resolve in precedence order:
// org override > brand blueprint (DB) > embedded fixture.
func TestResolutionTiers(t *testing.T) {
	ctx := context.Background()
	fixture := Blueprint{Version: "fixture-v", Steps: []Step{{ID: "f1", Title: "F1"}}}

	// A store seeded with a DISTINCT brand blueprint under the base key.
	bpStore := testBlueprintStore(t)
	brandDoc, _ := json.Marshal(Blueprint{Version: "brand-v", Steps: []Step{{ID: "b1", Title: "B1"}}})
	if _, err := bpStore.SeedIfAbsent(ctx, "", brandDoc, 1); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	st := state{blueprints: bpStore, brand: "", defBlueprint: fixture}
	orgStore := testStore(t)

	// No override → the brand blueprint from the DB (tier 2).
	if bp, tier := st.resolveBlueprint(ctx, orgStore); tier != "brand" || bp.Version != "brand-v" {
		t.Fatalf("no override → brand tier want (brand, brand-v), got (%s, %s)", tier, bp.Version)
	}

	// An org override wins (tier 1).
	orgDoc, _ := json.Marshal(Blueprint{Version: "org-v", Steps: []Step{{ID: "o1", Title: "O1"}}})
	if err := orgStore.SetCurriculum(ctx, orgDoc, 2); err != nil {
		t.Fatalf("set override: %v", err)
	}
	if bp, tier := st.resolveBlueprint(ctx, orgStore); tier != "org" || bp.Version != "org-v" {
		t.Fatalf("override present → org tier want (org, org-v), got (%s, %s)", tier, bp.Version)
	}

	// With an EMPTY DB (no seeded brand blueprint) and no override, the embedded fixture
	// is the fail-safe (tier 3).
	empty := testBlueprintStore(t)
	st3 := state{blueprints: empty, brand: "", defBlueprint: fixture}
	if bp, tier := st3.resolveBlueprint(ctx, testStore(t)); tier != "fixture" || bp.Version != "fixture-v" {
		t.Fatalf("empty DB + no override → fixture tier want (fixture, fixture-v), got (%s, %s)", tier, bp.Version)
	}

	// A disabled brand blueprint is SKIPPED — resolution falls through to the fixture.
	off := false
	disabledDoc, _ := json.Marshal(Blueprint{Version: "disabled-v", Enabled: &off, Steps: []Step{{ID: "x", Title: "X"}}})
	dstore := testBlueprintStore(t)
	if _, err := dstore.SeedIfAbsent(ctx, "", disabledDoc, 1); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
	st4 := state{blueprints: dstore, brand: "", defBlueprint: fixture}
	if bp, tier := st4.resolveBlueprint(ctx, testStore(t)); tier != "fixture" || bp.Version != "fixture-v" {
		t.Fatalf("disabled brand blueprint must be skipped → fixture, got (%s, %s)", tier, bp.Version)
	}
}

// TestSeedIdempotentNoClobber proves the seed-if-absent contract: seeding is a one-time
// insert per brand, so a redeploy (re-seed) NEVER clobbers a SuperAdmin edit.
func TestSeedIdempotentNoClobber(t *testing.T) {
	ctx := context.Background()
	store := testBlueprintStore(t)

	// First bootstrap seeds the base + every brand (base "" and zoo).
	n, err := seedBlueprints(ctx, store)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != 1+len(BrandCurriculums()) {
		t.Fatalf("first seed must insert base + every brand, got %d", n)
	}

	// A SuperAdmin edits the base blueprint → version 2.
	editDoc, _ := json.Marshal(Blueprint{Version: "admin-edit", Steps: []Step{{ID: "x", Title: "X"}}})
	if v, err := store.SaveVersion(ctx, "", editDoc, 3); err != nil || v != 2 {
		t.Fatalf("admin edit want version 2, got v=%d err=%v", v, err)
	}

	// A redeploy re-runs the seed — it must be a NO-OP (every brand already present).
	if n2, err := seedBlueprints(ctx, store); err != nil || n2 != 0 {
		t.Fatalf("re-seed must be a no-op, seeded=%d err=%v", n2, err)
	}

	// The admin edit is still authoritative — never clobbered back to the seed.
	doc, version, _, ok, err := store.LatestResolved(ctx, "")
	if err != nil || !ok {
		t.Fatalf("latest after re-seed: ok=%v err=%v", ok, err)
	}
	var got Blueprint
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatalf("decode latest: %v", err)
	}
	if version != 2 || got.Version != "admin-edit" {
		t.Fatalf("re-seed clobbered the admin edit: version=%d blueprint=%q", version, got.Version)
	}
}
