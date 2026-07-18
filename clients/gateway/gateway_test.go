package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/gateway/edge"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp wires /v1/gateway over a real temp-dir store and returns both so a test
// can assert the HTTP surface and the persisted state.
func mountApp(t *testing.T) (*zip.App, *edge.Store) {
	t.Helper()
	st, err := edge.New(t.TempDir(), "admin", edge.Policy{PerIPRPM: 100, WindowSec: 1})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	app := zip.New(zip.Config{})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), GatewayPolicy: st}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return app, st
}

// call drives a request with the given identity headers. There is no
// SanitizeIdentity in the test app, so the raw X-* headers stand in for a
// sanitized principal (X-User-Id ⇒ validated; X-User-IsAdmin=true ⇒ SuperAdmin).
func call(t *testing.T, app *zip.App, method, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func orgAdmin(org string) map[string]string {
	return map[string]string{"X-User-Id": "u-" + org, "X-Org-Id": org}
}
func superAdmin(org string) map[string]string {
	return map[string]string{"X-User-Id": "root", "X-Org-Id": org, "X-User-IsAdmin": "true"}
}

func TestGet_RequiresPrincipal(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := call(t, app, http.MethodGet, "/v1/gateway/config", "", nil); code != 403 {
		t.Fatalf("no principal must be 403, got %d", code)
	}
}

func TestOrgAdmin_SetsOwnOrgRPM(t *testing.T) {
	app, st := mountApp(t)
	// An org admin sets its own ceiling.
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"org_rpm":60}`, orgAdmin("acme")); code != 200 {
		t.Fatalf("org OrgRPM write: got %d", code)
	}
	if got := st.OrgRPM("acme"); got != 60 {
		t.Fatalf("persisted OrgRPM(acme) = %d, want 60", got)
	}
	// GET reflects it in the effective view.
	code, body := call(t, app, http.MethodGet, "/v1/gateway/config", "", orgAdmin("acme"))
	if code != 200 {
		t.Fatalf("GET: %d", code)
	}
	var p edge.Policy
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if p.OrgRPM != 60 {
		t.Fatalf("effective OrgRPM = %d, want 60", p.OrgRPM)
	}
}

func TestOrgAdmin_CannotSetPlatform(t *testing.T) {
	app, st := mountApp(t)
	// A non-SuperAdmin PUT that carries a platform field is refused.
	code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"cors_origins":["https://x.io"]}`, orgAdmin("acme"))
	if code != 403 {
		t.Fatalf("org admin platform write must be 403, got %d", code)
	}
	if len(st.Platform().CORSOrigins) != 0 {
		t.Fatal("platform CORS must be untouched by a non-admin write")
	}
}

func TestSuperAdmin_SetsPlatform(t *testing.T) {
	app, st := mountApp(t)
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"per_ip_rpm":500}`, superAdmin("admin")); code != 200 {
		t.Fatalf("SuperAdmin platform write: got %d", code)
	}
	if got := st.Platform().PerIPRPM; got != 500 {
		t.Fatalf("platform PerIPRPM = %d, want 500", got)
	}
}

// The crux: a SuperAdmin viewing another tenant (org-switched X-Org-Id) still
// writes the PLATFORM row, not the switched tenant's row — PutPlatform targets the
// admin org explicitly.
func TestSuperAdmin_OrgSwitchedStillWritesPlatform(t *testing.T) {
	app, st := mountApp(t)
	code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{"cors_origins":["https://y.io"]}`, superAdmin("some-tenant"))
	if code != 200 {
		t.Fatalf("write: %d", code)
	}
	if got := st.Platform().CORSOrigins; len(got) != 1 || got[0] != "https://y.io" {
		t.Fatalf("platform CORS = %v, want the org-switched SuperAdmin's write on the platform row", got)
	}
	// The switched tenant's own row must NOT have received the platform CORS.
	if raw, ok, _ := st.Get(t.Context(), "some-tenant"); ok && len(raw.CORSOrigins) != 0 {
		t.Fatalf("switched tenant row must not carry platform CORS, got %v", raw.CORSOrigins)
	}
}

func TestPut_EmptyBodyRejected(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", `{}`, orgAdmin("acme")); code != 400 {
		t.Fatalf("empty policy must be 400, got %d", code)
	}
}

// getPolicy GETs the effective config for the given identity and decodes it.
func getPolicy(t *testing.T, app *zip.App, hdr map[string]string) edge.Policy {
	t.Helper()
	code, body := call(t, app, http.MethodGet, "/v1/gateway/config", "", hdr)
	if code != 200 {
		t.Fatalf("GET: %d (%s)", code, body)
	}
	var p edge.Policy
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return p
}

