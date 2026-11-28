package brand

import "testing"

func TestIssuerFor(t *testing.T) {
	// hanzo issues at hanzo.ai, the live discovery document's iss;
	// hanzo.id remains trusted as a sibling issuer.
	cases := map[string]string{
		"hanzo":    "https://hanzo.ai",
		"lux":      "https://lux.id",
		"zoo":      "https://zoolabs.id",
		"pars":     "https://pars.id",
		"bootnode": "https://id.bootno.de",
		"LUX":      "https://lux.id",     // case-insensitive
		"  zoo  ":  "https://zoolabs.id", // trimmed
		"unknown":  "https://hanzo.ai",   // falls back to hanzo
		"":         "https://hanzo.ai",   // empty → hanzo default
	}
	for id, want := range cases {
		if got := IssuerFor(id); got != want {
			t.Errorf("IssuerFor(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestForHost(t *testing.T) {
	match := map[string]string{
		"api.hanzo.ai":        "hanzo",
		"hanzo.ai":            "hanzo",
		"api.lux.network":     "lux",
		"lux.network":         "lux",
		"API.LUX.NETWORK":     "lux", // case-insensitive
		"api.lux.network:443": "lux", // port stripped
		"api.zoo.ngo":         "zoo",
		"chat.zoo.ngo":        "zoo",
		"api.pars.network":    "pars",
		// AltDomains: the real cloud console surfaces run on <brand>.cloud /
		// <brand>.network — a Host there must brand as Lux/Zoo, never Hanzo.
		"console.lux.cloud":   "lux",
		"console.zoo.cloud":   "zoo",
		"foo.zoo.network":     "zoo",
		"console.hanzo.cloud": "hanzo",
		"app.hanzo.app":       "hanzo",
	}
	for host, want := range match {
		if got, ok := ForHostOK(host); !ok || got != want {
			t.Errorf("ForHostOK(%q) = %q,%v; want %q,true", host, got, ok, want)
		}
		if got := ForHost(host); got != want {
			t.Errorf("ForHost(%q) = %q; want %q", host, got, want)
		}
	}
	// No brand domain matches → not ok, and ForHost defaults to hanzo. The
	// caller (skills) uses the not-ok signal to fall back to the DEPLOYMENT
	// brand instead of blindly emitting Hanzo on a non-hanzo pod.
	for _, host := range []string{"example.com", "localhost", "", "10.0.0.1"} {
		if _, ok := ForHostOK(host); ok {
			t.Errorf("ForHostOK(%q) matched a brand, want no match", host)
		}
		if got := ForHost(host); got != Default {
			t.Errorf("ForHost(%q) = %q, want %q", host, got, Default)
		}
	}
}

// TestLoadConfig_IssuerDerivedFromBrand asserts that when CLOUD_IAM_ISSUER is
// unset, the issuer is derived from CLOUD_BRAND — so a non-hanzo brand does not
// silently validate against iam.hanzo.ai.
