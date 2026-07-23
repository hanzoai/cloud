package integrations

import "testing"

// TestSubdomainHost pins the shared multi-tenant SaaS host normalizer used by Zendesk
// and Re:amaze: bare label, full host, and URL all resolve to <label><suffix>; junk,
// a foreign apex, a suffix-smuggling host, and injection all reject — so a hostile
// accountId can never reshape the verify origin.
func TestSubdomainHost(t *testing.T) {
	ok := map[string]string{
		"acme":                     "acme.zendesk.com",
		"acme.zendesk.com":         "acme.zendesk.com",
		"https://acme.zendesk.com": "acme.zendesk.com",
		"acme.zendesk.com/agent":   "acme.zendesk.com",
		"ACME":                     "acme.zendesk.com",
	}
	for in, want := range ok {
		got, err := subdomainHost(in, ".zendesk.com", "zendesk")
		if err != nil || got != want {
			t.Errorf("subdomainHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ",
		"acme.example.com",          // foreign apex
		"acme.zendesk.com.evil.com", // suffix smuggling
		"acme.zendesk.com@evil.com", // credential/host trick
		"a b.zendesk.com",           // space / injection
		"acme.zendesk.com:8080",     // port trick
	} {
		if _, err := subdomainHost(in, ".zendesk.com", "zendesk"); err == nil {
			t.Errorf("subdomainHost(%q): want error", in)
		}
	}
	// The same normalizer serves Re:amaze on its own suffix.
	if got, err := subdomainHost("acme", ".reamaze.io", "reamaze"); err != nil || got != "acme.reamaze.io" {
		t.Errorf(`subdomainHost("acme", ".reamaze.io") = %q, %v; want "acme.reamaze.io"`, got, err)
	}
}
