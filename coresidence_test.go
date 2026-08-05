package cloud

// coresidence_test.go — the scorer seam's composition argument, as a GATE.
//
// risk.go states it in prose: this is an IN-PROCESS handoff, and which apps share
// a process is a per-plugin CHOICE rather than a law. Two of those choices are
// currently load-bearing for the fleet's safety, and both are one import away from
// being reversed by somebody who has not read that file:
//
//	apps/commerce INSTALLS a scorer (installRiskScorer, on the credit door's own
//	plane client). It is the seam's first and only producer.
//	apps/gateway ARMS the fleet-wide abuse gate on cloud.RiskScorerInstalled(),
//	and refuses mode=live while that reads false.
//
// As composed today plugin/commerce links commerce alone and plugin/gateway links
// gateway alone, so the gateway process sees no scorer and the abuse gate cannot
// be armed. That refusal is the fail-SAFE direction and it is currently
// UNCONDITIONAL — which is exactly what makes it fragile: nothing anywhere fails
// when it stops being true.
//
// Link the two into one root and RiskScorerInstalled() starts answering true in
// the process that arms the gate. The gate would then be armed on the strength of
// a CO-RESIDENCY ACCIDENT rather than on the question arming actually asks —
// whether the risk plane can answer for the FLEET, which is a cross-process ask no
// in-process global can settle (gateway.go says so at the check itself). Arming a
// fail-CLOSED privileged gate across the whole API plane on a wrong answer is not
// a defect that shows up in a test of either app.
//
// So the invariant is structural, and it is checked structurally: no composition
// root may LINK both. Imports are followed transitively, because linking is
// transitive — a root that reaches commerce through three intermediate packages
// has linked it just as surely as one that names it.

import (
	"go/build"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// modulePath is this module, and therefore the prefix that separates a
// FIRST-PARTY import from a dependency.
//
// Only first-party edges are followed, and that is complete rather than a
// shortcut: apps/commerce and apps/gateway are packages of this module, and no
// package outside it can import back into it. A path into either therefore lies
// entirely inside this module.
const modulePath = "github.com/hanzoai/cloud/"

// the two apps whose co-residence the seam's safety currently rests on.
const (
	scorerApp = "github.com/hanzoai/cloud/apps/commerce"
	gateApp   = "github.com/hanzoai/cloud/apps/gateway"
)

func TestComposition_NoRootLinksBothTheScorerAndTheAbuseGate(t *testing.T) {
	roots := compositionRoots(t)
	if len(roots) < 2 {
		t.Fatalf("found %d composition roots — the scan is not looking at the fleet, so its "+
			"silence proves nothing", len(roots))
	}

	// Counted INDEPENDENTLY of the violation below, so that a root which links both
	// still proves the scan can see each of them. Folding the two questions together
	// would make a real violation also report the scan as blind, which is the
	// opposite of what happened.
	var scorerRoots, gateRoots int
	for _, root := range roots {
		linked := links(t, root)
		hasScorer, hasGate := linked[scorerApp], linked[gateApp]
		if hasScorer {
			scorerRoots++
		}
		if hasGate {
			gateRoots++
		}
		if hasScorer && hasGate {
			t.Errorf("%s links BOTH %s and %s.\n"+
				"\tcommerce installs the risk scorer and gateway arms its fleet-wide abuse gate on "+
				"cloud.RiskScorerInstalled(). In one process that predicate answers true, and the "+
				"gate becomes armable on a co-residency accident rather than on whether the risk "+
				"plane can answer for the FLEET — which is a cross-process question no in-process "+
				"global settles. Arm it deliberately (a plane op, as the obs event door does), "+
				"never by linking.", root, scorerApp, gateApp)
		}
	}

	// ANTI-VACUITY. A renamed app, a moved root directory or a typo in either
	// constant above would make the assertion pass by reaching nothing at all, and a
	// gate that cannot fail is not a gate.
	if scorerRoots == 0 {
		t.Errorf("no composition root links %s — the scan cannot see the scorer's installer, "+
			"so it is not policing anything", scorerApp)
	}
	if gateRoots == 0 {
		t.Errorf("no composition root links %s — the scan cannot see the abuse gate, "+
			"so it is not policing anything", gateApp)
	}
}

// compositionRoots is every `package main` under the two trees that hold them:
// plugin/<app> is an app's own root, cmd/<binary> is a program's.
func compositionRoots(t *testing.T) []string {
	t.Helper()
	var roots []string
	for _, tree := range []string{"plugin", "cmd"} {
		err := filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			// A directory with no Go files, or one that will not resolve, is simply not
			// a root — never a reason to fail the invariant.
			if pkg, err := build.ImportDir(path, 0); err == nil && pkg.Name == "main" {
				roots = append(roots, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}
	return roots
}

// links is every first-party package a root reaches, transitively.
func links(t *testing.T, root string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			// Unresolvable or Go-free: it contributes no edges. The anti-vacuity
			// assertions above are what catch a scan that resolves too little.
			return
		}
		// Imports only — never TestImports. A test's dependencies are not linked into
		// the binary, and the invariant is about what the binary holds.
		for _, imp := range pkg.Imports {
			if !strings.HasPrefix(imp, modulePath) {
				continue
			}
			if seen[imp] {
				continue
			}
			seen[imp] = true
			walk(strings.TrimPrefix(imp, modulePath))
		}
	}
	walk(root)
	return seen
}
