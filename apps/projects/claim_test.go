package projects

import (
	"context"
	"errors"
	"testing"
)

// claim_test.go pins the site custom-domain ownership boundary at the STORE
// level: a pending claim holds its name but must never resolve, verification is
// owner-scoped, and no path walks a live host backwards into pending.

// TestPendingClaimHoldsTheNameButNeverResolves is the hijack boundary. If this
// test ever goes green in the other direction, claiming yourco.com is enough to
// serve it.
func TestPendingClaimHoldsTheNameButNeverResolves(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-a", 100); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Held: nobody else may take it.
	if err := s.CreateProject(ctx, mkProject("acme", "acme", "Acme")); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "acme", "acme", "tok-b", 110); !errors.Is(err, errHostTaken) {
		t.Fatalf("cross-org claim = %v, want errHostTaken", err)
	}
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 110); !errors.Is(err, errHostTaken) {
		t.Fatalf("cross-org bind over a pending claim = %v, want errHostTaken", err)
	}

	// ...but NOT served.
	if _, err := s.ResolveHost(ctx, "yadota.tech"); !errors.Is(err, errNotFound) {
		t.Fatalf("ResolveHost on a pending claim = %v, want errNotFound — a pending claim must never route", err)
	}
	// ...and not reported as a domain the site serves.
	hosts, err := s.ListHostsForProject(ctx, "yadota", "yadota")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, h := range hosts {
		if h == "yadota.tech" {
			t.Fatal("ListHostsForProject reported a pending claim as a served domain")
		}
	}
	// ...though its owner can still see it as a claim.
	claims, err := s.ListHostClaims(ctx, "yadota", "yadota")
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	if len(claims) != 1 || claims[0].Host != "yadota.tech" || claims[0].Status != HostPending {
		t.Fatalf("ListHostClaims = %+v, want one pending yadota.tech", claims)
	}
	if claims[0].Token != "tok-a" {
		t.Errorf("token = %q, want the challenge token", claims[0].Token)
	}
}

func TestVerifyPromotesAndRoutes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-a", 100); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.VerifyHost(ctx, "yadota.tech", "yadota", "yadota", 200); err != nil {
		t.Fatalf("verify: %v", err)
	}
	p, err := s.ResolveHost(ctx, "yadota.tech")
	if err != nil || p.Org != "yadota" {
		t.Fatalf("ResolveHost after verify = (%v, %v), want the yadota project", p.Org, err)
	}
	claim, err := s.HostClaimFor(ctx, "yadota.tech", "yadota", "yadota")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claim.Status != HostVerified || claim.VerifiedAt != 200 {
		t.Errorf("claim = %+v, want verified at 200", claim)
	}
	// The proof has been consumed; the token is not kept around.
	if claim.Token != "" {
		t.Errorf("token = %q, want cleared once verified", claim.Token)
	}
}

// TestVerifyIsOwnerScoped: proving a host verifies only the claiming project's
// row. Another org must never be able to promote a name it does not hold.
func TestVerifyIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, o := range []string{"yadota", "acme"} {
		if err := s.CreateProject(ctx, mkProject(o, o, o)); err != nil {
			t.Fatalf("create %s: %v", o, err)
		}
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-a", 100); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.VerifyHost(ctx, "yadota.tech", "acme", "acme", 200); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-org verify = %v, want errNotFound", err)
	}
	if _, err := s.ResolveHost(ctx, "yadota.tech"); !errors.Is(err, errNotFound) {
		t.Fatal("a cross-org verify promoted the claim — it must not route")
	}
	// Existence is never confirmed across a tenant boundary.
	if _, err := s.HostClaimFor(ctx, "yadota.tech", "acme", "acme"); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-org HostClaimFor = %v, want errNotFound", err)
	}
}

// TestReclaimKeepsTheTokenAndNeverDowngrades: a repeat claim must not invalidate
// a record the customer already published, and must never take a live host down.
func TestReclaimKeepsTheTokenAndNeverDowngrades(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-a", 100); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-b", 110); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	claim, _ := s.HostClaimFor(ctx, "yadota.tech", "yadota", "yadota")
	if claim.Token != "tok-a" {
		t.Errorf("token = %q, want the original tok-a — a re-claim must not invalidate a published record", claim.Token)
	}

	// Now verified. A later re-claim must NOT walk it back to pending.
	if err := s.VerifyHost(ctx, "yadota.tech", "yadota", "yadota", 200); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-c", 300); err != nil {
		t.Fatalf("re-claim after verify: %v", err)
	}
	claim, _ = s.HostClaimFor(ctx, "yadota.tech", "yadota", "yadota")
	if claim.Status != HostVerified {
		t.Fatalf("status = %q after re-claim, want it to stay verified — a live host must not be taken off the air", claim.Status)
	}
	if _, err := s.ResolveHost(ctx, "yadota.tech"); err != nil {
		t.Fatalf("ResolveHost after re-claim = %v, want it still serving", err)
	}
}

// TestBindPromotesOwnPendingClaim: the operator/admin path binding a host this
// project already claimed should make it live, not conflict with itself.
func TestBindPromotesOwnPendingClaim(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.ClaimHost(ctx, "yadota.tech", "yadota", "yadota", "tok-a", 100); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.BindHost(ctx, "yadota.tech", "yadota", "yadota", 200); err != nil {
		t.Fatalf("bind own pending claim: %v", err)
	}
	if _, err := s.ResolveHost(ctx, "yadota.tech"); err != nil {
		t.Fatalf("ResolveHost after operator bind = %v, want serving", err)
	}
	claim, _ := s.HostClaimFor(ctx, "yadota.tech", "yadota", "yadota")
	if claim.Status != HostVerified || claim.Token != "" {
		t.Errorf("claim = %+v, want verified with the token cleared", claim)
	}
}

// TestExistingRowsStayVerified: the additive migration defaults status to
// 'verified', because every row that predates it is already serving. A default
// of 'pending' would take every live site off the air on upgrade.
func TestExistingRowsStayVerified(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Insert the way a pre-migration binary did: no status/token columns named.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO site_hosts (host, org, slug, created_at, updated_at) VALUES (?,?,?,?,?)`,
		"legacy.example", "yadota", "yadota", 50, 50); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	if _, err := s.ResolveHost(ctx, "legacy.example"); err != nil {
		t.Fatalf("ResolveHost on a pre-migration row = %v, want it still serving", err)
	}
}
