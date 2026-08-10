package brand

import "testing"

// Every host the deployment fronts resolves to ITS OWN brand's issuer. A map
// written by hand had this wrong for a brand nobody remembered to add.
func TestIssuerByHostCoversEveryBrand(t *testing.T) {
	m := IssuerByHost()
	for _, c := range []struct{ host, want string }{
		{"hanzo.id", "https://hanzo.id"},
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
