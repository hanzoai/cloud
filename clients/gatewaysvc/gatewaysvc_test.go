package gatewaysvc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/gatewaypolicy"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp wires /v1/gateway over a real temp-dir store and returns both so a test
// can assert the HTTP surface and the persisted state.
func mountApp(t *testing.T) (*zip.App, *gatewaypolicy.Store) {
	t.Helper()
	st, err := gatewaypolicy.New(t.TempDir(), "admin", gatewaypolicy.Policy{PerIPRPM: 100, WindowSec: 1})
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
	var p gatewaypolicy.Policy
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
