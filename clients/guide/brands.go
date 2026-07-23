package guide

import (
	"embed"
	"sort"
	"strings"
)

// brands.go is the WHITE-LABEL tier of the blueprint resolution chain. A brand
// (hanzo/zoo/lux) can ship its own default blueprint as DATA — a brands/<brand>.yaml
// file — so a Zoo or Lux console runs ITS OWN journey, not Hanzo's. Adding a brand's
// journey is a new YAML file, never a code change — exactly the same Blueprint schema
// as base.yaml. Every embedded brand file is parsed AND validated at init: a malformed
// brand blueprint is a BUILD-TIME fault (panic at package init), mirroring the base
// default's mustBlueprint discipline — never a runtime surprise.
//
// These embedded brand blueprints are the SEED for the shared brand tier: on Mount the
// base ("") + each brand's blueprint are seeded-if-absent into the shared store
// (blueprint_store.go), after which the DB is authoritative. When the DB is unreachable
// they are also the fail-safe fixture the resolver falls through to.

//go:embed brands
var brandFS embed.FS

// brandBlueprints maps a lowercased brand name to its default blueprint. Built once at
// init from the embedded brands/ directory.
var brandBlueprints = mustBrands()

func mustBrands() map[string]Blueprint {
	m := map[string]Blueprint{}
	entries, err := brandFS.ReadDir("brands")
	if err != nil {
		return m // no brands directory → every brand uses the base default
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := brandFS.ReadFile("brands/" + e.Name())
		if err != nil {
			panic("guide: brand blueprint " + e.Name() + " unreadable: " + err.Error())
		}
		bp, err := Parse(raw)
		if err != nil {
			panic("guide: brand blueprint " + e.Name() + " invalid: " + err.Error())
		}
		m[strings.ToLower(strings.TrimSuffix(e.Name(), ".yaml"))] = bp
	}
	return m
}

// brandBlueprint returns the brand's embedded default blueprint, or (Blueprint{},
// false) when the brand ships none. Case-insensitive, whitespace-trimmed (deps.Brand
// is operator config).
func brandBlueprint(brand string) (Blueprint, bool) {
	bp, ok := brandBlueprints[strings.ToLower(strings.TrimSpace(brand))]
	return bp, ok
}

// fixtureBlueprint is the embedded fail-safe for a deployment's brand: the brand's own
// blueprint if it ships one, else the base default. It is the ultimate fallback when
// the DB carries no seeded blueprint (unreachable / pre-seed).
func fixtureBlueprint(brand string) Blueprint {
	if bp, ok := brandBlueprint(brand); ok {
		return bp
	}
	return defaultBlueprint
}

// brandCurriculum returns the brand's default journey as an engine Curriculum (the
// enabled projection of its blueprint), or (Curriculum{}, false) when the brand ships
// none — introspection for the resolution/link-guard tests.
func brandCurriculum(brand string) (Curriculum, bool) {
	bp, ok := brandBlueprint(brand)
	if !ok {
		return Curriculum{}, false
	}
	return bp.Curriculum(), true
}

// BrandCurriculums returns the sorted brand names that ship a default blueprint —
// introspection for a catalog / link-guard test.
func BrandCurriculums() []string {
	out := make([]string, 0, len(brandBlueprints))
	for b := range brandBlueprints {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}
