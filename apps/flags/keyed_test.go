package flags

import "testing"

// A plan slug keys switches of its own only when no field's name is another's plus
// the start of the slug: "window-" and "hours-" would key another plan's switch of
// another field, so they are not keyed; every keyed slug's switches are its own.
func TestASlugKeysOnlyItsOwnSwitches(t *testing.T) {
	for _, slug := range []string{"window-x", "hours-x", "Window-x", "HOURS-pro"} {
		if Keyed(slug) {
			t.Errorf("%q is keyed, want refused", slug)
		}
	}
	slugs := []string{"x", "go", "pro-2x", "dev-5x", "max-10x", "ultra", "windows", "hours", "window", "team-premium"}
	seen := map[string]string{}
	for _, slug := range append(slugs, "window-x", "hours-x") {
		for _, f := range PlanFields {
			key := PlanKey(f, slug)
			if prev, ok := seen[key]; ok && Keyed(slug) {
				t.Errorf("%s keys %s, as %s does", f+"/"+slug, key, prev)
			}
			if Keyed(slug) {
				seen[key] = f + "/" + slug
			}
		}
	}
	for _, slug := range slugs {
		if !Keyed(slug) {
			t.Errorf("%q is refused, want keyed", slug)
		}
	}
	if PlanKey("premium", "window-x") != PlanKey("premium_window", "x") || PlanKey("ours_window", "hours-x") != PlanKey("ours_window_hours", "x") {
		t.Fatal("the collision Keyed refuses is not the one PlanKey makes")
	}
}
