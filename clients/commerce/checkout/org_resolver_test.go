package checkout

import "testing"

// brandForHost is exact-suffix: a host maps to a brand only when it equals the
// brand domain or is a real subdomain of it. Cross-brand spoofs
// ("pay.lux.network.evil.com") must NOT inherit the spoofed brand — they fall
// through to the deployment default.
func TestBrandForHost(t *testing.T) {
	cases := []struct {
		host string
		slug string
	}{
		{"pay.hanzo.ai", "hanzo"},
		{"commerce-api.hanzo.ai", "hanzo"},
		{"hanzo.ai", "hanzo"},
		{"pay.lux.network", "lux"},
		{"lux.network", "lux"},
		{"pay.zoo.ngo", "zoo"},
		{"pay.pars.network", "pars"},
		// Unknown host → deployment default (hanzo).
		{"pay.example.test", "hanzo"},
		{"random.internal", "hanzo"},
		// Spoofs must NOT inherit the spoofed brand; they resolve to default.
		{"pay.lux.network.evil.com", "hanzo"},
		{"pay.zoo.ngo.attacker.test", "hanzo"},
		// Non-subdomain lookalikes must not hijack the brand.
		{"notlux.network", "hanzo"},
	}
	for _, tc := range cases {
		if got := brandForHost(tc.host).slug; got != tc.slug {
			t.Errorf("brandForHost(%q).slug = %q, want %q", tc.host, got, tc.slug)
		}
	}
}

// The resolver returns a usable tenant for any well-formed host — no separate
// tenant registry, no 404. The public Square config comes from the org via the
// env fallback (the cloud-org path), so the pay SPA can mount its card iframe.
func TestOrgResolver_ResolvesOrgAsTenant(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "production")
	t.Setenv("SQUARE_APPLICATION_ID", "sq0idp-TESTAPP")
	t.Setenv("SQUARE_LOCATION_ID", "TESTLOC")

	r := NewOrgResolver(nil) // nil loader → brand-default synthetic org + env

	ten, err := r.Resolve("pay.hanzo.ai")
	if err != nil {
		t.Fatalf("Resolve(pay.hanzo.ai) err = %v, want nil", err)
	}
	if ten.Name != "hanzo" {
		t.Errorf("tenant name = %q, want hanzo", ten.Name)
	}
	if ten.IAM.Issuer != "https://hanzo.id" || ten.IAM.ClientID != "hanzo-app" {
		t.Errorf("tenant IAM = %+v, want hanzo.id/hanzo-app", ten.IAM)
	}
	if ten.Square.ApplicationID != "sq0idp-TESTAPP" || ten.Square.LocationID != "TESTLOC" {
		t.Errorf("tenant Square = %+v, want env app/location", ten.Square)
	}
	if ten.Square.Environment != "production" {
		t.Errorf("tenant Square env = %q, want production", ten.Square.Environment)
	}
	// Square must be an enabled provider so the card method surfaces; Stripe never.
	var hasSquare, hasStripe bool
	for _, p := range ten.Providers {
		if p.Name == "square" && p.Enabled {
			hasSquare = true
		}
		if p.Name == "stripe" {
			hasStripe = true
		}
	}
	if !hasSquare {
		t.Errorf("tenant providers %+v missing enabled square", ten.Providers)
	}
	if hasStripe {
		t.Errorf("tenant providers %+v must not surface stripe", ten.Providers)
	}
}

// A malformed Host (empty / control bytes) is the only case that 404s.
func TestOrgResolver_MalformedHostRejected(t *testing.T) {
	r := NewOrgResolver(nil)
	for _, h := range []string{"", "\x00bad", " ", ":"} {
		if _, err := r.Resolve(h); err != ErrUnknownTenant {
			t.Errorf("Resolve(%q) err = %v, want ErrUnknownTenant", h, err)
		}
	}
}

// The public tenant JSON built from an org resolution must carry the Square
// block and enabled square provider, and never leak a secret.
func TestOrgResolver_PublicViewCarriesSquare(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "production")
	t.Setenv("SQUARE_APPLICATION_ID", "sq0idp-PUBVIEW")
	t.Setenv("SQUARE_LOCATION_ID", "LOCPUB")

	r := NewOrgResolver(nil)
	ten, err := r.Resolve("pay.hanzo.ai")
	if err != nil {
		t.Fatalf("Resolve err = %v", err)
	}
	pv := toPublicView(ten)
	if pv.Square.ApplicationID != "sq0idp-PUBVIEW" || pv.Square.Environment != "production" {
		t.Errorf("publicView.Square = %+v, want env app/prod", pv.Square)
	}
}

// The hanzo tenant's return allowlist must carry the first-party app hosts so
// pay can bounce ?return= back to the app that sent the user here (e.g. the
// playground onboarding flow). An empty allowlist would reject every return and
// strand the user on the brand default after onboarding.
func TestOrgResolver_ReturnAllowlistCarriesAppHosts(t *testing.T) {
	r := NewOrgResolver(nil)
	ten, err := r.Resolve("pay.hanzo.ai")
	if err != nil {
		t.Fatalf("Resolve err = %v", err)
	}
	const want = "https://playground.hanzo.bot"
	found := false
	for _, h := range ten.ReturnURLAllowlist {
		if h == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hanzo return allowlist %v missing %q", ten.ReturnURLAllowlist, want)
	}
	// It must survive the public projection as a non-nil, populated slice — a
	// nil slice serializes as JSON null and crashes the pay SPA (e is not
	// iterable) on /onboard.
	if pv := toPublicView(ten); len(pv.ReturnURLAllowlist) == 0 {
		t.Errorf("publicView return allowlist is empty, want first-party app hosts")
	}
}

// enabledProviders honors the deploy-wide disabled policy: Square is off only
// when explicitly disabled; crypto + wire are always present; Stripe never is.
func TestEnabledProviders_SquareOffWhenDisabled(t *testing.T) {
	t.Setenv("COMMERCE_DISABLED_PROCESSORS", "square")
	for _, p := range enabledProviders() {
		if p.Name == "square" {
			t.Errorf("square surfaced despite COMMERCE_DISABLED_PROCESSORS=square")
		}
	}
}
