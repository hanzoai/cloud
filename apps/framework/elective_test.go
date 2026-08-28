package framework

// The opt-in, at the module. Driven against a real entitlement peer on a real
// socket: both failures live in the crossing.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/internal/planetest"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/doctype"
	engine "github.com/hanzoai/framework"
	"github.com/zap-proto/zip"
)

// A module for this package's tests, registered in init so it is present before
// moduleOf builds its map on first use.
func init() {
	doctype.RegisterModule("lane", []doctype.DocType{
		{Name: "lane-thing", Module: "lane", Fields: []doctype.DocField{
			{Fieldname: "title", Fieldtype: doctype.FieldData, Label: "Title"},
		}},
	})
}

// gated mirrors the real mount. 200 means the request reached the handler.
func gated() *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	g := app.Group(prefix)
	g.Use(zip.H(elective))
	served := func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"served": c.Path()}) }
	g.Get("/modules", served)  // static: never a DocType
	g.Get("/:doctype", served) // documents
	g.Get("/:doctype/:name", served)
	return app
}

var member = map[string]string{"X-User-Id": "acme/z@acme.test", "X-Org-Id": "acme"}

func fetch(t *testing.T, app *zip.App, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// An org that has not enabled the module cannot tell its documents apart from a
// path nobody registered.
func TestModuleNotEnabledIsNotThere(t *testing.T) {
	planetest.Entitled(t, func(_, p string) bool { return p == "erp" }) // a DIFFERENT module
	app := gated()

	code, body := fetch(t, app, prefix+"/lane-thing", member)
	if code != http.StatusNotFound {
		t.Fatalf("GET lane-thing = %d %s, want 404 — a module answered an org that never enabled it", code, body)
	}
	// And on the document route too, not only the list.
	if code, _ := fetch(t, app, prefix+"/lane-thing/abc", member); code != http.StatusNotFound {
		t.Errorf("GET lane-thing/abc = %d, want 404 — the refusal covers the list and not the document", code)
	}
}

// Enabling admits, and one org's purchase is not another's.
func TestEnablingTheModuleLetsTheOrgIn(t *testing.T) {
	planetest.Entitled(t, func(org, p string) bool { return org == "acme" && p == "lane" })
	app := gated()

	if code, body := fetch(t, app, prefix+"/lane-thing", member); code != http.StatusOK {
		t.Fatalf("GET lane-thing = %d %s, want 200 — acme enabled the module and was refused", code, body)
	}
	other := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}
	if code, _ := fetch(t, app, prefix+"/lane-thing", other); code != http.StatusNotFound {
		t.Errorf("GET lane-thing as initech = %d, want 404 — one org's enablement admitted another", code)
	}
}

// What no module owns is not gated: a static segment, and an org's own DocType.
// Both fall out of the same empty answer from moduleOf.
func TestWhatNoModuleOwnsIsServed(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // no peer: proves neither asks
	app := gated()

	for _, path := range []string{prefix + "/modules", prefix + "/own-doctype"} {
		if code, body := fetch(t, app, path, member); code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200 — a path no module owns was made to depend on entitlement", path, code, body)
		}
	}
}

// An entitlement outage fails closed.
func TestEntitlementUnreachableRefusesTheModule(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // nothing is listening in it
	app := gated()

	if code, body := fetch(t, app, prefix+"/lane-thing", member); code != http.StatusNotFound {
		t.Fatalf("GET lane-thing with no entitlement peer = %d %s, want 404", code, body)
	}
}

// No validated principal is 404, not 401.
func TestNoPrincipalIsNotThereEither(t *testing.T) {
	planetest.Entitled(t, func(org, p string) bool { return org == "acme" && p == "lane" })
	app := gated()

	// The org header alone, with nothing that validated it — the shape a forged
	// client header arrives in.
	if code, _ := fetch(t, app, prefix+"/lane-thing", map[string]string{"X-Org-Id": "acme"}); code != http.StatusNotFound {
		t.Errorf("GET lane-thing unvalidated = %d, want 404", code)
	}
}

// doctypeIn runs before the router binds a parameter, so getting it wrong admits
// everything.
func TestDoctypeInReadsTheSegment(t *testing.T) {
	for _, c := range []struct {
		path string
		want string
	}{
		{prefix + "/lane-thing", "lane-thing"},
		{prefix + "/lane-thing/abc", "lane-thing"},
		{prefix + "/lane-thing/abc/submit", "lane-thing"},
		{prefix, ""},
		{prefix + "/", ""},
		{"/v1/other/lane-thing", ""},
	} {
		got, ok := doctypeIn(c.path)
		if c.want == "" {
			if ok {
				t.Errorf("doctypeIn(%q) = %q, want none", c.path, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("doctypeIn(%q) = %q,%v want %q", c.path, got, ok, c.want)
		}
	}
}

// Two orgs are two DATABASES, not two values in an `org` column. cloud.OrgStore
// keys one file per namespace, so distinct engines are distinct files.
func TestEachOrgGetsItsOwnBase(t *testing.T) {
	planetest.Entitled(t, func(_, p string) bool { return p == "lane" })
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	acme, err := engineOf("acme")
	if err != nil {
		t.Fatalf("engine acme: %v", err)
	}
	initech, err := engineOf("initech")
	if err != nil {
		t.Fatalf("engine initech: %v", err)
	}
	if acme == initech {
		t.Fatal("both orgs resolved the SAME engine — one file holds every tenant, and isolation is an `org` column again")
	}
	// And the same org is the SAME engine, or every request opens a new file.
	again, err := engineOf("acme")
	if err != nil || again != acme {
		t.Errorf("acme resolved a different engine on the second call (%v)", err)
	}
}

// An unvalidated caller is refused BEFORE a database is named: naming one is the
// first thing an op does now, and namespace answers a 500 for an empty org where
// the engine answers a 403.
func TestNoOrgIsRefusedNotCrashed(t *testing.T) {
	planetest.Entitled(t, func(string, string) bool { return true })
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	_, err := engineOf("")
	if err == nil {
		t.Fatal("an empty org named a database")
	}
	if got := engine.Classify(err); got != engine.CodeForbidden {
		t.Errorf("empty org classified %v, want %v — the caller reads a 500 instead of a 403", got, engine.CodeForbidden)
	}
}
