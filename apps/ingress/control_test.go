package ingress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The control plane is TYPED, and typing moved four facts out of the handler and
// into the framework: the success status, the 204 a void op answers, which id
// wins when the URL and the body disagree, and whether a DELETE reads a body.
// None of those are visible in the handler source any more, so they are pinned
// HERE — against a real router, over real requests. The store/engine tests next
// door prove the edge routes; these prove the API that configures it.

// mountControl mounts the /v1/ingress control plane on its own app + data dir.
// App role (CLOUD_INGRESS_EDGE_ENABLED unset), so no listener is bound.
func mountControl(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_INGRESS_EDGE_ENABLED", "")
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	if err := Mount(app, cloud.Deps{Logger: luxlog.NewNoOpLogger(), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	return app
}

// call drives one request. org=="" sends no principal at all; admin says whether
// the gateway attested SuperAdmin — the two facts admin() turns on.
func call(t *testing.T, app *zip.App, method, path, org string, admin bool, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		// principal.Org validates on X-User-Id: the gateway sets it only from a
		// verified credential, so an org without one is not a principal.
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// seedService puts a backend pool the routes below can reference.
func seedService(t *testing.T, app *zip.App, org, id string) {
	t.Helper()
	code, body := call(t, app, http.MethodPost, "/v1/ingress/services", org, true,
		`{"id":"`+id+`","backends":[{"url":"http://127.0.0.1:9001"}]}`)
	if code != http.StatusOK {
		t.Fatalf("seed service: %d %s", code, body)
	}
}

// TestControlPlaneRequiresSuperAdmin pins the gate: the edge is platform
// infrastructure, so a validated non-admin tenant is refused exactly like an
// unauthenticated caller, on a read and on a write alike.
func TestControlPlaneRequiresSuperAdmin(t *testing.T) {
	app := mountControl(t)

	for _, c := range []struct {
		name         string
		org          string
		admin        bool
		method, path string
	}{
		{"no principal", "", false, http.MethodGet, "/v1/ingress/status"},
		{"tenant, not admin", "acme", false, http.MethodGet, "/v1/ingress/status"},
		{"tenant, not admin, list", "acme", false, http.MethodGet, "/v1/ingress/routes"},
		{"tenant, not admin, tls", "acme", false, http.MethodGet, "/v1/ingress/tls"},
		{"tenant, not admin, delete", "acme", false, http.MethodDelete, "/v1/ingress/routes/r1"},
	} {
		if code, body := call(t, app, c.method, c.path, c.org, c.admin, ""); code != http.StatusForbidden {
			t.Errorf("%s: %s %s = %d, want 403 (%s)", c.name, c.method, c.path, code, body)
		}
	}
}

// TestStatusShape pins the status view's field names and its app-role answer.
func TestStatusShape(t *testing.T) {
	app := mountControl(t)

	code, body := call(t, app, http.MethodGet, "/v1/ingress/status", "admin", true, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("status body: %v (%s)", err, body)
	}
	for _, k := range []string{"role", "edgeEnabled", "httpAddr", "httpsAddr",
		"acmeStaging", "acmeCacheDir", "liveHosts", "tlsHosts", "proxy"} {
		if _, ok := got[k]; !ok {
			t.Errorf("status is missing %q: %s", k, body)
		}
	}
	if got["role"] != "app" || got["edgeEnabled"] != false {
		t.Errorf("app role must not bind listeners: %s", body)
	}
}

// TestObjectLifecycle walks one route through create, list, read, replace and
// delete, pinning the status of each — 200 on a create (NOT 201: this API has
// always answered 200 and a typed op must not move it), 204 with an EMPTY body
// on a delete, 404 once it is gone.
func TestObjectLifecycle(t *testing.T) {
	app := mountControl(t)
	seedService(t, app, "admin", "pool")

	code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"id":"web","host":"App.Example.COM.","service":"pool"}`)
	if code != http.StatusOK {
		t.Fatalf("create route = %d, want 200 (%s)", code, body)
	}
	var created Route
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	// validate() canonicalises the host, and the response is the stored object.
	if created.ID != "web" || created.Host != "app.example.com" {
		t.Fatalf("create echoed %+v", created)
	}

	code, body = call(t, app, http.MethodGet, "/v1/ingress/routes", "admin", true, "")
	if code != http.StatusOK {
		t.Fatalf("list = %d (%s)", code, body)
	}
	var listed struct {
		Routes []Route `json:"routes"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("list body: %v (%s)", err, body)
	}
	if len(listed.Routes) != 1 || listed.Routes[0].ID != "web" {
		t.Fatalf("list = %s", body)
	}

	code, body = call(t, app, http.MethodGet, "/v1/ingress/routes/web", "admin", true, "")
	if code != http.StatusOK {
		t.Fatalf("get = %d (%s)", code, body)
	}
	var one Route
	if err := json.Unmarshal(body, &one); err != nil || one.ID != "web" {
		t.Fatalf("get body: %v (%s)", err, body)
	}

	code, body = call(t, app, http.MethodDelete, "/v1/ingress/routes/web", "admin", true, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204 (%s)", code, body)
	}
	if len(body) != 0 {
		t.Fatalf("204 must carry no body, got %q", body)
	}
	if code, _ := call(t, app, http.MethodDelete, "/v1/ingress/routes/web", "admin", true, ""); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/ingress/routes/web", "admin", true, ""); code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", code)
	}
}

