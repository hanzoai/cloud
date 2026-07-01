package cloud

import (
	"os"
	"testing"
)

func TestBrandFor(t *testing.T) {
	// hanzo → hanzo.id (NOT iam.hanzo.ai): brand.go was pinned to the real OIDC
	// issuer in fddaeb14 ("pin hanzo IAM issuer to hanzo.id") — iam.hanzo.ai is a
	// routing alias, and the live .well-known reports iss=https://hanzo.id, so a
	// token would fail the issuer check against iam.hanzo.ai. This stale
	// assertion predated that pin; aligned here (drive-by, brand.go unchanged).
	cases := map[string]string{
		"hanzo":    "https://hanzo.id",
		"lux":      "https://lux.id",
		"zoo":      "https://zoo.id",
		"pars":     "https://pars.id",
		"bootnode": "https://id.bootno.de",
		"LUX":      "https://lux.id",   // case-insensitive
		"  zoo  ":  "https://zoo.id",   // trimmed
		"unknown":  "https://hanzo.id", // falls back to hanzo
		"":         "https://hanzo.id", // empty → hanzo default
	}
	for brand, want := range cases {
		if got := IssuerForBrand(brand); got != want {
			t.Errorf("IssuerForBrand(%q) = %q, want %q", brand, got, want)
		}
	}
}

// TestLoadConfig_IssuerDerivedFromBrand asserts that when CLOUD_IAM_ISSUER is
// unset, the issuer is derived from CLOUD_BRAND — so a non-hanzo brand does not
// silently validate against iam.hanzo.ai.
func TestLoadConfig_IssuerDerivedFromBrand(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "lux")
	os.Unsetenv("CLOUD_IAM_ISSUER")
	cfg := LoadConfig()
	if cfg.IAMIssuer != "https://lux.id" {
		t.Fatalf("derived issuer = %q, want https://lux.id", cfg.IAMIssuer)
	}
	if cfg.Brand != "lux" {
		t.Fatalf("brand = %q, want lux", cfg.Brand)
	}
}