// TestPerOrg_CacheRateCORS_RoundTripAndIsolation is the crux of the per-org edge
// config: an org PUTs cache TTL (default + per-path) + rate + method allowlist, and
// GET round-trips them merged onto the platform CORS. A second org sets its own and
// sees ONLY its own — the per-org rows are isolated, and the first org is untouched.
func TestPerOrg_CacheRateCORS_RoundTripAndIsolation(t *testing.T) {
	app, _ := mountApp(t)

	// Platform CORS (SuperAdmin) — the shared, org-independent edge knob every
	// tenant sees in its effective view.
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config",
		`{"cors_origins":["https://acme.example"]}`, superAdmin("admin")); code != 200 {
		t.Fatalf("platform CORS write: got %d (%s)", code, body)
	}

	// acme sets its full per-org config: rate + default/per-path cache + methods.
	acmeCfg := `{"org_rpm":120,"cache_ttl_sec":30,"cache_paths":{"/v1/models":300},"methods":["get","post"]}`
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config", acmeCfg, orgAdmin("acme")); code != 200 {
		t.Fatalf("acme per-org write: got %d (%s)", code, body)
	}

	// GET(acme) round-trips cache + rate + methods, plus the platform CORS.
	acme := getPolicy(t, app, orgAdmin("acme"))
	if acme.OrgRPM != 120 {
		t.Fatalf("acme OrgRPM = %d, want 120", acme.OrgRPM)
	}
	if acme.CacheTTLSec != 30 {
		t.Fatalf("acme CacheTTLSec = %d, want 30", acme.CacheTTLSec)
	}
	if acme.CachePaths["/v1/models"] != 300 {
		t.Fatalf("acme CachePaths = %v, want /v1/models:300", acme.CachePaths)
	}
	if len(acme.Methods) != 2 || acme.Methods[0] != "GET" || acme.Methods[1] != "POST" {
		t.Fatalf("acme Methods = %v, want [GET POST] (normalized)", acme.Methods)
	}
	if len(acme.CORSOrigins) != 1 || acme.CORSOrigins[0] != "https://acme.example" {
		t.Fatalf("acme CORS = %v, want the platform CORS", acme.CORSOrigins)
	}

	// globex sets a DIFFERENT config; GET(globex) sees only its own per-org values,
	// never acme's cache_paths/methods — but the same platform CORS.
	if code, body := call(t, app, http.MethodPut, "/v1/gateway/config",
		`{"org_rpm":10,"cache_ttl_sec":5}`, orgAdmin("globex")); code != 200 {
		t.Fatalf("globex per-org write: got %d (%s)", code, body)
	}
	globex := getPolicy(t, app, orgAdmin("globex"))
	if globex.OrgRPM != 10 || globex.CacheTTLSec != 5 {
		t.Fatalf("globex config = OrgRPM %d / CacheTTLSec %d, want 10/5", globex.OrgRPM, globex.CacheTTLSec)
	}
	if len(globex.CachePaths) != 0 {
		t.Fatalf("globex must NOT inherit acme's cache_paths, got %v", globex.CachePaths)
	}
	if len(globex.Methods) != 0 {
		t.Fatalf("globex must NOT inherit acme's methods, got %v", globex.Methods)
	}
	if len(globex.CORSOrigins) != 1 || globex.CORSOrigins[0] != "https://acme.example" {
		t.Fatalf("globex CORS = %v, want the shared platform CORS", globex.CORSOrigins)
	}

	// acme is unchanged by globex's write — isolation is symmetric.
	if acme2 := getPolicy(t, app, orgAdmin("acme")); acme2.OrgRPM != 120 || acme2.CacheTTLSec != 30 || len(acme2.Methods) != 2 {
		t.Fatalf("acme mutated by globex write: %+v", acme2)
	}
}

// TestPut_ValidatesPerOrgConfig: a structurally invalid per-org body is a 400, not
// a silently-dropped field.
func TestPut_ValidatesPerOrgConfig(t *testing.T) {
	app, _ := mountApp(t)
	for _, body := range []string{
		`{"methods":["TRACE"]}`,        // unknown HTTP method
		`{"cache_paths":{"v1":10}}`,    // path without leading /
		`{"cache_ttl_sec":9999999999}`, // over MaxCacheTTLSec
	} {
		if code, _ := call(t, app, http.MethodPut, "/v1/gateway/config", body, orgAdmin("acme")); code != 400 {
			t.Fatalf("invalid body %s must be 400, got %d", body, code)
		}
	}
}
