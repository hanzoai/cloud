package forge

import "testing"

// A delivery from a namespace the estate created beside its tenants' names the
// estate's org; a tenant's canonical namespace still names the tenant; anything
// else is refused.
func TestOrgReadsTheEstatesOwnNamespaces(t *testing.T) {
	for owner, want := range map[string]string{"hanzoai": "hanzo", "hanzo": "hanzo", "hanzo-inc": "hanzo", "hanzozt": "hanzo", "HanzoZT": "hanzo"} {
		got, err := Org(owner)
		if err != nil || got != want {
			t.Fatalf("Org(%q) = %q, %v; want %q", owner, got, err, want)
		}
	}
	if _, err := Org("nobody-here"); err == nil {
		t.Fatal("an unmapped namespace resolved")
	}
	if o, err := Owner("hanzo"); err != nil || o != "hanzoai" {
		t.Fatalf("Owner(hanzo) = %q, %v; the write direction must stay the canonical namespace", o, err)
	}
}
