package entitlements

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── RequireProduct middleware ──────────────────────────────────────────────────

// probe mounts a single route guarded by RequireProduct(commerce, product) and a
// terminal 200 handler, so a test can assert the gate's verdict by the status code:
// 200 = admitted (reached the handler), 402/403 = refused by the gate.
func probe(commerce cloud.CommerceClient, product string) *zip.App {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/probe", RequireProduct(commerce, product), func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	})
	return app
}

func TestRequireProduct(t *testing.T) {
	const org, product = "acme", "world"

	t.Run("entitled → pass", func(t *testing.T) {
		fc := newFakeCommerce()
		fc.grant(org, product)
		code, body := send(t, probe(fc, product), orgMember("GET", "/v1/probe", org, nil))
		if code != http.StatusOK {
			t.Fatalf("entitled: want 200, got %d (body=%s)", code, body)
		}
	})

	t.Run("definitive not-licensed → 402", func(t *testing.T) {
		fc := newFakeCommerce() // grants nothing ⇒ Active:false, no error
		code, body := send(t, probe(fc, product), orgMember("GET", "/v1/probe", org, nil))
		if code != http.StatusPaymentRequired {
			t.Fatalf("not-licensed: want 402, got %d (body=%s)", code, body)
		}
	})

	t.Run("infra error → fail open (pass)", func(t *testing.T) {
		fc := newFakeCommerce()
		fc.err = errors.New("commerce datastore unreachable")
		code, body := send(t, probe(fc, product), orgMember("GET", "/v1/probe", org, nil))
		if code != http.StatusOK {
			t.Fatalf("infra error must fail OPEN: want 200, got %d (body=%s)", code, body)
		}
		if fc.calls != 1 {
			t.Fatalf("commerce should have been consulted once; calls=%d", fc.calls)
		}
	})

	t.Run("nil commerce → fail open (pass)", func(t *testing.T) {
		code, body := send(t, probe(nil, product), orgMember("GET", "/v1/probe", org, nil))
		if code != http.StatusOK {
			t.Fatalf("nil commerce must fail OPEN: want 200, got %d (body=%s)", code, body)
		}
	})

	t.Run("unvalidated principal → 403", func(t *testing.T) {
		fc := newFakeCommerce()
		fc.grant(org, product)
		// Forged: X-Org-Id present but NO X-User-Id ⇒ not a validated principal.
		req := jsonReq("GET", "/v1/probe", nil)
		req.Header.Set("X-Org-Id", org)
		code, body := send(t, probe(fc, product), req)
		if code != http.StatusForbidden {
			t.Fatalf("unvalidated: want 403, got %d (body=%s)", code, body)
		}
		if fc.calls != 0 {
			t.Fatalf("commerce must not be consulted for an unvalidated caller; calls=%d", fc.calls)
		}
	})

	t.Run("group middleware form gates children", func(t *testing.T) {
		// Proves the real wiring shape — app.Group(prefix, RequireProduct(...)) —
		// gates a child route exactly as the inline form does.
		fc := newFakeCommerce() // grants nothing ⇒ 402
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		g := app.Group("/v1/probe", RequireProduct(fc, product))
		g.Get("/child", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]any{"ok": true}) })
		code, body := send(t, app, orgMember("GET", "/v1/probe/child", org, nil))
		if code != http.StatusPaymentRequired {
			t.Fatalf("group-gated child: want 402, got %d (body=%s)", code, body)
		}
	})
}

// ── GET /v1/entitlements projection ────────────────────────────────────────────

func decodeProjection(t *testing.T, body []byte) projectionView {
	t.Helper()
	var v projectionView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode projectionView: %v (body=%s)", err, body)
	}
	return v
}

// mountProjection wires just GET /v1/entitlements over the given commerce client.
func mountProjection(t *testing.T, commerce cloud.CommerceClient) *zip.App {
	t.Helper()
	store, err := openStore(t.TempDir() + "/entitlements.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{store: store, commerce: commerce, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/entitlements", s.projection)
	return app
}

// wantSixKeys asserts the apps map carries EXACTLY the six shell keys — the shell
// maps over them unconditionally, so a missing key is a contract break.
func wantSixKeys(t *testing.T, apps map[string]bool) {
	t.Helper()
	for _, k := range []string{"studio", "bot", "world", "platform", "team", "admin"} {
		if _, ok := apps[k]; !ok {
			t.Fatalf("apps missing key %q; got %v", k, apps)
		}
	}
	if len(apps) != 6 {
		t.Fatalf("apps must have exactly 6 keys, got %d: %v", len(apps), apps)
	}
}

func TestProjectionUnvalidatedForbidden(t *testing.T) {
	app := mountProjection(t, newFakeCommerce())
	req := jsonReq("GET", "/v1/entitlements", nil)
	req.Header.Set("X-Org-Id", "acme") // no X-User-Id ⇒ unvalidated
	code, body := send(t, app, req)
	if code != http.StatusForbidden {
		t.Fatalf("unvalidated projection: want 403, got %d (body=%s)", code, body)
	}
}

func TestProjectionPerAppBool(t *testing.T) {
	fc := newFakeCommerce()
	fc.grant("acme", "team")  // acme's plan licenses team ...
	fc.grant("acme", "world") // ... and world
	app := mountProjection(t, fc)

	code, body := send(t, app, orgMember("GET", "/v1/entitlements", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("projection: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	if v.Tier != "test" { // fakeCommerce resolves every org to plan "test"
		t.Fatalf("tier = %q, want %q", v.Tier, "test")
	}
	if !v.Apps["team"] || !v.Apps["world"] {
		t.Fatalf("granted apps must be true: %v", v.Apps)
	}
	if v.Apps["studio"] || v.Apps["bot"] || v.Apps["platform"] {
		t.Fatalf("ungranted apps must be false: %v", v.Apps)
	}
	if v.Apps["admin"] {
		t.Fatalf("non-admin caller must have admin=false: %v", v.Apps)
	}
}

func TestProjectionAdminBit(t *testing.T) {
	// A super admin (X-User-IsAdmin=true) reports admin:true regardless of products.
	app := mountProjection(t, newFakeCommerce())
	code, body := send(t, app, superAdmin("GET", "/v1/entitlements", nil))
	if code != http.StatusOK {
		t.Fatalf("admin projection: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	if !v.Apps["admin"] {
		t.Fatalf("super admin must have admin=true: %v", v.Apps)
	}
}

func TestProjectionFailsSafeNotFiveHundred(t *testing.T) {
	// Commerce error must NOT 500 the endpoint: it returns 200 with every app locked
	// (fail-safe-to-locked), so the shell degrades to locked, never crashes.
	fc := newFakeCommerce()
	fc.err = errors.New("commerce down")
	app := mountProjection(t, fc)

	code, body := send(t, app, orgMember("GET", "/v1/entitlements", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("commerce error: want 200 (fail-safe), got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	for _, k := range appProducts {
		if v.Apps[k] {
			t.Fatalf("on commerce error app %q must be locked (false): %v", k, v.Apps)
		}
	}
	if v.Tier != "" {
		t.Fatalf("tier on commerce error = %q, want empty", v.Tier)
	}
}

func TestProjectionNilCommerceLocked(t *testing.T) {
	// Commerce not co-resident: every product app locked, admin still reflects the
	// caller's bit, 200 not 500.
	app := mountProjection(t, nil)
	code, body := send(t, app, orgMember("GET", "/v1/entitlements", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("nil commerce: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	for _, k := range appProducts {
		if v.Apps[k] {
			t.Fatalf("nil commerce app %q must be false: %v", k, v.Apps)
		}
	}
}
