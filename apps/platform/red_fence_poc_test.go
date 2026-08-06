package platform

// red_fence_poc_test.go — red's adversarial PoC for the org fence, kept.
//
// These were written to FAIL against the code as it stood: each asserts the
// SECURE behaviour, so a green run IS the fix. They exercise the REAL
// namespace.Sanitize, the REAL cdApps confinement and a REAL dynamic client.
//
// WHAT THEY FOUND (red F1, HIGH, confirmed end to end). The write path derives a
// directory with namespace.Sanitize(org); the read path confined with the RAW
// `owner` claim. Sanitize on ONE side of an authorization compare is a collision
// waiting to be named: for org "Acme" the directory is "acme-<hash>", so the raw
// claim never matched its OWN rows, and an org whose raw name IS the literal
// string "acme-<hash>" matched them instead. The slugger is public code, so that
// value is offline-computable by anyone.
//
// The fix canonicalises BOTH sides (cd.go owns, fleet.go scopeNamespaces).
// Sanitize is INJECTIVE, so that is collision-free and not merely symmetric.
//
// PORTED TO THE CURRENT NAMING. Red wrote these against `tenant-<org>`
// directories; an org is its name now, so the victim's directory is the slug
// itself. The ATTACK is untouched — it never depended on the prefix, only on the
// asymmetry — which is the point worth keeping: dropping the prefix neither
// caused this bug nor fixed it.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/k8s"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// cdAppObj builds a real-shaped Hanzo CD Application CR in hanzo-cd, tracking a
// workload whose DESTINATION namespace is destNS.
func cdAppObj(name, destNS, project string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.hanzo.ai/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": cdNamespace},
		"spec": map[string]any{
			"project":     project,
			"destination": map[string]any{"namespace": destNS},
			"source":      map[string]any{"path": "charts/app", "helm": map[string]any{"valueFiles": []any{"values/" + destNS + "/billing.yaml"}}},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced", "revision": "deadbeefcafefeed"},
			"health": map[string]any{"status": "Healthy"},
		},
	}}
}

func fakeCDService(objs ...runtime.Object) *cloud.Service[fleetState] {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.CDApplications: "ApplicationList",
	}, objs...)
	return &cloud.Service[fleetState]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: fleetState{dyn: dyn, scan: &nsCache{}},
	}
}

func orgAdmin(org string) fleetPrincipal {
	return fleetPrincipal{Authority: cloud.Authority{Validated: true, OrgAdmin: true}, org: org}
}

// ── the asymmetry, at the predicate level (no k8s) ───────────────────────────

// The confinement key and the raw owner claim must agree for EVERY org, not only
// for the ones whose names happen to already be clean slugs.
func TestFenceReadAsymmetry_SanitizeVsRaw(t *testing.T) {
	victimRaw := "Acme" // capital A ⇒ Sanitize is NOT the identity
	victimSlug := namespace.Sanitize(victimRaw)
	if victimSlug == victimRaw {
		t.Fatalf("precondition: %q must not be its own slug", victimRaw)
	}
	victimNS := victimSlug // an org is its name: the directory IS the slug

	// The victim must reach its own rows...
	if !owns(victimNS, victimRaw) {
		t.Fatalf("the victim %q cannot read its OWN namespace %q — the read confinement disagrees with the write path",
			victimRaw, victimNS)
	}
	// ...and the offline-computable slug must NOT be a key anyone else can hold.
	if owns(victimNS, victimSlug) {
		t.Fatalf("ORG FENCE BREACH: an org whose raw name is the literal %q reaches %q's namespace %q",
			victimSlug, victimRaw, victimNS)
	}
}

// ── the asymmetry, end to end, through the real cdApps confinement ───────────
func TestFenceReadAsymmetry_CrossTenantCDRead(t *testing.T) {
	victimRaw := "Acme"
	victimSlug := namespace.Sanitize(victimRaw)
	victimApp := victimSlug + "-billing" // the Application name the generator mints

	s := fakeCDService(cdAppObj(victimApp, victimSlug, victimSlug))

	// The attacker is an org-admin of an org it legitimately controls, named the
	// victim's sanitized slug (a clean, registrable DNS label).
	got, err := cdApps(s, context.Background(), orgAdmin(victimSlug))
	if err != nil {
		t.Fatalf("cdApps(attacker): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ORG FENCE BREACH: attacker org %q read %d row(s) of victim %q: %+v",
			victimSlug, len(got), victimRaw, got)
	}
}

// The mirror correctness proof: the victim itself, presenting its OWN raw owner
// claim, must reach its own reconciliation rows.
func TestFenceReadAsymmetry_VictimCannotReadOwnRows(t *testing.T) {
	victimRaw := "Acme"
	victimNS := namespace.Sanitize(victimRaw)
	s := fakeCDService(cdAppObj(victimNS+"-billing", victimNS, victimNS))

	got, err := cdApps(s, context.Background(), orgAdmin(victimRaw))
	if err != nil {
		t.Fatalf("cdApps(victim): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("victim org %q should see its own 1 row, saw %d — the read confinement disagrees with the write path",
			victimRaw, len(got))
	}
}

