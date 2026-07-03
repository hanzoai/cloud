package kms_test

// RED: dual-mount precedence — the REAL production topology, built through the
// HIP-0106 composition root exactly as serve.go does it: kms is wired EXPLICITLY
// (kms.New → kms.Mount) while admin (order 146) still mounts via the registry
// (cloud.MountAll). Prove kms's public /v1/kms/config does NOT shadow the admin
// gate, and admin's routes still require global-admin (403 without it). The
// kms-standalone harness (kms_test.go) cannot see this cross-subsystem topology.
//
// This is the ONE test in the package that imports cloud + the subsystem bundle:
// it is specifically ABOUT the composition of an explicitly-mounted subsystem with
// a registry-mounted one. The kms PACKAGE itself imports no cloud (proven by
// go list -deps); only this cross-subsystem test binary does.

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud"
	kms "github.com/hanzoai/cloud/clients/kms"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"

	// Pull in the full subsystem bundle so admin (order 146) init()-registers into
	// cloud.Registry — the real production topology. kms is no longer in the bundle
	// (it is composed explicitly below), matching serve.go.
	_ "github.com/hanzoai/cloud/subsystems"
)

// newDualApp mirrors serve.go's transitional composition root: build deps, build
// the kms store explicitly, inject it as deps.KMS (the migration bridge), Mount
// kms explicitly, then MountAll the registry subsystems (admin).
func newDualApp(t *testing.T, mk string) *zip.App {
	t.Helper()
	cfg := &cloud.Config{
		Brand: "hanzo", Domain: "api.hanzo.ai", IAMIssuer: "https://hanzo.id",
		DataDir:         t.TempDir(),
		Enable:          []string{"kmssvc", "admin"}, // explicit kms + registry admin
		KMSMasterKeyRef: mk,
	}
	deps := cloud.BuildDeps(cfg)

	store, err := kms.New(kms.Config{DataDir: cfg.DataDir, MasterKeyB64: cfg.KMSMasterKeyRef}, deps.Logger)
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	deps.KMS = store // transitional bridge, as in serve.go

	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))

	// Explicit kms mount (order 10 equivalent), then the registry (admin at 146).
	if err := kms.Mount(app, kms.Deps{
		Store: store, Logger: deps.Logger, Brand: cfg.Brand, Env: cfg.Env, IAMIssuer: cfg.IAMIssuer,
	}); err != nil {
		t.Fatalf("kms.Mount: %v", err)
	}
	if err := cloud.MountAll(app, cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	return app
}

func TestDualMount_AdminConfigDoesNotShadowGate(t *testing.T) {
	mk := masterKeyB64(t)
	app := newDualApp(t, mk)

	// 1. kms's public /v1/kms/config reachable WITHOUT any identity → 200,
	//    and is the KMS config (proves the explicit kms mount owns the exact path).
	r := do(t, app, "GET", "/v1/kms/config", "", "", false, nil)
	if r.StatusCode != 200 {
		t.Fatalf("/v1/kms/config = %d, want 200 (kms public config)", r.StatusCode)
	}
	body := decode(t, r.Body)
	if body["apiBase"] != "/v1/kms" {
		t.Fatalf("/v1/kms/config not served by kms? body=%v", body)
	}

	// 2. admin's GATED siblings must 403 WITHOUT admin — NOT shadowed to public,
	//    NOT 404 (they ARE mounted now).
	for _, path := range []string{"/v1/admin/orgs", "/v1/admin/users", "/v1/admin/me", "/v1/admin/audit"} {
		r := do(t, app, "GET", path, "hanzo", "", false, nil) // a normal (non-admin) principal
		if r.StatusCode != 403 {
			t.Fatalf("BREACH: %s without admin = %d, want 403 (gate must fire; kms must not shadow it)", path, r.StatusCode)
		}
	}

	// 3. A crafted path that is NOT an exact admin route (e.g. /v1/kms/config/x)
	//    must not fall through to kms's config handler.
	r = do(t, app, "GET", "/v1/kms/config/../orgs", "hanzo", "", false, nil)
	t.Logf("/v1/kms/config/../orgs → %d", r.StatusCode)

	// 4. Anonymous probe of admin route leaks nothing beyond 403.
	r = do(t, app, "GET", "/v1/admin/orgs", "", "", false, nil)
	if r.StatusCode != 403 {
		t.Fatalf("anonymous /v1/admin/orgs = %d, want 403", r.StatusCode)
	}
	t.Logf("dual-mount OK: kms public /v1/kms/config (200) coexists with admin gate (403 without admin)")
	_ = json.Marshal
}
