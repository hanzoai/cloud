package cloud_test

// plane_registry_test.go — the gate that lets the peer clients be GENERATED FROM
// SOURCE without the document drifting away from the program.
//
// plane/gen reads the declarations (zipdoc, the repo's other Go emitter, reads
// the same call sites the same way). zip's own Declaration argues the opposite —
// project from the LIVE ROUTER, never the AST — and it is right about the case it
// is about: a host discovering a plugin it does not build cannot parse source it
// does not have.
//
// This test is how both stay true at once. Source is the INPUT; the running
// registry is the JUDGE. An op added to commerce and not regenerated, or a
// generated op that no longer exists, fails HERE — so the generator never gets to
// be quietly wrong, which is the only real objection to reading source.

import (
	luxlog "github.com/luxfi/log"
	"sort"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/allowance"
	"github.com/hanzoai/cloud/apps/commerce"
	"github.com/hanzoai/cloud/apps/entitlement"
	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/apps/risk"
	allowancepeer "github.com/hanzoai/cloud/plane/allowance"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	entitlementpeer "github.com/hanzoai/cloud/plane/entitlement"
	eventpeer "github.com/hanzoai/cloud/plane/event"
	flagspeer "github.com/hanzoai/cloud/plane/flag"
	riskpeer "github.com/hanzoai/cloud/plane/risk"
	"github.com/zap-proto/zip"
)

// liveOps mounts one app and reads back every op it registers on the plane.
func liveOps(t *testing.T, name string, mount cloud.UseFunc, global bool) []string {
	t.Helper()
	shortRun(t)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	cfg, done, err := cloud.SpecConfig()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	t.Cleanup(done)
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: luxlog.Default(), DisableStartupMessage: true})
	if err := cloud.UseAll(app, []cloud.Plugin{{
		Name: name, Price: cloud.Free, Use: mount, Global: global,
	}}, cfg, deps); err != nil {
		t.Fatalf("mount %s: %v", name, err)
	}
	var got []string
	for _, op := range cloud.Plane().Registry() {
		got = append(got, op.OperationID)
	}
	sort.Strings(got)
	return got
}

// TestGeneratedSurfaceIsTheLiveSurface: what plane/commerce offers is exactly
// what commerce registers — no more, no less.
func TestGeneratedSurfaceIsTheLiveSurface(t *testing.T) {
	live := liveOps(t, commercepeer.App, commerce.Use, true)
	sameSurface(t, "commerce", commercepeer.Ops, live,
		"commerce registered no plane ops at all — the mount, not the generator, is the thing to look at")
}

// TestGeneratedRiskSurfaceIsTheLiveSurface holds the SCORER to the same gate, and
// it is the one op where the gate earns its keep twice over.
//
// The scorer is reached by a gate in another binary and by nothing else: there is
// no HTTP route behind it and no caller who would notice it missing until a
// payment is being screened. An op that stopped being registered — a Mount that
// no longer calls exposeDecide, a rename on one side — would leave every gate in
// the fleet reading the absent exemption and allowing unscored, silently, which is
// precisely the failure this whole client exists to end. So the registration itself
// is asserted, from the running registry.
func TestGeneratedRiskSurfaceIsTheLiveSurface(t *testing.T) {
	live := liveOps(t, riskpeer.App, risk.Use, false)
	t.Cleanup(func() { _ = risk.Shutdown(t.Context()) })
	sameSurface(t, "risk", riskpeer.Ops, live,
		"risk registered no plane ops at all — every gate in the fleet reads an absent scorer, "+
			"allows unscored, and nothing says so. Mount must call exposeDecide.")
}

// TestGeneratedAnalyticsSurfaceIsTheLiveSurface holds the EVENT ENDPOINT to the
// same gate, and it earns its keep the way the scorer's does.
//
// It is reached by peers in other binaries and by nothing else: no HTTP route
// stands behind it, and the callers that would notice it missing are the ones
// whose whole contract is that a failed emit changes nothing. An op that stopped
// being registered — a build that no longer calls exposeCapture, a rename on one
// side — would leave every peer's facts unwritten, silently, while every endpoint
// they describe kept answering exactly as before. So the registration itself is
// asserted, from the running registry.
func TestGeneratedAnalyticsSurfaceIsTheLiveSurface(t *testing.T) {
	live := liveOps(t, eventpeer.App, event.Use, false)
	t.Cleanup(func() { _ = event.Shutdown(t.Context()) })
	sameSurface(t, "event", eventpeer.Ops, live,
		"analytics registered no plane ops at all — every peer that states a fact about "+
			"its own work writes nothing, and nothing says so. build must call exposeCapture.")
}

