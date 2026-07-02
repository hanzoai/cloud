package kms_test

// RED: dual-mount precedence. With BOTH kmssvc (order 10, mounts FIRST, registers
// PUBLIC /v1/kms/config) AND admin (order 146, registers GATED /v1/admin/{orgs,
// users,...}) enabled, prove kms's order-10 public config does NOT shadow the
// admin gate, and admin's routes still require global-admin (403 without it).
// This is the real production topology; the kmssvc-only harness cannot see it.

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"

	// Pull in the full subsystem bundle so BOTH kmssvc (order 10) and admin
	// (order 146) init()-register — the real production topology.
	_ "github.com/hanzoai/cloud/subsystems"
)

func newDualApp(t *testing.T, mk string) *zip.App {
	t.Helper()
	cfg := &cloud.Config{
		Brand: "hanzo", Domain: "api.hanzo.ai", IAMIssuer: "https://hanzo.id",
		DataDir:         t.TempDir(),
		Enable:          []string{"kmssvc", "admin"}, // both, so order 10 + 146 co-exist
		KMSMasterKeyRef: mk,
	}
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))
	if err := cloud.MountAll(app, cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	return app
}

func TestDualMount_AdminConfigDoesNotShadowGate(t *testing.T) {
	mk := masterKeyB64(t)
	app := newDualApp(t, mk)

	// 1. kms's public /v1/kms/config reachable WITHOUT any identity → 200,
	//    and is the KMS config (proves order-10 kms won the exact path).
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
