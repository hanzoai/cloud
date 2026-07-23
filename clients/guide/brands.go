package guide

import (
	"embed"
	"sort"
	"strings"
)

// brands.go is the WHITE-LABEL tier of the curriculum resolution chain. A brand
// (hanzo/zoo/lux) can ship its own default quest set as DATA — a brands/<brand>.yaml
// file — so a Zoo or Lux console runs ITS OWN journey, not Hanzo's. It is resolved
// once at Mount from deps.Brand and sits between the two other tiers:
//
//	org-custom (guide_curriculum row)  >  brand default (brands/<brand>.yaml)  >  base default (default.yaml)
//
// Adding a brand's quests is a new YAML file, never a code change — exactly the
// same shape as default.yaml (Step / Curriculum). Every embedded brand file is
// parsed AND validated at init: a malformed brand curriculum is a BUILD-TIME fault
// (panic at package init), mirroring the base default's mustDefault discipline —
// never a runtime surprise.

//go:embed brands
var brandFS embed.FS

// brandCurriculums maps a lowercased brand name to its default curriculum. Built
// once at init from the embedded brands/ directory.
var brandCurriculums = mustBrands()

func mustBrands() map[string]Curriculum {
	m := map[string]Curriculum{}
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
			panic("guide: brand curriculum " + e.Name() + " unreadable: " + err.Error())
		}
		cur, err := Parse(raw)
		if err != nil {
			panic("guide: brand curriculum " + e.Name() + " invalid: " + err.Error())
		}
		m[strings.ToLower(strings.TrimSuffix(e.Name(), ".yaml"))] = cur
	}
	return m
}

// brandCurriculum returns the brand's default curriculum, or (Curriculum{}, false)
// when the brand ships none — the caller then uses the built-in base default. The
// lookup is case-insensitive and whitespace-trimmed (deps.Brand is operator config).
func brandCurriculum(brand string) (Curriculum, bool) {
	cur, ok := brandCurriculums[strings.ToLower(strings.TrimSpace(brand))]
	return cur, ok
}

// BrandCurriculums returns the sorted brand names that ship a default curriculum —
// introspection for a catalog / link-guard test.
func BrandCurriculums() []string {
	out := make([]string, 0, len(brandCurriculums))
	for b := range brandCurriculums {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}