// TestGeneratedAllowanceSurfaceIsTheLiveSurface holds the FREE-CALL COUNTER to the
// same gate, and the pair of ops is why it needs one.
//
// The counter answers two ops that must both exist and must stay distinct: a READ
// that admits a call and a TAKE that counts one that was served. They are reached by
// the AI gate in another binary and by nothing else. Lose the read and every free
// caller is refused by a gate that cannot ask; lose the take and the ceiling stops
// counting while every route keeps answering — free inference, unbounded, and nothing
// says so. So both registrations are asserted, from the running registry.
func TestGeneratedAllowanceSurfaceIsTheLiveSurface(t *testing.T) {
	live := liveOps(t, allowancepeer.App, allowance.Use, false)
	t.Cleanup(func() { _ = allowance.Shutdown(t.Context()) })
	sameSurface(t, "allowance", allowancepeer.Ops, live,
		"allowance registered no plane ops at all — the AI gate can neither admit a free "+
			"caller nor count one, and nothing says so. Mount must call expose.")
}

// TestGeneratedFlagsSurfaceIsTheLiveSurface holds the FLAG READ to the same gate,
// and its absence is the loudest of the five.
//
// It is what every capability that is not yet ga asks about every request reaching
// its prefix (HIP-0139 §8), from whichever binary serves that capability. The
// refusal fails CLOSED, so an op that stopped being registered — a Mount that no
// longer calls exposeHold, a rename on one side — does not error anywhere: it
// turns every beta product off for every customer who was let into it, and each
// one reads as a 404 that is indistinguishable from the product not existing.
// So the registration itself is asserted, from the running registry.
func TestGeneratedFlagsSurfaceIsTheLiveSurface(t *testing.T) {
	live := liveOps(t, flagspeer.App, flags.Use, false)
	t.Cleanup(func() { _ = flags.Shutdown(t.Context()) })
	sameSurface(t, "flags", flagspeer.Ops, live,
		"flags registered no plane ops at all — every capability that is not ga refuses "+
			"every org, including the ones holding its flag, and nothing says so. Mount must call exposeHold.")
}

// TestGeneratedEntitlementSurfaceIsTheLiveSurface holds the ENABLEMENT READ to
// the same gate, and it fails the same silent way its flags twin does.
//
// It is what every ELECTIVE capability asks about every request reaching its
// prefix, from whichever binary serves that capability. The refusal fails CLOSED,
// so an op that stopped being registered — a Mount that no longer calls
// exposeHolds, a rename on one side — errors nowhere: it turns every elective
// product off for every customer who PAID for it, and each one reads as a 404
// indistinguishable from the product not existing. So the registration itself is
// asserted, from the running registry.
func TestGeneratedEntitlementSurfaceIsTheLiveSurface(t *testing.T) {
	live := liveOps(t, entitlementpeer.App, entitlement.Use, false)
	t.Cleanup(func() { _ = entitlement.Shutdown(t.Context()) })
	sameSurface(t, "entitlement", entitlementpeer.Ops, live,
		"entitlement registered no plane ops at all — every elective capability refuses "+
			"every org, including the ones that enabled and are billed for it, and nothing "+
			"says so. Mount must call exposeHolds.")
}

// sameSurface holds a generated peer client to the surface its app actually
// registers: the same ops, in the same order, nothing extra on either side.
//
// ONE comparison for all of them. It stood written out once per app, so a
// difference in the message was the only thing that could distinguish them —
// and the defect does not differ: either the generator is stale or a Mount
// stopped exposing an op. What DOES differ per app is the worse case, an EMPTY
// registry, so each names its own consequence in empty.
func sameSurface(t *testing.T, app string, generated, live []string, empty string) {
	t.Helper()
	if len(live) == 0 {
		t.Fatal(empty)
	}
	generated = append([]string(nil), generated...)
	sort.Strings(generated)
	if len(generated) != len(live) {
		t.Fatalf("generated %d ops, %s serves %d\n  generated: %v\n  live:      %v\n"+
			"  fix: go run ./plane/gen", len(generated), app, len(live), generated, live)
	}
	for i := range live {
		if generated[i] != live[i] {
			t.Fatalf("op %d: generated %q, %s serves %q\n  fix: go run ./plane/gen",
				i, generated[i], app, live[i])
		}
	}
}
