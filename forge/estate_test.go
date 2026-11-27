package forge

import "testing"

// A delivery from a namespace the estate created beside its tenants' names the
// estate's org; a tenant's canonical namespace still names the tenant; anything
// else is refused.
func TestOrgReadsTheEstatesOwnNamespaces(t *testing.T) {
	for owner, want := range map[string]string{
		"hanzoai": "hanzo", "hanzo": "hanzo", "hanzo-inc": "hanzo", "hanzozt": "hanzo", "HanzoZT": "hanzo",
		"charts": "hanzo", "hanzo-apps": "hanzo", "hanzo-community": "hanzo", "hanzo-dart": "hanzo", "hanzo-docs": "hanzo",
		"lux": "lux", "zoo": "zoo", "adnexus": "adnexus",
	} {
		got, err := Org(owner)
		if err != nil || got != want {
			t.Fatalf("Org(%q) = %q, %v; want %q", owner, got, err, want)
		}
	}
	// The GitHub org names are NOT forge namespaces: a delivery names the
	// namespace it landed in, and luxfi/zooai are not namespaces here.
	for _, owner := range []string{"nobody-here", "luxfi", "zooai"} {
		if _, err := Org(owner); err == nil {
			t.Fatalf("Org(%q) resolved; that namespace belongs to no org here", owner)
		}
	}
	if o, err := Owner("hanzo"); err != nil || o != "hanzoai" {
		t.Fatalf("Owner(hanzo) = %q, %v; the write direction must stay the canonical namespace", o, err)
	}
	for org, want := range map[string]string{"lux": "lux", "zoo": "zoo", "adnexus": "adnexus"} {
		if o, err := Owner(org); err != nil || o != want {
			t.Fatalf("Owner(%q) = %q, %v; want %q", org, o, err, want)
		}
	}
}
