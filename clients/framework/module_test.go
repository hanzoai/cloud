package framework

import (
	"encoding/json"
	"net/http"
	"testing"
)

// testFixtures is a tiny two-DocType lane used to exercise the generic install
// path without depending on any real app lane (clients/cms imports framework, so
// framework's own tests can't import it back).
func testFixtures() []DocType {
	return []DocType{
		{Name: "Widget", Fields: []DocField{{Fieldname: "code", Fieldtype: FieldData, Reqd: true}}},
		{Name: "Gadget", Module: "wrong", Fields: []DocField{{Fieldname: "label", Fieldtype: FieldData}}},
	}
}

// withTestModule registers a lane for the duration of a test and resets the
// process-global registry afterward (mirrors resetHooks in the hook tests).
func withTestModule(t *testing.T, module string, fx []DocType) {
	t.Helper()
	resetModules()
	RegisterModule(module, fx)
	t.Cleanup(resetModules)
}

func TestInstallModule_CreatesFixturesStampedWithModule(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)

	code, body := do(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("install want 200, got %d (%s)", code, body)
	}
	var res struct {
		Module   string   `json:"module"`
		Created  []string `json:"created"`
		Existing []string `json:"existing"`
	}
	_ = json.Unmarshal(body, &res)
	if res.Module != "shop" || len(res.Created) != 2 || len(res.Existing) != 0 {
		t.Fatalf("install result mismatch: %+v", res)
	}

	// The fixtures now exist in the org AND carry the module tag (Gadget's stray
	// "wrong" module is overwritten with the lane's own name).
	code, body = do(t, app, http.MethodGet, "/v1/framework/doctypes/Gadget", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get installed doctype want 200, got %d (%s)", code, body)
	}
	var dt DocType
	_ = json.Unmarshal(body, &dt)
	if dt.Module != "shop" {
		t.Fatalf("installed doctype module want %q, got %q", "shop", dt.Module)
	}
}

func TestInstallModule_Idempotent(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)

	if code, body := do(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", nil); code != http.StatusOK {
		t.Fatalf("first install want 200, got %d (%s)", code, body)
	}
	// Second install creates nothing and reports everything as already present.
	code, body := do(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", nil)
	var res struct {
		Created  []string `json:"created"`
		Existing []string `json:"existing"`
	}
	_ = json.Unmarshal(body, &res)
	if code != http.StatusOK || len(res.Created) != 0 || len(res.Existing) != 2 {
		t.Fatalf("idempotent install want created=0 existing=2, got %d %+v", code, res)
	}
}

func TestInstallModule_UnknownModule404(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/modules/ghost/install", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("unknown module install want 404, got %d", code)
	}
}

// TestInstallModule_TenantIsolation: installing into one org NEVER creates the
// lane's DocTypes in another org.
func TestInstallModule_TenantIsolation(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)

	if code, _ := do(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", nil); code != http.StatusOK {
		t.Fatal("install into acme failed")
	}
	// A different tenant sees NONE of acme's installed DocTypes.
	code, body := do(t, app, http.MethodGet, "/v1/framework/doctypes", "victim", nil)
	var list struct {
		Data []DocType `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Data) != 0 {
		t.Fatalf("victim org must have zero doctypes, got %d %+v", code, list.Data)
	}
}

// TestInstallModule_ForgedPrincipalRefused: an X-Org-Id with no validated
// principal (no X-User-Id) is refused before any store access.
func TestInstallModule_ForgedPrincipalRefused(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)
	// call with empty user = no validated principal.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "victim", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("forged-principal install want 403, got %d", code)
	}
}

// TestInstallModule_NonOwnerDenied: after the owner is seeded (trust-on-first-use),
// a different member of the same org who is not a System Manager cannot install.
func TestInstallModule_NonOwnerDenied(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)

	// u_acme installs first → becomes System Manager (owner seed).
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", "u_acme", false, nil); code != http.StatusOK {
		t.Fatal("owner install failed")
	}
	// A different, non-admin member of acme is denied (org is now owned).
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", "u_intruder", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-owner install want 403, got %d", code)
	}
}

func TestListAndGetModule(t *testing.T) {
	withTestModule(t, "shop", testFixtures())
	app := mountApp(t)

	// listModules
	code, body := do(t, app, http.MethodGet, "/v1/framework/modules", "acme", nil)
	var lst struct {
		Data []struct {
			Module   string   `json:"module"`
			Doctypes []string `json:"doctypes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &lst)
	if code != http.StatusOK || len(lst.Data) != 1 || lst.Data[0].Module != "shop" || len(lst.Data[0].Doctypes) != 2 {
		t.Fatalf("listModules mismatch: %d %+v", code, lst.Data)
	}

	// getModule before install → installed empty.
	code, body = do(t, app, http.MethodGet, "/v1/framework/modules/shop", "acme", nil)
	var g struct {
		Doctypes  []string `json:"doctypes"`
		Installed []string `json:"installed"`
	}
	_ = json.Unmarshal(body, &g)
	if code != http.StatusOK || len(g.Doctypes) != 2 || len(g.Installed) != 0 {
		t.Fatalf("getModule (pre-install) mismatch: %d %+v", code, g)
	}

	// Install, then getModule → installed lists both.
	do(t, app, http.MethodPost, "/v1/framework/modules/shop/install", "acme", nil)
	_, body = do(t, app, http.MethodGet, "/v1/framework/modules/shop", "acme", nil)
	_ = json.Unmarshal(body, &g)
	if len(g.Installed) != 2 {
		t.Fatalf("getModule (post-install) installed want 2, got %+v", g.Installed)
	}
}

// TestInstallModule_ReservedRouteNotShadowed proves "modules" is a reserved
// DocType name, so the static module routes can never be shadowed by a document
// route (define of a DocType named "modules" is refused).
func TestInstallModule_ReservedRouteNotShadowed(t *testing.T) {
	if err := (&DocType{Name: "modules", Fields: []DocField{{Fieldname: "a", Fieldtype: FieldData}}}).Validate(); err == nil {
		t.Fatal("DocType named \"modules\" must be reserved (Validate should fail)")
	}
}
