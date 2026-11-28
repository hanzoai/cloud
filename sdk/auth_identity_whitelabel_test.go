package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/brand"
)

// TestValidate_FailSecureOnEmptyTrustSet proves I2: a validator that trusts no provider REJECTS an otherwise-valid, correctly signed token — the axis
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

	// A validator that trusts no provider must DENY, not skip the check.
	vNone := &Boundary{claims: newIdentityCache(), keys: newIAMKeys()}
	if _, err := vNone.validate(tok); err == nil {
		t.Error("a validator with no provider must REJECT (fail-secure), not accept")
	}
}

// TestBrandIssuers proves the issuer list is derived from the brands registry and
// covers every configured brand (one source of truth).
func TestBrandIssuers(t *testing.T) {
	got := brand.Issuers()
	for _, want := range []string{"https://hanzo.id", "https://lux.id", "https://zoolabs.id", "https://pars.id", "https://id.bootno.de"} {
		found := slices.Contains(got, want)
		if !found {
			t.Errorf("BrandIssuers()=%v missing %q", got, want)
		}
	}
}

// TestNewBoundary_EveryBrandByItsOwnDiscovery proves one binary validates every brand's
// tokens, each by the issuer its own discovery document names, and nothing else.
func TestNewBoundary_EveryBrandByItsOwnDiscovery(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	var providers fakeProviders
	for _, u := range brand.Discoveries() {
		iss, err := issuerOf(u)
		if err != nil {
			t.Fatal(err)
		}
		providers = append(providers, fakeProvider{iss, jwks.URL})
	}
	v, err := newBoundary(brand.Discoveries(), &http.Client{Transport: providers}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(issuer string) string {
		c := tokenClaims("hanzo-console", "acme", "", false, time.Now().Add(time.Hour))
		c.Issuer = issuer
		return signWith(t, key, c)
	}
	for _, p := range providers {
		if _, err := v.validate(sign(p.issuer)); err != nil {
			t.Errorf("validator must trust the %s issuer: %v", p.issuer, err)
		}
		for _, sib := range brand.Siblings(p.issuer) {
			if _, err := v.validate(sign(sib)); err != nil {
				t.Errorf("validator must trust sibling AlsoIssuer %s of %s: %v", sib, p.issuer, err)
			}
		}
	}
	if _, err := v.validate(sign("https://evil.id")); err == nil {
		t.Error("validator must reject an untrusted issuer")
	}
}
