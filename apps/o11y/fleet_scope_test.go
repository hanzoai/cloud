package o11y

// fleet_scope_test.go — the per-product o11y surface must cover the FLEET, not a
// hand-kept subset of it.
//
// Before this gate, knownServices was the only way into resolveService, and it
// listed 26 k8s workloads. manifest.Apps lists 119 routed apps. The overlap was
// TWELVE. The other 107 — ai, admin, base, platform, projects, team, usage,
// tasks, deploy, exec, index, … — answered honest-empty on
// /v1/o11y/status, /v1/o11y/product/metrics and the scoped log read, not because
// their telemetry was missing (their request spans have been in event.span the
// whole time, stamped by cloud's TracingMiddleware) but because this package had
// never heard of them.
//
// The fix was to DERIVE the set instead of keeping a second one. These tests are
// what stop it from silently becoming a hand list again.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

// TestEveryRoutedAppHasAProductScope is the fleet direction: an app the host
// routes must be an app the o11y surface can scope to.
func TestEveryRoutedAppHasAProductScope(t *testing.T) {
	// Guard the iteration source before iterating it. Every way this could examine
	// zero apps — a manifest that failed to load, a package that stopped exporting
	// Apps — is a defect elsewhere that would otherwise arrive here as a green
	// tick. A floor rather than a zero-check, because a reader that degrades to
	// finding 3 of 119 is the same bug wearing a smaller number.
	if len(manifest.Apps) < 100 {
		t.Fatalf("manifest.Apps has %d rows — the fleet is 119 apps, so this gate is reading the "+
			"wrong table and would pass by not looking", len(manifest.Apps))
	}

	scoped, coresident, fallback := 0, 0, 0
	for _, a := range manifest.Apps {
		svc, ok := resolveService(a.Name)

		// The router's FALLBACK claims the API root, so it bounds nothing. Like a
		// co-resident app it has no product scope, for the mirror-image reason.
		if claimsAPIRoot(a) {
			if ok && len(svc.Routes) > 0 {
				t.Errorf("%s claims the API root %q and still resolved to routes %v — that scope is "+
					"every request in the fleet", a.Name, apiRoot, svc.Routes)
			}
			fallback++
			continue
		}

		if a.Coresident {
			// A co-resident app ROUTES nothing of its own (zen is a Claim middleware
			// on ai's router). There is no route subtree to attribute spans to, so
			// there is no honest product scope — and inventing `/v1/zen` would
			// silently report zero for a service that is genuinely serving.
			if ok && len(svc.Routes) > 0 && !knownServices[a.Name] {
				t.Errorf("%s is co-resident but resolved to routes %v — it routes no prefix of its "+
					"own, so those spans belong to the app it wraps", a.Name, svc.Routes)
			}
			coresident++
			continue
		}

		if !ok {
			t.Errorf("routed app %q does not resolve to a product scope — its status, metrics and "+
				"logs pages answer honest-empty while its spans sit in event.span", a.Name)
			continue
		}
		if len(svc.Routes) == 0 {
			t.Errorf("routed app %q resolved with NO routes — the RED query would scope to nothing "+
				"and report a healthy idle service", a.Name)
			continue
		}
		// The routes must be the MANIFEST's answer, not a convention that happens to
		// agree with it for most rows.
		want := manifest.PrefixesFor(a.Name)
		if len(want) > 0 {
			if len(svc.Routes) != len(want) {
				t.Errorf("%s: routes = %v, manifest says %v", a.Name, svc.Routes, want)
				continue
			}
			for i := range want {
				if svc.Routes[i] != want[i] {
					t.Errorf("%s: routes = %v, manifest says %v", a.Name, svc.Routes, want)
					break
				}
			}
		}
		scoped++
	}

	if scoped == 0 {
		t.Fatal("no routed app resolved to a product scope — this gate proved nothing")
	}
	// The two exemptions are exemptions, not a growing list. If either stops
	// existing the rule that justifies it has moved and must be re-argued.
	if fallback != 1 {
		t.Errorf("%d apps claim the API root, want exactly 1 (ai) — a second fallback means two apps "+
			"are being handed the same unclaimed traffic", fallback)
	}
	if coresident != 1 {
		t.Errorf("%d co-resident apps, want exactly 1 (zen)", coresident)
	}
	t.Logf("%d routed apps scoped, %d co-resident, %d API-root fallback, %d manifest rows",
		scoped, coresident, fallback, len(manifest.Apps))
}

// claimsAPIRoot reports whether an app declares the prefix every other app is
// under — the router's fallback role.
func claimsAPIRoot(a manifest.App) bool {
	for _, p := range a.Prefixes {
		if strings.TrimSuffix(p, "/") == apiRoot {
			return true
		}
	}
	return false
}