// TestURLIsTheAddressingAuthority pins the precedence a typed op inherits: the
// path id wins over an id in the body (PUT), and a DELETE addresses what it
// deletes with its URL and never reads a body — so a body naming another object
// can neither redirect the write nor smuggle a second delete.
func TestURLIsTheAddressingAuthority(t *testing.T) {
	app := mountControl(t)
	seedService(t, app, "admin", "pool")

	// PUT /routes/keep with a body claiming id "spoof" must write "keep".
	code, body := call(t, app, http.MethodPut, "/v1/ingress/routes/keep", "admin", true,
		`{"id":"spoof","host":"keep.example.com","service":"pool"}`)
	if code != http.StatusOK {
		t.Fatalf("put = %d (%s)", code, body)
	}
	var put Route
	if err := json.Unmarshal(body, &put); err != nil || put.ID != "keep" {
		t.Fatalf("path id must win over body id: %v (%s)", err, body)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/ingress/routes/spoof", "admin", true, ""); code != http.StatusNotFound {
		t.Fatalf("body id was written: /routes/spoof = %d, want 404", code)
	}

	// A second object, so the DELETE below has something to spoof toward.
	if code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"id":"other","host":"other.example.com","service":"pool"}`); code != http.StatusOK {
		t.Fatalf("create other = %d (%s)", code, body)
	}
	if code, _ := call(t, app, http.MethodDelete, "/v1/ingress/routes/keep", "admin", true,
		`{"id":"other"}`); code != http.StatusNoContent {
		t.Fatal("delete with a body must still delete the URL's id")
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/ingress/routes/other", "admin", true, ""); code != http.StatusOK {
		t.Fatalf("the body's id was deleted: /routes/other = %d, want 200", code)
	}
}

// TestCreateMintsAnIDAndRejectsInvalid pins the two ends of the create path: an
// omitted id is generated, and an object that fails validate() is 400.
func TestCreateMintsAnIDAndRejectsInvalid(t *testing.T) {
	app := mountControl(t)
	seedService(t, app, "admin", "pool")

	code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"host":"minted.example.com","service":"pool"}`)
	if code != http.StatusOK {
		t.Fatalf("create = %d (%s)", code, body)
	}
	var minted Route
	if err := json.Unmarshal(body, &minted); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	if minted.ID == "" {
		t.Fatalf("create must mint an id: %s", body)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/ingress/routes/"+minted.ID, "admin", true, ""); code != http.StatusOK {
		t.Fatalf("minted id %q is not addressable", minted.ID)
	}

	if code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"host":"no such host","service":"pool"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid host = %d, want 400 (%s)", code, body)
	}
	if code, body := call(t, app, http.MethodPost, "/v1/ingress/middlewares", "admin", true,
		`{"id":"m1","type":"stripPrefix"}`); code != http.StatusBadRequest {
		t.Fatalf("stripPrefix without prefixes = %d, want 400 (%s)", code, body)
	}
}

