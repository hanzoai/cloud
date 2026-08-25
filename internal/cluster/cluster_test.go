package cluster

import "testing"

// Class is TOTAL — every input decided, never a panic — and it CONFINES: anything
// it does not recognise is classified out, so a reader can never reach beyond the
// tenants it names, and a tenant namespace authorizes to its OWN org rather than
// to the platform.
func TestClassIsTotalAndConfining(t *testing.T) {
	for _, tc := range []struct {
		ns, tenant, env string
		ok              bool
	}{
		{"hanzo", "hanzo", "main", true},
		{"hanzo-mainnet", "hanzo", "main", true},
		{"hanzo-testnet", "hanzo", "test", true},
		{"hanzo-devnet", "hanzo", "dev", true},
		{"tenant-maxpower", "maxpower", "main", true}, // authorizes to maxpower, NOT hanzo
		{"tenant-hanzo", "hanzo", "main", true},
		{"tenant-", "", "", false},     // empty tenant is not a tenant
		{"kube-system", "", "", false}, // never ours
		{"default", "", "", false},
		{"", "", "", false},
		{"hanzo-evil", "", "", false}, // a look-alike suffix is not a lifecycle env
	} {
		tenant, env, ok := Class(tc.ns)
		if tenant != tc.tenant || env != tc.env || ok != tc.ok {
			t.Errorf("Class(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.ns, tenant, env, ok, tc.tenant, tc.env, tc.ok)
		}
		if got := Tenant(tc.ns); got != tc.tenant {
			t.Errorf("Tenant(%q) = %q, want %q", tc.ns, got, tc.tenant)
		}
		if got := Env(tc.ns); got != tc.env {
			t.Errorf("Env(%q) = %q, want %q", tc.ns, got, tc.env)
		}
	}
}