// TestDerivedRoutesBeatTheSlashV1NameConvention pins the REASON the routes come
// from the manifest rather than from string concatenation.
//
// For ~20 apps `/v1/<name>` is not a path anyone serves: plan serves /v1/plan,
// storage serves /v1/s3/buckets, account serves /v1/orgs, knowledge serves
// /v1/knowledge/*. Scoping their RED series to `/v1/<name>` matches no span at all and
// renders as a healthy service with no traffic — the worst kind of wrong, because
// it looks like an answer.
//
// If this test ever finds ZERO such apps it must fail, not pass: that would mean
// either the manifest changed shape or this test stopped reading it, and in both
// cases the claim it is defending has gone unverified.
func TestDerivedRoutesBeatTheSlashV1NameConvention(t *testing.T) {
	var differ []string
	for _, a := range manifest.Apps {
		if a.Coresident {
			continue
		}
		svc, ok := resolveService(a.Name)
		if !ok || len(svc.Routes) == 0 {
			continue
		}
		convention := "/v1/" + a.Name
		exact := len(svc.Routes) == 1 && svc.Routes[0] == convention
		if !exact {
			differ = append(differ, a.Name)
		}
	}
	if len(differ) == 0 {
		t.Fatal("every app's routes equal \"/v1/\"+name — either the manifest is no longer being " +
			"read (this gate is asserting nothing) or the derivation has been replaced by the " +
			"convention it exists to correct")
	}
	t.Logf("%d apps whose real prefixes differ from \"/v1/\"+name: %v", len(differ), differ)
}

// TestTheFallbackDoesNotSwallowTheFleet is the attribution rule.
//
// ai declares `/v1` — the catch-all it serves the OpenAI-compatible surface from,
// so that zen's c.Next() has somewhere to fall through to. A bare
// startsWith('/v1') scan therefore hands ai every request in the fleet. Over six
// hours of live spans /v1/kms/* alone was 161,705 of 434,765, so the metrics page
// would have reported KMS's traffic as inference.
//
// manifest.OwnerOf already warns that "anything deciding policy from a bare
// HasPrefix scan will attribute those paths to the wrong app". A RED query decides
// policy. ai therefore gets NO route scope at all — the honest answer, and the one
// that does not cost 15.5s to compute.
func TestTheFallbackDoesNotSwallowTheFleet(t *testing.T) {
	// The premise: ai really does claim the API root. If the manifest stops saying
	// that, this gate is no longer testing anything and must say so rather than pass.
	if got := manifest.PrefixesFor("ai"); len(got) == 0 {
		t.Fatal(`manifest has no prefixes for "ai" — this gate cannot check the case it exists for`)
	} else {
		root := false
		for _, p := range got {
			if strings.TrimSuffix(p, "/") == apiRoot {
				root = true
			}
		}
		if !root {
			t.Fatalf(`ai no longer claims %q (prefixes=%v) — the swallow case this gate defends `+
				`against has moved, so it is asserting nothing`, apiRoot, got)
		}
	}
	if svc, ok := resolveService("ai"); ok && len(svc.Routes) > 0 {
		t.Errorf("ai resolved to routes %v — every nested app's spans would count as inference", svc.Routes)
	}

	// A BOUNDED ancestor is different and must still exclude its children: admin
	// owns /v1/admin and seven other apps live under it.
	admin, ok := resolveService("admin")
	if !ok {
		t.Fatal(`resolveService("admin") refused`)
	}
	if len(admin.Excludes) == 0 {
		t.Error("admin owns /v1/admin and excludes NOTHING — apps nested under it count as admin traffic")
	}
	// A leaf app excludes nothing, and must not: over-excluding hides its own spans.
	kms, ok := resolveService("kms")
	if !ok {
		t.Fatal(`resolveService("kms") refused`)
	}
	if len(kms.Excludes) != 0 {
		t.Errorf("kms excludes %v — nothing nests inside /v1/kms", kms.Excludes)
	}
	t.Logf("ai: no route scope (fallback); admin excludes %d nested prefixes; kms excludes 0",
		len(admin.Excludes))
}

// TestDerivationLosesNoPreviouslyServedProduct is the other direction. Deriving
// the set must ADD apps, never drop a slug the console already asks for — the 26
// verified workloads and the 7 console aliases were all serving before.
func TestDerivationLosesNoPreviouslyServedProduct(t *testing.T) {
	if len(knownServices) == 0 || len(productAlias) == 0 {
		t.Fatal("knownServices or productAlias is empty — this gate has nothing to compare against")
	}
	checked := 0
	for name := range knownServices {
		if _, ok := resolveService(name); !ok {
			t.Errorf("workload %q resolved before and does not now — the derivation dropped a "+
				"product the console already asks for", name)
		}
		checked++
	}
	for slug := range productAlias {
		if _, ok := resolveService(slug); !ok {
			t.Errorf("console alias %q no longer resolves", slug)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("compared nothing")
	}
	t.Logf("%d previously-served products still resolve", checked)
}

// TestProductScopeStillRefusesWhatItAlwaysRefused — deriving the ALLOWLIST must
// not weaken the SHAPE gate. The product param is still placed into a PromQL
// label, a bound datastore parameter and a hostname, and validProduct is still
// the one thing standing in front of that.
func TestProductScopeStillRefusesWhatItAlwaysRefused(t *testing.T) {
	bad := []string{
		"", " ", "-leading", "trailing-", "UPPER", "under_score", "dot.dot",
		`"} or up{`, "../etc/passwd", "a/b", "sp ace",
		"waytoolongwaytoolongwaytoolongwaytoolongwaytoolongwaytoolongwaytoolong",
		"definitely-not-an-app-xyz", // well-formed but in neither table
	}
	for _, p := range bad {
		if svc, ok := resolveService(p); ok {
			t.Errorf("resolveService(%q) = %+v, true — want refused", p, svc)
		}
	}
	// And the gate must still SAY YES to a real one, or the loop above proves only
	// that everything is refused.
	if _, ok := resolveService("kms"); !ok {
		t.Fatal(`resolveService("kms") refused a real routed app — the gate now refuses everything, ` +
			`which would make every assertion above vacuously true`)
	}
}
