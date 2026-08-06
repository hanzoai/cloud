package platform

// red_reserved_gap_test.go — red's PoC for the reservation gap the bare-<org>
// rename opened. Written to FAIL against the code as it stands: each test
// asserts the SECURE behaviour, so a GREEN run IS the fix.
//
// THE GAP. Dropping the `tenant-` prefix made `tenant-` an ORDINARY name — but
// the cluster's LIVE tenant fences are still spelled with it. universe
// infra/k8s/hanzo-cd/project-tenants.yaml declares AppProjects tenant-hanzo,
// tenant-lux, tenant-zoo, tenant-zen and tenant-maxpower, each admitting
// destination namespace `tenant-<org>`. reserved() covers the kube-, hanzo-,
// lux- and zoo- families but NOT the tenant- family, so an IAM org named
// `tenant-maxpower` resolves onto the real maxpower tenant's namespace and fence.
//
// The prefix that used to BE the fence is now a name a customer can claim.
//
// BLAST RADIUS IS CURRENTLY GATED, AND THE GATE SELF-CLEARS. checkFence refuses
// a main write while the live ApplicationSet still says hasPrefix "tenant-"
// (it does today), and no tenant-* values directory exists yet, so there are no
// rows to read. Both conditions end the moment universe lands the companion
// reservation rule — by design, with no further review. That is precisely why
// this must be fixed BEFORE the companion lands, not after.
//
// FIX DIRECTION: reserve the tenant- family structurally, alongside kube- and
// the brands — one line in reserved(). Retiring the legacy namespaces instead is
// also sound, but until they are gone the name must not be claimable.

import (
	"context"
	"testing"

	"github.com/hanzoai/namespace"
)

// legacyTenantOrgs are the orgs whose LIVE fence is still spelled `tenant-<org>`
// (universe project-tenants.yaml). Each is a real AppProject + namespace today.
var legacyTenantOrgs = []string{"hanzo", "lux", "zoo", "zen", "maxpower"}

// ── the predicate ────────────────────────────────────────────────────────────

// A directory naming a LIVE tenant fence belongs to the platform's namespace
// family, exactly as hanzo-* and kube-* do, and must be reserved.
func TestReservedCoversTheLegacyTenantFamily(t *testing.T) {
	for _, org := range legacyTenantOrgs {
		dir := "tenant-" + org
		if !reserved(dir) {
			t.Errorf("RESERVATION GAP: %q is a LIVE AppProject + namespace (universe project-tenants.yaml) but reserved(%q)=false — a customer org of that name resolves onto %s's own fence",
				dir, dir, org)
		}
	}
	// The family, not merely the five that exist today: the next tenant onboarded
	// under the legacy layout must not become claimable the moment it is created.
	if !reserved("tenant-anything") {
		t.Errorf("RESERVATION GAP: the tenant- family is not reserved structurally, so every legacy fence is claimable by name")
	}
}

// ── the read path: cross-org CD disclosure ───────────────────────────────────
//
// checkFence gates WRITES to main. It does not gate reads. owns() admits any
// non-reserved directory, so an org named `tenant-maxpower` reads every CD
// Application whose destination namespace is the real maxpower tenant's.
func TestOwnsRefusesTheLegacyTenantFamily(t *testing.T) {
	for _, org := range legacyTenantOrgs {
		ns := "tenant-" + org
		if owns(ns, ns) {
			t.Errorf("CROSS-ORG READ: an org named %q owns namespace %q — the live fence of org %q", ns, ns, org)
		}
	}
}

// End to end through the real confinement.
func TestCDConfinementRefusesALegacyTenantNamespace(t *testing.T) {
	const victimNS = "tenant-maxpower" // the live namespace + AppProject
	s := fakeCDService(cdAppObj(victimNS+"-billing", victimNS, victimNS))

	got, err := cdApps(s, context.Background(), orgAdmin("tenant-maxpower"))
	if err != nil {
		t.Fatalf("cdApps: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("CROSS-ORG READ: org %q read %d row(s) of the real maxpower tenant: %+v",
			"tenant-maxpower", len(got), got)
	}
}

// ── the write path: takeover of a live tenant namespace ──────────────────────
//
// resolveOrg is the ONE place a directory is admitted. It must refuse a legacy
// tenant fence to a non-SuperAdmin, exactly as it refuses `hanzo` or `kube-system`.
func TestResolveOrgRefusesTheLegacyTenantFamily(t *testing.T) {
	for _, org := range legacyTenantOrgs {
		claim := "tenant-" + org
		got, err := resolveOrg(claim, "", false) // a plain org admin of that org
		if err == nil {
			t.Errorf("NAMESPACE TAKEOVER: a non-super org admin of %q resolved to directory %q — the live namespace + AppProject of org %q",
				claim, got, org)
		}
	}
}

// A SuperAdmin may still name one (it is the platform's, like any reserved name).
func TestResolveOrgAdmitsALegacyTenantFamilyNameToSuperAdmin(t *testing.T) {
	if _, err := resolveOrg("admin", "tenant-maxpower", true); err != nil {
		t.Fatalf("a SuperAdmin must still be able to name a reserved directory: %v", err)
	}
}

// The control: an ordinary customer org is unaffected by the fix.
func TestOrdinaryOrgStillResolves(t *testing.T) {
	for _, org := range []string{"acme", "globex", "Acme", "maxpower-two"} {
		got, err := resolveOrg(org, "", false)
		if err != nil {
			t.Fatalf("ordinary org %q must still resolve: %v", org, err)
		}
		if got != namespace.Sanitize(org) {
			t.Fatalf("org %q resolved to %q, want %q", org, got, namespace.Sanitize(org))
		}
	}
}

// ── the two boards must ask the SAME predicate ───────────────────────────────

// cd.go owns() ends in `&& !reserved(ns)`; fleet.go scopeNamespaces has no such
// clause. So a non-super org admin of the reserved brand org `hanzo` is refused
// the delivery board and handed the platform's namespaces on the fleet board.
// One predicate, asked in one place and not the other, is two policies.
func TestBothBoardsAskReserved(t *testing.T) {
	all := []string{"hanzo", "hanzo-testnet", "hanzo-devnet", "acme"}
	if got := scopeNamespaces(all, orgAdmin("hanzo")); len(got) != 0 {
		t.Errorf("ASYMMETRY: the fleet board handed the org admin of reserved org %q the platform namespaces %v, while cd.go owns(\"hanzo\",\"hanzo\")=%v refuses the same caller",
			"hanzo", got, owns("hanzo", "hanzo"))
	}
}
