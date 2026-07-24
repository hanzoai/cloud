package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"os"
	"testing"
	"time"
)

// TestValidate_FailSecureOnEmptyTrustSet proves I2: a validator whose resolved
// issuer set is empty REJECTS an otherwise-valid, correctly signed token — the axis
// is never silently disabled. Production always resolves a non-empty set; this
// guards the misconfiguration path (an empty issuer set), which must fail closed,
// not open.
func TestValidate_FailSecureOnEmptyTrustSet(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	future := time.Now().Add(time.Hour)
	tok := signWith(t, key, tokenClaims("hanzo-console", "acme", "", false, future))

	// Sanity: a properly configured validator accepts the token.
	if _, err := newIdentityValidator(testIssuer, jwks.URL, 0).validate(tok); err != nil {
		t.Fatalf("baseline valid token must be accepted, got %v", err)
	}

	// Empty issuer set → deny (construct directly; trustedIssuers never yields empty
	// with a primary, so bypass it to exercise the guard).
	vEmptyIss := &identityValidator{issuers: nil, cache: newJWKSCache(jwks.URL, 0), keys: newIAMKeys()}
	if _, err := vEmptyIss.validate(tok); err == nil {
		t.Error("empty issuer set must REJECT (fail-secure), not accept")
	}
}

// TestTrustedIssuers_WhiteLabel proves the in-binary validator's trusted-issuer
// set is the primary issuer UNIONED with every white-label brand issuer plus the
// WHITELABEL_ISSUERS override, deduped, primary-first.
func TestTrustedIssuers_WhiteLabel(t *testing.T) {
	os.Unsetenv("WHITELABEL_ISSUERS")
	got := trustedIssuers("https://hanzo.id")
	want := map[string]bool{
		"https://hanzo.id":     true,
		"https://lux.id":       true,
		"https://zoo.id":       true, // per cloud brand.go registry
		"https://pars.id":      true,
		"https://id.bootno.de": true, // bootnode brand also in the registry
	}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for w := range want {
		if !set[w] {
			t.Errorf("trusted set %v missing %q", got, w)
		}
	}
	if got[0] != "https://hanzo.id" {
		t.Errorf("primary issuer must be first, got %q", got[0])
	}

	// Override adds a brand without a rebuild.
	t.Setenv("WHITELABEL_ISSUERS", "https://custom.id, https://another.id")
	got2 := trustedIssuers("https://hanzo.id")
	if !issuerAllowed("https://custom.id", got2) || !issuerAllowed("https://another.id", got2) {
		t.Errorf("WHITELABEL_ISSUERS override must add issuers, got %v", got2)
	}
}

// TestIssuerAllowed proves the set membership check: brand issuers pass, an
// outsider is rejected, and an empty set is fail-secure — it matches NOTHING (I2),
// so a misconfiguration that empties the allowlist denies every token.
func TestIssuerAllowed(t *testing.T) {
	set := []string{"https://hanzo.id", "https://lux.id"}
	if !issuerAllowed("https://lux.id", set) {
		t.Error("lux.id must be allowed")
	}
	if issuerAllowed("https://attacker.id", set) {
		t.Error("attacker.id must be rejected")
	}
	if issuerAllowed("anything", nil) {
		t.Error("empty set must DENY (fail-secure), never skip the check")
	}
}

// TestBrandIssuers proves the issuer list is derived from the brands registry and
// covers every configured brand (one source of truth).
func TestBrandIssuers(t *testing.T) {
	got := BrandIssuers()
	for _, want := range []string{"https://hanzo.id", "https://lux.id", "https://zoo.id", "https://pars.id", "https://id.bootno.de"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("BrandIssuers()=%v missing %q", got, want)
		}
	}
}

// TestNewIdentityValidator_MultiIssuer proves the constructed validator carries the
// full brand set, so a lux token would pass the issuer gate on the hanzo binary.
func TestNewIdentityValidator_MultiIssuer(t *testing.T) {
	os.Unsetenv("WHITELABEL_ISSUERS")
	v := newIdentityValidator("https://hanzo.id", "http://iam.hanzo.svc/v1/iam/.well-known/jwks", 0)
	if !issuerAllowed("https://lux.id", v.issuers) {
		t.Fatalf("validator must trust the lux issuer, set=%v", v.issuers)
	}
	if !issuerAllowed("https://hanzo.id", v.issuers) {
		t.Fatalf("validator must still trust hanzo (no regression), set=%v", v.issuers)
	}
	if issuerAllowed("https://evil.id", v.issuers) {
		t.Fatalf("validator must reject an untrusted issuer, set=%v", v.issuers)
	}
}
