package guide

import (
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestBrandCurriculumResolves: a brand that ships brands/<brand>.yaml (zoo) resolves
// to ITS OWN journey; an unknown brand (and the empty brand) fall back to the base
// default. Every embedded brand file is validated at init (mustBrands) — a build-time
// fault otherwise — so a resolved brand curriculum is always well-formed.
func TestBrandCurriculumResolves(t *testing.T) {
	zoo, ok := brandCurriculum("zoo")
	if !ok {
		t.Fatal("zoo must ship a brand curriculum (brands/zoo.yaml)")
	}
	if zoo.Version != "zoo-1" || len(zoo.Steps) == 0 {
		t.Fatalf("zoo curriculum mis-resolved: version=%q steps=%d", zoo.Version, len(zoo.Steps))
	}
	if err := Validate(zoo); err != nil {
		t.Fatalf("embedded brand curriculum must be valid: %v", err)
	}
	// The zoo journey reframes the base — its first quest is foundation, not company.
	if _, has := zoo.stepByID("foundation"); !has {
		t.Fatal("zoo journey must open with the foundation quest")
	}

	// Case-insensitive + whitespace-trimmed (deps.Brand is operator config).
	if _, ok := brandCurriculum("  ZOO "); !ok {
		t.Fatal("brand lookup must be case-insensitive and trimmed")
	}
	// Unknown brand and empty brand fall back (no brand curriculum).
	if _, ok := brandCurriculum("nope"); ok {
		t.Fatal("an unknown brand must NOT resolve a brand curriculum")
	}
	if _, ok := brandCurriculum(""); ok {
		t.Fatal("an empty brand must NOT resolve a brand curriculum")
	}
	if len(BrandCurriculums()) == 0 {
		t.Fatal("BrandCurriculums must list the shipped brand(s)")
	}
}

// TestMountUsesBrandDefault: mounting with deps.Brand="zoo" makes the zoo journey the
// effective default an org sees (overview), while a hanzo/empty brand uses the base
// default — the white-label tier of the resolution chain, wired at Mount.
func TestMountUsesBrandDefault(t *testing.T) {
	mountBrand := func(brand string) overviewView {
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Brand: brand}
		if err := Mount(app, deps); err != nil {
			t.Fatalf("Mount(brand=%q): %v", brand, err)
		}
		t.Cleanup(func() { _ = Shutdown() })
		// Only the store-backed detector so the read never hits a live warehouse.
		mounted.State.detectors = map[string]Detector{"acted": mounted.State.detectors["acted"]}
		return decode[overviewView](t, req(t, app, "GET", "/v1/guide", "acme", nil).Body)
	}

	zoo := mountBrand("zoo")
	if zoo.Version != "zoo-1" || zoo.Progress.Next != "foundation" {
		t.Fatalf("zoo brand must serve the zoo journey (version=%q next=%q)", zoo.Version, zoo.Progress.Next)
	}

	base := mountBrand("") // no brand → base default
	if base.Version != "builtin-2" || base.Progress.Next != "company" {
		t.Fatalf("empty brand must serve the base journey (version=%q next=%q)", base.Version, base.Progress.Next)
	}
}
