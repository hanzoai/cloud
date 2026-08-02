package cloud

import "testing"

// TestQualifyRefusesEveryCrossTenantShape pins the mint. Each refusal below is a
// shape that, admitted, would put two tenants in one key space.
func TestQualifyRefusesEveryCrossTenantShape(t *testing.T) {
	for _, tc := range []struct{ name, brand, org string }{
		{"no brand", "", "acme"},
		{"brand carries the separator", "hanzo/zoo", "acme"},
		{"no org", "hanzo", ""},
		{"org is whitespace", "hanzo", "   "},
		{"org is the anonymous lane", "hanzo", publicOrg},
		{"org carries the separator", "hanzo", "lux/acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Qualify(tc.brand, tc.org); err == nil {
				t.Fatalf("Qualify(%q,%q) minted %q; want a refusal", tc.brand, tc.org, got)
			}
		})
	}
}

// TestQualifyIsInjectiveAcrossBrands is the whole reason this door exists: the
// same org id under two brands must produce two keys.
func TestQualifyIsInjectiveAcrossBrands(t *testing.T) {
	a, err := Qualify("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Qualify("zoo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two brands minted one key %q", a)
	}
	if a.Org() != "acme" || b.Org() != "acme" {
		t.Fatalf("org half lost: %q / %q", a.Org(), b.Org())
	}
	if !Qualified("hanzo", a) || Qualified("zoo", a) {
		t.Fatalf("Qualified does not agree with Qualify for %q", a)
	}
}

// TestQualifiedRejectsAnythingQualifyWouldNotMint proves the validator is
// derived from the mint rather than a second, weaker opinion of the shape.
func TestQualifiedRejectsAnythingQualifyWouldNotMint(t *testing.T) {
	for _, k := range []Tenant{"acme", "hanzo/", "hanzo/lux/acme", "/acme", "hanzo/$public", "HANZO/acme"} {
		if Qualified("hanzo", k) {
			t.Errorf("Qualified accepted %q, which Qualify would never mint", k)
		}
	}
}