// A control: a clean-slug org is confined correctly, so the bug was specifically
// the Sanitize/raw disagreement and not the confinement's shape.
func TestFenceRead_CleanOrgIsConfined(t *testing.T) {
	s := fakeCDService(cdAppObj("acme-web", "acme", "acme"))
	got, err := cdApps(s, context.Background(), orgAdmin("globex"))
	if err != nil {
		t.Fatalf("cdApps: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("clean foreign org saw %d rows, want 0", len(got))
	}
	own, _ := cdApps(s, context.Background(), orgAdmin("acme"))
	if len(own) != 1 {
		t.Fatalf("clean owner org saw %d rows, want 1", len(own))
	}
}

// ── the confinement, over the whole shape of org names ───────────────────────

// cdApps had no tests at all. This is the table red asked for: every class of
// name namespace.Sanitize treats differently, each proving the SAME two things —
// an org reaches its own rows, and no other org reaches them.
func TestCDConfinementHoldsForEveryShapeOfOrgName(t *testing.T) {
	for _, raw := range []string{
		"acme",   // already a clean slug (identity)
		"Acme",   // case ⇒ suffixed
		"team.a", // punctuation ⇒ suffixed
		"a-really-long-organisation-name-that-exceeds-the-thirty-two-byte-label", // truncated + suffixed
		"acme-0123456789abcdef", // looks like the slugger's OWN output ⇒ re-suffixed
		"ACME-Ltd",              // case + punctuation
	} {
		t.Run(raw, func(t *testing.T) {
			ns := namespace.Sanitize(raw)
			if ns == "" {
				t.Fatalf("precondition: %q must resolve to a name", raw)
			}
			s := fakeCDService(cdAppObj(ns+"-billing", ns, ns))

			own, err := cdApps(s, context.Background(), orgAdmin(raw))
			if err != nil {
				t.Fatalf("cdApps(owner): %v", err)
			}
			if len(own) != 1 {
				t.Fatalf("org %q saw %d of its OWN rows, want 1", raw, len(own))
			}

			// Nobody else, including whoever holds the slug as a raw name.
			for _, other := range []string{ns, "globex", raw + "x", "admin"} {
				if other == raw {
					continue
				}
				got, err := cdApps(s, context.Background(), orgAdmin(other))
				if err != nil {
					t.Fatalf("cdApps(%q): %v", other, err)
				}
				if len(got) != 0 {
					t.Fatalf("ORG FENCE BREACH: %q read %d row(s) belonging to %q", other, len(got), raw)
				}
			}
		})
	}
}

// An unvalidated or org-less caller reads nothing, at every shape.
func TestCDConfinementRefusesACallerWithNoOrg(t *testing.T) {
	s := fakeCDService(cdAppObj("acme-web", "acme", "acme"))
	for _, p := range []fleetPrincipal{
		{Authority: cloud.Authority{Validated: true, OrgAdmin: true}, org: ""},
		{Authority: cloud.Authority{Validated: true, OrgAdmin: true}, org: "  "},
	} {
		got, err := cdApps(s, context.Background(), p)
		if err != nil {
			t.Fatalf("cdApps: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("a caller with no org read %d rows", len(got))
		}
	}
}

// A RESERVED namespace is never any customer's, however its org is spelled —
// the control that replaced the `tenant-` prefix. A SuperAdmin still sees them.
func TestCDConfinementNeverHandsOverAReservedNamespace(t *testing.T) {
	s := fakeCDService(
		cdAppObj("hanzo-papers", "hanzo", platformProject),
		cdAppObj("kube-system-thing", "kube-system", platformProject),
		cdAppObj("hanzo-cd-self", "hanzo-cd", platformProject),
	)
	for _, org := range []string{"hanzo", "kube-system", "hanzo-cd", "admin", "acme"} {
		got, err := cdApps(s, context.Background(), orgAdmin(org))
		if err != nil {
			t.Fatalf("cdApps(%q): %v", org, err)
		}
		if len(got) != 0 {
			t.Fatalf("org admin %q read %d reserved row(s): %+v", org, len(got), got)
		}
	}
	super := fleetPrincipal{Authority: cloud.Authority{Validated: true, Super: true}, org: "admin"}
	all, err := cdApps(s, context.Background(), super)
	if err != nil {
		t.Fatalf("cdApps(super): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("a SuperAdmin saw %d of 3 rows", len(all))
	}
}

// ── the same asymmetry on the live fleet board (red F1, second site) ────────

// scopeNamespaces carried the identical compare and is LIVE on /v1/platform/fleet.
//
// It is exercised in the layout fleet.go's nsClass actually decodes — the
// `tenant-<slug>` namespaces that exist on the cluster today — because the bug
// and its fix are about the COMPARE, not the layout. Teaching the fleet board
// the bare-org layout this package now writes is the convergence follow-up; it
// is deliberately not smuggled in behind a security fix.
func TestFleetScopeCanonicalisesBothSides(t *testing.T) {
	raw := "Acme"
	slug := namespace.Sanitize(raw)
	ns := "tenant-" + slug
	all := []string{ns, "tenant-globex", "hanzo", "hanzo-testnet"}

	own := scopeNamespaces(all, orgAdmin(raw))
	if len(own) != 1 || own[0] != ns {
		t.Fatalf("org %q was scoped to %v, want [%s] — it cannot see its own namespace", raw, own, ns)
	}
	// The offline-computable slug must not be a key anyone else can hold.
	if got := scopeNamespaces(all, orgAdmin(slug)); len(got) != 0 {
		t.Fatalf("FLEET FENCE BREACH: an org whose raw name is the literal slug %q was scoped to %v", slug, got)
	}
	if got := scopeNamespaces(all, orgAdmin("")); len(got) != 0 {
		t.Fatalf("a caller with no org was scoped to %v", got)
	}
	// And a clean org still reaches its own, unchanged.
	if got := scopeNamespaces(all, orgAdmin("globex")); len(got) != 1 || got[0] != "tenant-globex" {
		t.Fatalf("a clean org was scoped to %v", got)
	}
}
