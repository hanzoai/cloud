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
	"sort"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// liveOps mounts one app and reads back every op it registers on the plane.
func liveOps(t *testing.T, name string, mount cloud.MountFunc, global bool) []string {
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
	app := zip.New(zip.Config{Logger: deps.Logger, DisableStartupMessage: true})
	if err := cloud.MountAll(app, []cloud.Plugin{{
		Name: name, Price: cloud.Free, Mount: mount, Global: global,
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
	live := liveOps(t, commercepeer.App, commerce.Mount, true)

	generated := append([]string(nil), commercepeer.Ops...)
	sort.Strings(generated)

	if len(live) == 0 {
		t.Fatal("commerce registered no plane ops at all — the mount, not the generator, is the thing to look at")
	}
	if len(generated) != len(live) {
		t.Fatalf("generated %d ops, commerce serves %d\n  generated: %v\n  live:      %v\n"+
			"  fix: go run ./plane/gen", len(generated), len(live), generated, live)
	}
	for i := range live {
		if generated[i] != live[i] {
			t.Fatalf("op %d: generated %q, commerce serves %q\n  fix: go run ./plane/gen",
				i, generated[i], live[i])
		}
	}
}
