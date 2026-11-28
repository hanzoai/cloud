package brand

import "testing"

// Every host the deployment fronts resolves to ITS OWN brand's issuer. A map
// written by hand had this wrong for a brand nobody remembered to add.
func TestIssuerByHostCoversEveryBrand(t *testing.T) {
	m := IssuerByHost()
	for _, c := range []struct{ host, want string }{
		{"hanzo.ai", "https://hanzo.ai"},
		{"hanzo.id", "https://hanzo.ai"},
		{"lux.id", "https://lux.id"},
		{"zoolabs.id", "https://zoolabs.id"},
		{"id.zoo.network", "https://zoolabs.id"},
		{"pars.id", "https://pars.id"},
		{"id.bootno.de", "https://id.bootno.de"},
		{"lux.network", "https://lux.id"},
	} {
		if got := m[c.host]; got != c.want {
			t.Errorf("%s -> %q, want %q — its relying parties would reject its own tokens", c.host, got, c.want)
		}
	}
	// No host may resolve to another brand's issuer.
	for h, iss := range m {
		if id, ok := ForIssuer(iss); !ok {
			t.Errorf("%s maps to %q, which no brand claims", h, iss)
		} else if _, isOurs := ForHostOK(h); !isOurs && iss != IssuerFor(id) {
			t.Errorf("%s maps outside its own brand", h)
		}
	}
}

// During an issuer move a brand has more than one issuer: every one names the brand,
// none is minted under by default, and an organization's own issuer (one path segment
// under a platform origin) is never one.
func TestABrandsIssuersDuringAMove(t *testing.T) {
	for _, iss := range []string{"https://hanzo.id", "https://hanzo.ai", "https://hanzo.ai/"} {
		if id, ok := ForIssuer(iss); !ok || id != "hanzo" {
			t.Errorf("ForIssuer(%q) = %q, %v; want hanzo", iss, id, ok)
		}
	}
	for _, iss := range []string{"https://hanzo.ai/acme", "https://hanzo.ai/acme/", "https://hanzo.ai.evil.test", "http://hanzo.ai"} {
		if id, ok := ForIssuer(iss); ok {
			t.Errorf("ForIssuer(%q) = %q: an organization's issuer or a lookalike named a platform brand", iss, id)
		}
	}
	if got := Siblings("https://hanzo.id"); len(got) != 1 || got[0] != "https://hanzo.ai" {
		t.Errorf("Siblings(hanzo.id) = %v", got)
	}
	if got := Siblings("https://hanzo.ai"); len(got) != 1 || got[0] != "https://hanzo.id" {
		t.Errorf("Siblings(hanzo.ai) = %v", got)
	}
	if got := Siblings("https://lux.id"); len(got) != 0 {
		t.Errorf("a brand with one issuer has siblings: %v", got)
	}
	for iss, want := range map[string]bool{"https://hanzo.id": true, "https://hanzo.ai": true, "https://hanzo.ai/acme": false, "https://lux.id": false} {
		if got := Trusts("https://hanzo.id", iss); got != want {
			t.Errorf("Trusts(hanzo.id, %q) = %v, want %v", iss, got, want)
		}
	}
	if got := IssuerFor("hanzo"); got != "https://hanzo.ai" {
		t.Errorf("IssuerFor(hanzo) = %q, want https://hanzo.ai", got)
	}
	for host, iss := range IssuerByHost() {
		if iss == "https://hanzo.id" {
			t.Errorf("%s still mints under legacy hanzo.id issuer", host)
		}
	}
}
