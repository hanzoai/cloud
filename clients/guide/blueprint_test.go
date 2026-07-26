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
	if _, err := bpStore.SeedOrUpgrade(ctx, "", brandDoc, seedVersion, 1); err != nil {
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
	if _, err := dstore.SeedOrUpgrade(ctx, "", disabledDoc, seedVersion, 1); err != nil {
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

// TestSeedVersionAwareUpgrade proves the version-aware seed UPGRADES an UNEDITED seed row
// in place when the embedded generation is newer — the mechanism that ships the 888 corpus
// to a deployment already seeded with the old 114-journey — while never downgrading.
func TestSeedVersionAwareUpgrade(t *testing.T) {
	ctx := context.Background()
	store := testBlueprintStore(t)
	oldDoc, _ := json.Marshal(Blueprint{Version: "seed-v1", Steps: []Step{{ID: "a", Title: "A"}}})
	newDoc, _ := json.Marshal(Blueprint{Version: "seed-v2", Steps: []Step{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}})

	// Seed generation 1.
	if act, err := store.SeedOrUpgrade(ctx, "", oldDoc, 1, 100); err != nil || act != SeedInserted {
		t.Fatalf("first seed want SeedInserted, got %q err=%v", act, err)
	}
	// Re-seed at the SAME generation → no-op (idempotent redeploy).
	if act, err := store.SeedOrUpgrade(ctx, "", oldDoc, 1, 101); err != nil || act != SeedNone {
		t.Fatalf("re-seed same generation want SeedNone, got %q err=%v", act, err)
	}
	// A NEWER generation over the UNEDITED seed → UPGRADE in place (still version 1).
	if act, err := store.SeedOrUpgrade(ctx, "", newDoc, 2, 102); err != nil || act != SeedUpgraded {
		t.Fatalf("newer generation over unedited seed want SeedUpgraded, got %q err=%v", act, err)
	}
	doc, version, _, ok, err := store.LatestResolved(ctx, "")
	if err != nil || !ok {
		t.Fatalf("latest after upgrade: ok=%v err=%v", ok, err)
	}
	var got Blueprint
	_ = json.Unmarshal(doc, &got)
	if version != 1 || got.Version != "seed-v2" {
		t.Fatalf("upgrade must replace the seed IN PLACE at version 1, got version=%d blueprint=%q", version, got.Version)
	}
	// Equal or older generation must NEVER regress the seed.
	if act, err := store.SeedOrUpgrade(ctx, "", oldDoc, 2, 103); err != nil || act != SeedNone {
		t.Fatalf("equal generation want SeedNone, got %q err=%v", act, err)
	}
	if act, err := store.SeedOrUpgrade(ctx, "", oldDoc, 1, 104); err != nil || act != SeedNone {
		t.Fatalf("older generation must never downgrade, got %q err=%v", act, err)
	}
	// The upgrade keeps a SINGLE seed version (no history churn).
	if vs, _ := store.ListVersions(ctx, ""); len(vs) != 1 || vs[0].Version != 1 {
		t.Fatalf("upgrade must keep a single seed version, got %+v", vs)
	}
}

// TestSeedNeverClobbersAdminEdit is the LOAD-BEARING invariant: once a brand carries an
// admin edit, NO seed generation — however new — ever touches it again. An admin edit at
// any version survives forever.
func TestSeedNeverClobbersAdminEdit(t *testing.T) {
	ctx := context.Background()
	store := testBlueprintStore(t)
	seedDoc, _ := json.Marshal(Blueprint{Version: "seed-v1", Steps: []Step{{ID: "a", Title: "A"}}})
	if act, err := store.SeedOrUpgrade(ctx, "", seedDoc, 1, 100); err != nil || act != SeedInserted {
		t.Fatalf("seed want SeedInserted, got %q err=%v", act, err)
	}
	// An admin edits it → version 2, stamped source="admin".
	adminDoc, _ := json.Marshal(Blueprint{Version: "admin-edit", Steps: []Step{{ID: "x", Title: "X"}}})
	if v, err := store.SaveVersion(ctx, "", adminDoc, 101); err != nil || v != 2 {
		t.Fatalf("admin edit want version 2, got v=%d err=%v", v, err)
	}
	// A newer seed generation MUST NOT clobber the admin edit — even a far-future one.
	newSeed, _ := json.Marshal(Blueprint{Version: "seed-v2", Steps: []Step{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}})
	if act, err := store.SeedOrUpgrade(ctx, "", newSeed, 2, 102); err != nil || act != SeedNone {
		t.Fatalf("v2 seed over an admin edit MUST be SeedNone (never clobber), got %q err=%v", act, err)
	}
	if act, err := store.SeedOrUpgrade(ctx, "", newSeed, 99, 103); err != nil || act != SeedNone {
		t.Fatalf("any newer seed over an admin edit MUST be SeedNone, got %q err=%v", act, err)
	}
	// The admin edit is still authoritative + byte-intact.
	doc, version, _, ok, err := store.LatestResolved(ctx, "")
	if err != nil || !ok {
		t.Fatalf("latest: ok=%v err=%v", ok, err)
	}
	var got Blueprint
	_ = json.Unmarshal(doc, &got)
	if version != 2 || got.Version != "admin-edit" {
		t.Fatalf("admin edit clobbered by a seed: version=%d blueprint=%q", version, got.Version)
	}
}

// TestMigrateBackfillsLegacyAdminEdit proves a LEGACY admin edit (a version >= 2 row that
// predates the source column and defaulted to 'seed') is backfilled to source="admin" by
// migrate(), so SeedOrUpgrade protects it exactly like a fresh admin edit.
func TestMigrateBackfillsLegacyAdminEdit(t *testing.T) {
	ctx := context.Background()
	store := testBlueprintStore(t)
	// Emulate the pre-migration shape: a seed at v1 + an admin edit at v2, both with source
	// left at the column default 'seed' (as an old DB would have after the column was added
	// but before any provenance was written).
	seedDoc, _ := json.Marshal(Blueprint{Version: "legacy-seed", Steps: []Step{{ID: "a", Title: "A"}}})
	adminDoc, _ := json.Marshal(Blueprint{Version: "legacy-admin", Steps: []Step{{ID: "x", Title: "X"}}})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO guide_blueprint (brand,version,doc,updated_at,source,seed_version) VALUES ('',1,?,1,'seed',0)`, string(seedDoc)); err != nil {
		t.Fatalf("insert legacy seed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO guide_blueprint (brand,version,doc,updated_at,source,seed_version) VALUES ('',2,?,2,'seed',0)`, string(adminDoc)); err != nil {
		t.Fatalf("insert legacy admin: %v", err)
	}
	// Re-run the migration → the version-2 row is stamped admin (self-healing backfill).
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// A newer seed generation must now leave the legacy admin edit untouched.
	newSeed, _ := json.Marshal(Blueprint{Version: "seed-v9", Steps: []Step{{ID: "a", Title: "A"}}})
	if act, err := store.SeedOrUpgrade(ctx, "", newSeed, 9, 3); err != nil || act != SeedNone {
		t.Fatalf("legacy admin edit must be protected after backfill, got %q err=%v", act, err)
	}
	doc, version, _, _, _ := store.LatestResolved(ctx, "")
	var got Blueprint
	_ = json.Unmarshal(doc, &got)
	if version != 2 || got.Version != "legacy-admin" {
		t.Fatalf("legacy admin edit not preserved: version=%d blueprint=%q", version, got.Version)
	}
}

// TestAssembledSeedParsesAndValidates proves the embedded full-genome seed (base.yaml) is
// well-formed: it parses + validates at init (mustBlueprint), carries the 64-principle
// spine + the 1002-strategy corpus (888 modern + 114 heritage), and EVERY strategy files
// under a real spine principle with NO duplicate id across the whole corpus.
func TestAssembledSeedParsesAndValidates(t *testing.T) {
	bp := defaultBlueprint // parsed + validated at package init
	if got := len(bp.Principles); got != 64 {
		t.Fatalf("assembled seed principles want 64, got %d", got)
	}
	if got := len(bp.Sections); got != 12 {
		t.Fatalf("assembled seed sections want 12, got %d", got)
	}
	if got := len(bp.Steps); got != 67 {
		t.Fatalf("assembled seed steps want 67, got %d", got)
	}
	if got := len(bp.Templates); got != 6 {
		t.Fatalf("assembled seed templates want 6, got %d", got)
	}
	if got := len(bp.Strategies); got != 1002 {
		t.Fatalf("assembled seed strategies want 1002, got %d", got)
	}
	slugs := make(map[string]bool, len(bp.Principles))
	for _, p := range bp.Principles {
		if p.Slug == "" {
			t.Fatalf("principle %d has empty slug", p.N)
		}
		if slugs[p.Slug] {
			t.Fatalf("duplicate principle slug %q", p.Slug)
		}
		slugs[p.Slug] = true
	}
	ids := make(map[string]bool, len(bp.Strategies))
	modern, heritage := 0, 0
	for _, s := range bp.Strategies {
		if ids[s.ID] {
			t.Fatalf("duplicate strategy id %q across the corpus", s.ID)
		}
		ids[s.ID] = true
		if s.Principle == "" || !slugs[s.Principle] {
			t.Fatalf("strategy %q files under unresolved principle %q", s.ID, s.Principle)
		}
		switch s.Era {
		case "modern":
			modern++
		case "heritage":
			heritage++
		default:
			t.Fatalf("strategy %q has unknown era %q", s.ID, s.Era)
		}
	}
	if modern != 888 || heritage != 114 {
		t.Fatalf("corpus era split want modern=888 heritage=114, got modern=%d heritage=%d", modern, heritage)
	}
}
