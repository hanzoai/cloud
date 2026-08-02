package risk

// tenant_test.go — the tenant key, which is the whole product.
//
// The key is the store index for every feature row, the model index, the
// aggregate key, and the identity a snapshot is only restorable under. Keyed on
// the bare organisation, `acme` on one issuer and `acme` on another become one
// set of rows and one model. This file holds the mint to that.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestTenantKey_NeverRegresses is the whole boundary in one table.
func TestTenantKey_NeverRegresses(t *testing.T) {
	// A bare organisation is not a tenant.
	if tenant(orgA).qualified() {
		t.Fatalf("the bare org %q passes the shape check", orgA)
	}
	// Two brands, one organisation name: two tenants.
	ha, za := key(t, brandA, orgA), key(t, brandB, orgA)
	if ha == za {
		t.Fatalf("%q on two issuers produced one key", orgA)
	}
	// The key reads back to the organisation it names, in both halves.
	if ha.brandOf() != brandA || ha.org() != orgA {
		t.Fatalf("%q reads back as (%q, %q)", string(ha), ha.brandOf(), ha.org())
	}
	// An unregistered brand vouches for nobody.
	if _, err := qualify("nosuchbrand", orgA); err == nil {
		t.Fatal("an unregistered brand minted a tenant")
	}
	// Neither half may be empty, and the org may not carry the separator — a key
	// that does not read back is a key an operator cannot attribute.
	for _, tc := range []struct{ brandID, org string }{
		{"", orgA}, {brandA, ""}, {brandA, "   "}, {brandA, "a/b"}, {brandA, public},
	} {
		if k, err := qualify(tc.brandID, tc.org); err == nil {
			t.Errorf("qualify(%q, %q) minted %q", tc.brandID, tc.org, string(k))
		}
	}
	// qualified() is DERIVED from qualify, so a key the mint would not produce is
	// refused by the shape check too. Anything else is two definitions free to
	// disagree.
	for _, bad := range []string{"", orgA, sep + orgA, brandA + sep, "nosuchbrand" + sep + orgA, brandA + sep + orgA + sep + "x"} {
		if tenant(bad).qualified() {
			t.Errorf("%q passes qualified() but the mint would refuse it", bad)
		}
	}
	if !ha.qualified() || !za.qualified() {
		t.Fatal("a minted key fails its own shape check")
	}
}

// TestTenantOf_FailsClosedWithoutAValidatedPrincipal: no principal, no tenant,
// and the refusal is a 403 rather than a fallback to anything.
//
// The POSITIVE half — that the organisation comes from the validated principal
// and the brand from the deployment, so neither is caller-supplied — is asserted
// over a real request in typed_wire_test.go, because that is where a validated
// principal actually exists.
func TestTenantOf_FailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	_, err := tenantOf(context.Background(), brandA)
	if err == nil {
		t.Fatal("a context with no validated principal resolved a tenant")
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("the refusal is %v, want a 403", err)
	}
}

// TestObservation_SubjectIsNamespacedByKind: a person and an account that happen
// to share an identifier are two subjects, not one aggregate. Folding them would
// pool two behaviours into one baseline and make each look ordinary.
func TestObservation_SubjectIsNamespacedByKind(t *testing.T) {
	k := key(t, brandA, orgA)
	a := ob(t, "e_a", kindPerson, "x", 0, time.Now()).tx(k)
	b := ob(t, "e_b", kindAccount, "x", 0, time.Now()).tx(k)
	if a.AccountID == b.AccountID {
		t.Fatalf("two kinds of subject %q folded onto one aggregate key %q", "x", a.AccountID)
	}
	if !strings.HasPrefix(a.AccountID, kindPerson+":") {
		t.Fatalf("the aggregate key %q does not name its kind", a.AccountID)
	}
	// And the transaction carries the QUALIFIED key as its organisation, which is
	// what the model, the rings and the snapshot are all indexed on.
	if a.OrgID != string(k) {
		t.Fatalf("the transaction carries %q, want the qualified key %q", a.OrgID, string(k))
	}
}
