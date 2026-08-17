package integrations

import (
	"strings"
	"testing"
)

// Purge has to be grantable by BOTH credential paths, and this test exists
// because it was grantable by neither and then by only one.
//
// The two lists are not a general mirror — the apikey set covers R2, KV, D1 and
// Workers AI, which the OAuth app is not registered for — so a bijection would be
// wrong. What must hold is narrower and specific: the capabilities the PLATFORM
// itself depends on cannot depend on which button a user pressed. Nothing
// downstream can tell an apikey token from an OAuth one; they seal to the same
// KMS coordinate on purpose. So a capability the edge needs must be in both
// spellings or a connection silently works for one user and not another.
//
// Caught in the act: Zone:Cache Purge was added to the apikey set and the OAuth
// consent URL still asked for dns_records:edit, zone:read and pages:edit — read
// off a real authorize URL, not inferred. A token minted through that flow would
// have connected cleanly and been refused on its first purge.
func TestPurgeIsGrantableByBothCredentialPaths(t *testing.T) {
	if !has(cloudflareScopes, "Zone:Cache Purge") {
		t.Error("the apikey path does not request cache purge; the edge cannot invalidate a release")
	}
	if !has(cloudflareOAuthScopes, "cache_purge:edit") {
		t.Error("the OAuth path does not request cache purge; a token minted there is refused on first purge")
	}
}

// The two paths must also agree about the access they DO share, or the same
// integration means different things depending on how it was connected.
func TestBothPathsRequestTheSharedDNSAndZoneAccess(t *testing.T) {
	for _, pair := range []struct{ apikey, oauth string }{
		{"Zone:DNS:Edit", "dns_records:edit"},
		{"Zone:Read", "zone:read"},
		{"Account:Cloudflare Pages:Edit", "pages:edit"},
		{"Zone:Cache Purge", "cache_purge:edit"},
	} {
		if has(cloudflareScopes, pair.apikey) != has(cloudflareOAuthScopes, pair.oauth) {
			t.Errorf("%q and %q disagree: one path grants it and the other does not", pair.apikey, pair.oauth)
		}
	}
}

func has(set []string, want string) bool {
	for _, s := range set {
		if strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}
