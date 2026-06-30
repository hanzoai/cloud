package pricingsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// catModel builds one catalog entry. id "" means a Hanzo-style model whose
// overlay key is its name (mirrors the bundle: id || name).
func catModel(id, name, provider string, extra map[string]any) Model {
	m := Model{}
	if id != "" {
		m["id"] = id
	}
	if name != "" {
		m["name"] = name
	}
	if provider != "" {
		m["provider"] = provider
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// catSample is a 3-model fixture: one third-party (slashed id), one Hanzo
// (id==name), one other third-party.
func catSample() []Model {
	return []Model{
		catModel("anthropic/claude-opus-4.6", "Anthropic: Claude Opus 4.6", "Anthropic",
			map[string]any{"pricing": map[string]any{"input": float64(15), "output": float64(75)}}),
		catModel("", "zen4", "Hanzo", map[string]any{"tier": "ultra"}),
		catModel("openrouter/free-thing", "Free Thing", "OpenRouter", nil),
	}
}

func catHasID(ms []Model, id string) bool {
	for _, m := range ms {
		if modelID(m) == id {
			return true
		}
	}
	return false
}

func catFind(ms []Model, id string) Model {
	for _, m := range ms {
		if modelID(m) == id {
			return m
		}
	}
	return nil
}

// Default (empty overlay) => every model visible, unchanged, no admin metadata.
func TestVisibleCatalog_DefaultAllVisible(t *testing.T) {
	full := catSample()
	got := VisibleCatalog(full, map[string]Overlay{}, "acme", false)
	if len(got) != len(full) {
		t.Fatalf("default must show all: want %d, got %d", len(full), len(got))
	}
	for _, m := range got {
		if _, ok := m["_overlay"]; ok {
			t.Errorf("non-admin output must not carry _overlay: %v", m)
		}
	}
	// A no-org, non-admin caller also sees everything by default.
	if n := len(VisibleCatalog(full, map[string]Overlay{}, "", false)); n != len(full) {
		t.Errorf("empty-org default must show all: got %d", n)
	}
}

// Disabled entry is hidden from everyone EXCEPT orgs on its beta list.
func TestVisibleCatalog_DisabledHiddenExceptBeta(t *testing.T) {
	full := catSample()
	snap := map[string]Overlay{
		overlayKey(kindModel, "zen4"): {Kind: kindModel, ID: "zen4", Enabled: false, BetaOrgs: []string{"acme"}},
	}

	other := VisibleCatalog(full, snap, "other", false)
	if catHasID(other, "zen4") {
		t.Errorf("disabled zen4 must be hidden for non-beta org")
	}
	if len(other) != 2 {
		t.Errorf("want 2 visible for non-beta org, got %d", len(other))
	}

	acme := VisibleCatalog(full, snap, "acme", false)
	if !catHasID(acme, "zen4") {
		t.Errorf("disabled zen4 must be visible for beta org acme")
	}
	if len(acme) != 3 {
		t.Errorf("want 3 visible for beta org, got %d", len(acme))
	}

	if catHasID(VisibleCatalog(full, snap, "", false), "zen4") {
		t.Errorf("disabled zen4 must be hidden for empty org")
	}
}

// Override merges over the model (RFC 7386): nested object merges, a new key is
// added, and a null deletes a key.
func TestVisibleCatalog_OverrideMerged(t *testing.T) {
	full := catSample()
	patch := json.RawMessage(`{"pricing":{"input":5},"badge":"promo","name":null}`)
	snap := map[string]Overlay{
		overlayKey(kindModel, "anthropic/claude-opus-4.6"): {
			Kind: kindModel, ID: "anthropic/claude-opus-4.6", Enabled: true, Overrides: patch,
		},
	}
	got := VisibleCatalog(full, snap, "acme", false)
	m := catFind(got, "anthropic/claude-opus-4.6")
	if m == nil {
		t.Fatal("overridden model missing from output")
	}
	pricing, ok := m["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("pricing must remain an object, got %T", m["pricing"])
	}
	if pricing["input"] != float64(5) {
		t.Errorf("nested override: input want 5, got %v", pricing["input"])
	}
	if pricing["output"] != float64(75) {
		t.Errorf("deep-merge must preserve untouched output=75, got %v", pricing["output"])
	}
	if m["badge"] != "promo" {
		t.Errorf("override must add badge=promo, got %v", m["badge"])
	}
	if _, ok := m["name"]; ok {
		t.Errorf("RFC 7386 null must delete name, still present: %v", m["name"])
	}
	// The override must NOT mutate the caller's input model.
	if _, ok := full[0]["badge"]; ok {
		t.Errorf("gate mutated the input catalog (badge leaked onto source)")
	}
}

// Admin sees every entry, disabled included, each annotated with overlay state.
func TestVisibleCatalog_AdminSeesAll(t *testing.T) {
	full := catSample()
	snap := map[string]Overlay{
		overlayKey(kindModel, "zen4"):          {Kind: kindModel, ID: "zen4", Enabled: false},
		overlayKey(kindProvider, "OpenRouter"): {Kind: kindProvider, ID: "OpenRouter", Enabled: false, BetaOrgs: []string{"acme"}},
	}
	got := VisibleCatalog(full, snap, "", true)
	if len(got) != 3 {
		t.Fatalf("admin must see all 3 models, got %d", len(got))
	}
	zen := catFind(got, "zen4")
	ov, ok := zen["_overlay"].(map[string]any)
	if !ok {
		t.Fatal("admin model must carry _overlay annotation")
	}
	if ov["modelEnabled"] != false {
		t.Errorf("annotation modelEnabled want false, got %v", ov["modelEnabled"])
	}
	// The OpenRouter model is disabled via its PROVIDER; admin still sees it,
	// annotated providerEnabled=false.
	free := catFind(got, "openrouter/free-thing")
	fov := free["_overlay"].(map[string]any)
	if fov["providerEnabled"] != false {
		t.Errorf("annotation providerEnabled want false, got %v", fov["providerEnabled"])
	}
}

// Disabling a PROVIDER cascades to hide all its models (except provider-beta orgs).
func TestVisibleCatalog_ProviderCascade(t *testing.T) {
	full := catSample()
	snap := map[string]Overlay{
		overlayKey(kindProvider, "Anthropic"): {Kind: kindProvider, ID: "Anthropic", Enabled: false, BetaOrgs: []string{"acme"}},
	}
	other := VisibleCatalog(full, snap, "other", false)
	if catHasID(other, "anthropic/claude-opus-4.6") {
		t.Errorf("model under a disabled provider must be hidden")
	}
	if len(other) != 2 {
		t.Errorf("want 2 visible, got %d", len(other))
	}
	acme := VisibleCatalog(full, snap, "acme", false)
	if !catHasID(acme, "anthropic/claude-opus-4.6") {
		t.Errorf("provider beta org must see the provider's models")
	}
}

func TestVisibleProviders(t *testing.T) {
	providers := map[string]any{
		"Anthropic":  map[string]any{"total": float64(10)},
		"OpenRouter": map[string]any{"total": float64(5)},
	}
	snap := map[string]Overlay{
		overlayKey(kindProvider, "Anthropic"): {
			Kind: kindProvider, ID: "Anthropic", Enabled: false,
			Overrides: json.RawMessage(`{"label":"hidden"}`),
		},
	}
	got := VisibleProviders(providers, snap, "other", false)
	if _, ok := got["Anthropic"]; ok {
		t.Errorf("disabled provider must be hidden for non-beta org")
	}
	if _, ok := got["OpenRouter"]; !ok {
		t.Errorf("enabled provider must remain visible")
	}

	adm := VisibleProviders(providers, snap, "", true)
	a, ok := adm["Anthropic"].(map[string]any)
	if !ok {
		t.Fatal("admin must see disabled provider")
	}
	if a["label"] != "hidden" {
		t.Errorf("provider override must merge: label want hidden, got %v", a["label"])
	}
	ov := a["_overlay"].(map[string]any)
	if ov["providerEnabled"] != false {
		t.Errorf("admin provider annotation providerEnabled want false, got %v", ov["providerEnabled"])
	}
}

// Store: idempotent open, empty=clean, upsert/get/update round-trip, and the
// end-to-end gate through the store.
func TestCatalogStore_RoundTrip(t *testing.T) {
	c, err := openCatalog(":memory:")
	if err != nil {
		t.Fatalf("openCatalog: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	if err := c.migrate(); err != nil { // idempotent re-create
		t.Fatalf("re-migrate must be idempotent: %v", err)
	}
	if snap, err := c.Snapshot(ctx); err != nil || len(snap) != 0 {
		t.Fatalf("fresh store must be empty: len=%d err=%v", len(snap), err)
	}

	o := Overlay{
		Kind: kindModel, ID: "anthropic/claude-opus-4.6", Enabled: false,
		BetaOrgs: []string{"acme", "beta"}, Overrides: json.RawMessage(`{"badge":"x"}`), UpdatedAt: 123,
	}
	if err := c.Upsert(ctx, o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, ok, err := c.Get(ctx, kindModel, "anthropic/claude-opus-4.6")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Enabled || len(got.BetaOrgs) != 2 || string(got.Overrides) != `{"badge":"x"}` || got.UpdatedAt != 123 {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Re-upsert (PK conflict path) flips enabled and clears beta/overrides.
	if err := c.Upsert(ctx, Overlay{Kind: kindModel, ID: "anthropic/claude-opus-4.6", Enabled: true, UpdatedAt: 200}); err != nil {
		t.Fatalf("update upsert: %v", err)
	}
	got2, _, _ := c.Get(ctx, kindModel, "anthropic/claude-opus-4.6")
	if !got2.Enabled || len(got2.BetaOrgs) != 0 || len(got2.Overrides) != 0 {
		t.Errorf("update did not replace row: %+v", got2)
	}

	// Absent row.
	if _, ok, _ := c.Get(ctx, kindModel, "nope"); ok {
		t.Errorf("absent row must report ok=false")
	}

	// Gate through the store.
	_ = c.Upsert(ctx, Overlay{Kind: kindModel, ID: "zen4", Enabled: false, BetaOrgs: []string{"acme"}})
	full := catSample()
	if got, err := c.Models(ctx, full, "other", false); err != nil || catHasID(got, "zen4") {
		t.Errorf("Models: zen4 must be hidden for non-beta org (err=%v)", err)
	}
	if got, _ := c.Models(ctx, full, "acme", false); !catHasID(got, "zen4") {
		t.Errorf("Models: zen4 must be visible for beta org acme")
	}
	if got, _ := c.Models(ctx, full, "", true); !catHasID(got, "zen4") {
		t.Errorf("Models: admin must see disabled zen4")
	}
}

func containsStr(list []any, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// asModels reads a model array that may be []Model (after GateRootData) or []any
// (raw decoded JSON).
func asModels(v any) []Model {
	switch t := v.(type) {
	case []Model:
		return t
	case []any:
		m, _ := modelArray(t)
		return m
	}
	return nil
}

// The root /v1/pricing blob (raw arrays + id-reference lists + providers dict +
// families) must be gated identically to the leaves — no un-gated second source.
func TestGateRootData(t *testing.T) {
	root := func() map[string]any {
		return map[string]any{
			"updated": "x",
			"hanzoModels": []any{
				map[string]any{"name": "zen4", "tier": "ultra"},
				map[string]any{"name": "zen5"},
			},
			"thirdPartyModels": []any{
				map[string]any{"id": "anthropic/claude-opus-4.6", "name": "Opus", "provider": "Anthropic"},
				map[string]any{"id": "openrouter/free-thing", "name": "Free", "provider": "OpenRouter"},
			},
			"freeModels": []any{"openrouter/free-thing", "anthropic/claude-opus-4.6"},
			"providers": map[string]any{
				"Anthropic":  map[string]any{"total": float64(1)},
				"OpenRouter": map[string]any{"total": float64(1)},
			},
			"families": []any{
				map[string]any{"id": "zen", "models": []any{"zen4", "zen5"}},
			},
			"summary": map[string]any{"totalModels": float64(4)},
		}
	}
	// zen4 disabled (model); Anthropic disabled (provider) with beta acme.
	snap := map[string]Overlay{
		overlayKey(kindModel, "zen4"):         {Kind: kindModel, ID: "zen4", Enabled: false},
		overlayKey(kindProvider, "Anthropic"): {Kind: kindProvider, ID: "Anthropic", Enabled: false, BetaOrgs: []string{"acme"}},
	}

	// Non-beta customer: zen4 and the Anthropic model gone from EVERY field.
	d := root()
	GateRootData(d, snap, "other", false)
	hz := asModels(d["hanzoModels"])
	if catHasID(hz, "zen4") {
		t.Errorf("root.hanzoModels leaked disabled zen4")
	}
	if !catHasID(hz, "zen5") {
		t.Errorf("root.hanzoModels dropped enabled zen5")
	}
	tp := asModels(d["thirdPartyModels"])
	if catHasID(tp, "anthropic/claude-opus-4.6") {
		t.Errorf("root.thirdPartyModels leaked provider-disabled model")
	}
	if fm := d["freeModels"].([]any); containsStr(fm, "anthropic/claude-opus-4.6") {
		t.Errorf("root.freeModels leaked provider-disabled id")
	} else if !containsStr(fm, "openrouter/free-thing") {
		t.Errorf("root.freeModels dropped a visible id")
	}
	famModels := d["families"].([]any)[0].(map[string]any)["models"].([]any)
	if containsStr(famModels, "zen4") {
		t.Errorf("root.families leaked disabled zen4 ref")
	}
	if !containsStr(famModels, "zen5") {
		t.Errorf("root.families dropped enabled zen5 ref")
	}
	provs := d["providers"].(map[string]any)
	if _, ok := provs["Anthropic"]; ok {
		t.Errorf("root.providers leaked disabled Anthropic")
	}
	if _, ok := provs["OpenRouter"]; !ok {
		t.Errorf("root.providers dropped OpenRouter")
	}
	if d["summary"].(map[string]any)["totalModels"] != float64(4) {
		t.Errorf("summary aggregate counts must be untouched")
	}

	// Provider beta org sees the Anthropic model again; zen4 still hidden (no beta).
	d2 := root()
	GateRootData(d2, snap, "acme", false)
	if tp2 := asModels(d2["thirdPartyModels"]); !catHasID(tp2, "anthropic/claude-opus-4.6") {
		t.Errorf("provider beta org must see the model in root")
	}
	if hz2 := asModels(d2["hanzoModels"]); catHasID(hz2, "zen4") {
		t.Errorf("zen4 must stay hidden for acme (no model beta)")
	}

	// Admin sees everything; id-reference lists untouched.
	d3 := root()
	GateRootData(d3, snap, "", true)
	if hz3 := asModels(d3["hanzoModels"]); !catHasID(hz3, "zen4") {
		t.Errorf("admin must see disabled zen4 in root")
	}
	if len(d3["freeModels"].([]any)) != 2 {
		t.Errorf("admin freeModels must be untouched")
	}
	if _, ok := d3["providers"].(map[string]any)["Anthropic"]; !ok {
		t.Errorf("admin must see disabled provider in root")
	}
}

func TestCheckOverride(t *testing.T) {
	ok := []json.RawMessage{
		json.RawMessage(`{"a":1}`),
		json.RawMessage(`null`),
		json.RawMessage(``),
		json.RawMessage(`{"pricing":{"input":5}}`),
	}
	for _, r := range ok {
		if err := checkOverride(r); err != nil {
			t.Errorf("checkOverride(%s) must pass: %v", r, err)
		}
	}
	bad := []json.RawMessage{
		json.RawMessage(`[1,2]`), // array
		json.RawMessage(`"x"`),   // scalar
		json.RawMessage(`123`),   // number
	}
	for _, r := range bad {
		if checkOverride(r) == nil {
			t.Errorf("checkOverride(%s) must be rejected", r)
		}
	}
	// Size cap.
	big := json.RawMessage(`{"a":"` + strings.Repeat("x", maxOverrideBytes) + `"}`)
	if checkOverride(big) == nil {
		t.Errorf("oversize override must be rejected")
	}
	// Depth cap: N nested objects => jsonDepth N.
	tooDeep := json.RawMessage(strings.Repeat(`{"a":`, maxOverrideDepth+2) + "1" + strings.Repeat("}", maxOverrideDepth+2))
	if checkOverride(tooDeep) == nil {
		t.Errorf("over-deep override must be rejected")
	}
	atLimit := json.RawMessage(strings.Repeat(`{"a":`, maxOverrideDepth-1) + "1" + strings.Repeat("}", maxOverrideDepth-1))
	if err := checkOverride(atLimit); err != nil {
		t.Errorf("at-limit depth override must pass: %v", err)
	}
}