// TestHostIsAGlobalClaim pins the 409: a host is unique across the WHOLE edge,
// so a second org cannot take one another org already routes.
func TestHostIsAGlobalClaim(t *testing.T) {
	app := mountControl(t)
	seedService(t, app, "admin", "pool")
	seedService(t, app, "other", "pool")

	if code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"id":"mine","host":"contested.example.com","service":"pool"}`); code != http.StatusOK {
		t.Fatalf("first claim = %d (%s)", code, body)
	}
	code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "other", true,
		`{"id":"theirs","host":"contested.example.com","service":"pool"}`)
	if code != http.StatusConflict {
		t.Fatalf("second claim = %d, want 409 (%s)", code, body)
	}
}

// TestOrgScoped pins the isolation: the storage key is the admin org the gateway
// validated, so one admin org never reads or deletes another's objects.
func TestOrgScoped(t *testing.T) {
	app := mountControl(t)
	seedService(t, app, "admin", "pool")
	if code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"id":"private","host":"private.example.com","service":"pool"}`); code != http.StatusOK {
		t.Fatalf("create = %d (%s)", code, body)
	}

	if code, _ := call(t, app, http.MethodGet, "/v1/ingress/routes/private", "other", true, ""); code != http.StatusNotFound {
		t.Fatal("another org read the route")
	}
	if code, _ := call(t, app, http.MethodDelete, "/v1/ingress/routes/private", "other", true, ""); code != http.StatusNotFound {
		t.Fatal("another org deleted the route")
	}
	code, body := call(t, app, http.MethodGet, "/v1/ingress/routes", "other", true, "")
	if code != http.StatusOK {
		t.Fatalf("list = %d (%s)", code, body)
	}
	var listed struct {
		Routes []Route `json:"routes"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("list body: %v (%s)", err, body)
	}
	if len(listed.Routes) != 0 {
		t.Fatalf("another org listed %d routes: %s", len(listed.Routes), body)
	}
}

// TestListEnvelopes pins the three collection keys. They are the envelope every
// generated SDK unwraps, and they are NOT the same word as the path segment for
// free — each one is written out.
func TestListEnvelopes(t *testing.T) {
	app := mountControl(t)

	for path, key := range map[string]string{
		"/v1/ingress/routes":      "routes",
		"/v1/ingress/services":    "services",
		"/v1/ingress/middlewares": "middlewares",
	} {
		code, body := call(t, app, http.MethodGet, path, "admin", true, "")
		if code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", path, code, body)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("%s body: %v (%s)", path, err, body)
		}
		if len(got) != 1 {
			t.Errorf("%s envelope has %d keys, want 1: %s", path, len(got), body)
		}
		if _, ok := got[key]; !ok {
			t.Errorf("%s must answer under %q: %s", path, key, body)
		}
	}
}

// TestTLSRoundTrip pins the TLS view: a PUT echoes the normalized config, and the
// GET reports it alongside the edge-wide facts — including managedHosts, which is
// the union across orgs because one process holds one certificate cache.
func TestTLSRoundTrip(t *testing.T) {
	app := mountControl(t)
	seedService(t, app, "admin", "pool")
	if code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"id":"secure","host":"secure.example.com","service":"pool","tls":true}`); code != http.StatusOK {
		t.Fatalf("create tls route = %d (%s)", code, body)
	}

	code, body := call(t, app, http.MethodPut, "/v1/ingress/tls", "admin", true,
		`{"acmeEmail":"ops@example.com","extraHosts":["WWW.Example.com."]}`)
	if code != http.StatusOK {
		t.Fatalf("put tls = %d (%s)", code, body)
	}
	var cfg TLSConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("put tls body: %v (%s)", err, body)
	}
	if len(cfg.ExtraHosts) != 1 || cfg.ExtraHosts[0] != "www.example.com" {
		t.Fatalf("extraHosts must be normalized: %s", body)
	}

	code, body = call(t, app, http.MethodGet, "/v1/ingress/tls", "admin", true, "")
	if code != http.StatusOK {
		t.Fatalf("get tls = %d (%s)", code, body)
	}
	var view struct {
		Config        TLSConfig `json:"config"`
		Role          string    `json:"role"`
		EdgeEnabled   bool      `json:"edgeEnabled"`
		ManagedHosts  []string  `json:"managedHosts"`
		ACMEDirectory string    `json:"acmeDirectory"`
		ACMEEmail     string    `json:"acmeEmail"`
		Note          string    `json:"note"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("get tls body: %v (%s)", err, body)
	}
	if view.Config.ACMEEmail != "ops@example.com" || view.Role != "app" {
		t.Fatalf("tls view: %s", body)
	}
	// Sorted union: the TLS-marked route's host and the org's extraHost.
	if len(view.ManagedHosts) != 2 ||
		view.ManagedHosts[0] != "secure.example.com" || view.ManagedHosts[1] != "www.example.com" {
		t.Fatalf("managedHosts = %v, want the sorted union: %s", view.ManagedHosts, body)
	}
	if view.ACMEDirectory == "" || view.Note == "" {
		t.Fatalf("tls view must state the directory and the hot-apply note: %s", body)
	}
}

// TestMutationHotApplies pins the whole point of the subsystem: there is no
// config file and no restart, so a create is live in the engine when the request
// returns, and a delete is gone from it.
func TestMutationHotApplies(t *testing.T) {
	app := mountControl(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("live:" + r.URL.Path))
	}))
	defer backend.Close()

	if code, body := call(t, app, http.MethodPost, "/v1/ingress/services", "admin", true,
		`{"id":"pool","backends":[{"url":"`+backend.URL+`"}]}`); code != http.StatusOK {
		t.Fatalf("service = %d (%s)", code, body)
	}
	if code, body := call(t, app, http.MethodPost, "/v1/ingress/routes", "admin", true,
		`{"id":"hot","host":"hot.example.com","service":"pool"}`); code != http.StatusOK {
		t.Fatalf("route = %d (%s)", code, body)
	}

	rec := httptest.NewRecorder()
	mounted.State.engine.ServeHTTP(rec, httptest.NewRequest("GET", "http://hot.example.com/x", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "live:/x" {
		t.Fatalf("route did not hot-apply: %d %q", rec.Code, rec.Body.String())
	}

	if code, _ := call(t, app, http.MethodDelete, "/v1/ingress/routes/hot", "admin", true, ""); code != http.StatusNoContent {
		t.Fatal("delete")
	}
	rec = httptest.NewRecorder()
	mounted.State.engine.ServeHTTP(rec, httptest.NewRequest("GET", "http://hot.example.com/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted route still served: %d", rec.Code)
	}
}
