package zt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeZT is a stand-in for the real Hanzo Zero Trust controller's Edge Management
// API. It speaks the Ziti password-auth + {data,meta} envelope and, crucially,
// scopes NOTHING itself — it returns the FULL cross-org inventory so a test can
// prove that cloud (not the controller) enforces the "org-<org>" role-attribute
// tenant boundary. It records the auth it saw for assertions.
type fakeZT struct {
	lastAuthUser string
	authCount    int
	services     []map[string]any
	routers      []map[string]any

	// list401Once makes the FIRST /services list answer 401 (a stale session), to
	// exercise the client's re-auth-and-retry-once path.
	list401Once bool
	listCalls   int
}

func writePage(w http.ResponseWriter, items []map[string]any) {
	if items == nil {
		items = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": items,
		"meta": map[string]any{"pagination": map[string]any{
			"limit": perPage, "offset": 0, "totalCount": len(items),
		}},
	})
}

func (f *fakeZT) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc(mgmtBase+"/authenticate", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("method") != "password" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastAuthUser = body["username"]
		f.authCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"token":     "sess-tok",
			"expiresAt": time.Now().Add(20 * time.Minute).Format(time.RFC3339),
		}})
	})

	mux.HandleFunc(mgmtBase+"/services", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.list401Once && f.listCalls == 0 {
			f.listCalls++
			w.WriteHeader(http.StatusUnauthorized) // stale session on first read
			return
		}
		f.listCalls++
		writePage(w, f.services)
	})

	mux.HandleFunc(mgmtBase+"/edge-routers", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writePage(w, f.routers)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountApp mounts the zt surface against the fake controller with a service
// credential configured.
func mountApp(t *testing.T, f *fakeZT) *zip.App {
	t.Helper()
	srv := f.server(t)
	t.Setenv("ZT_CONTROLLER_URL", srv.URL)
	t.Setenv("ZT_CLIENT_ID", "admin")
	t.Setenv("ZT_CLIENT_SECRET", "s3cr3t")
	t.Setenv("ZT_CA_PEM", "")
	t.Setenv("ZT_INSECURE_SKIP_VERIFY", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		// A validated principal: principal.Tenant requires a non-empty X-User-Id,
		// which the gateway sets ONLY from a verified credential. Inject it directly
		// (empty org → no user → the 403 path).
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestMeshServicesTenantScopedAndShape(t *testing.T) {
	f := &fakeZT{services: []map[string]any{
		{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}, "encryptionRequired": true},
		{"id": "svc-b", "name": "reports", "roleAttributes": []string{"org-other"}, "encryptionRequired": false},
		{"id": "svc-c", "name": "shared", "roleAttributes": []string{"env-prod"}}, // no org → invisible to all
	}}
	app := mountApp(t, f)

	// No validated principal → 403, and the request never reaches the controller.
	if code, _ := do(t, app, http.MethodGet, "/v1/mesh/services", ""); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	code, body := do(t, app, http.MethodGet, "/v1/mesh/services", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if f.lastAuthUser != "admin" {
		t.Fatalf("cloud must authenticate with the ZT service credential, got user %q", f.lastAuthUser)
	}
	var listed struct {
		Services []meshView `json:"services"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	// acme sees ONLY its own service; the org-other and untagged services are filtered.
	if len(listed.Services) != 1 {
		t.Fatalf("acme want 1 mesh service, got %d (%+v)", len(listed.Services), listed.Services)
	}
	m := listed.Services[0]
	if m.ID != "svc-a" || m.Service != "checkout" || m.Mtls != "required" || m.Status != "active" {
		t.Fatalf("mesh view mismatch: %+v", m)
	}

	// Cross-tenant isolation: "other" sees only its own (encryptionRequired=false → "enabled").
	code, body = do(t, app, http.MethodGet, "/v1/mesh/services", "other")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Services) != 1 || listed.Services[0].Mtls != "enabled" {
		t.Fatalf("other must see exactly its own service, got %d %+v", code, listed.Services)
	}

	// A tenant with no ZT footprint gets an honest empty list.
	code, body = do(t, app, http.MethodGet, "/v1/mesh/services", "nobody")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Services) != 0 {
		t.Fatalf("nobody must see zero services, got %d %+v", code, listed.Services)
	}
}

func TestEdgeNodesTenantScopedShapeAndHealth(t *testing.T) {
	f := &fakeZT{routers: []map[string]any{
		{"id": "er-1", "name": "sfo-1", "roleAttributes": []string{"org-acme", "region-sfo3"}, "isOnline": true},
		{"id": "er-2", "name": "sfo-2", "roleAttributes": []string{"org-acme"}, "isOnline": false, "disabled": true},
		{"id": "er-3", "name": "nyc-1", "roleAttributes": []string{"org-acme"}, "isOnline": false},
		{"id": "er-9", "name": "theirs", "roleAttributes": []string{"org-other"}, "isOnline": true},
	}}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/edge/nodes", "acme")
	if code != http.StatusOK {
		t.Fatalf("edge nodes want 200, got %d (%s)", code, body)
	}
	var out struct {
		Nodes []edgeNodeView `json:"nodes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v", err)
	}
	// acme's three routers, never org-other's.
	if len(out.Nodes) != 3 {
		t.Fatalf("acme want 3 edge nodes, got %d (%+v)", len(out.Nodes), out.Nodes)
	}
	byID := map[string]edgeNodeView{}
	for _, n := range out.Nodes {
		byID[n.ID] = n
	}
	if n := byID["er-1"]; n.Status != "online" || n.Region != "sfo3" {
		t.Fatalf("er-1 want online/sfo3, got %+v", n)
	}
	if n := byID["er-2"]; n.Status != "disabled" || n.Region != "" {
		t.Fatalf("er-2 want disabled/no-region, got %+v", n)
	}
	if n := byID["er-3"]; n.Status != "offline" {
		t.Fatalf("er-3 want offline, got %+v", n)
	}
	if _, leaked := byID["er-9"]; leaked {
		t.Fatalf("cross-tenant leak: acme saw org-other's router er-9")
	}
}

func TestNetworksDerivedFromRouters(t *testing.T) {
	f := &fakeZT{routers: []map[string]any{
		{"id": "er-1", "name": "sfo-1", "roleAttributes": []string{"org-acme"}, "isOnline": true},
		{"id": "er-2", "name": "sfo-2", "roleAttributes": []string{"org-acme"}, "isOnline": false},
		{"id": "er-9", "name": "theirs", "roleAttributes": []string{"org-other"}, "isOnline": false},
	}}
	app := mountApp(t, f)

	// acme: one overlay network, 2 nodes, connected (≥1 online).
	code, body := do(t, app, http.MethodGet, "/v1/networks", "acme")
	if code != http.StatusOK {
		t.Fatalf("networks want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Networks []networkView `json:"networks"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(listed.Networks) != 1 {
		t.Fatalf("acme want 1 network, got %d", len(listed.Networks))
	}
	nw := listed.Networks[0]
	if nw.ID != "org-acme" || nw.Name != "acme" || nw.Nodes != 2 || nw.Status != "connected" {
		t.Fatalf("network view mismatch: %+v", nw)
	}

	// other: routers exist but none online → provisioning.
	code, body = do(t, app, http.MethodGet, "/v1/networks", "other")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Networks) != 1 || listed.Networks[0].Status != "provisioning" {
		t.Fatalf("other want 1 provisioning network, got %d %+v", code, listed.Networks)
	}

	// nobody: no routers → honest empty (no fabricated overlay).
	code, body = do(t, app, http.MethodGet, "/v1/networks", "nobody")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Networks) != 0 {
		t.Fatalf("nobody want zero networks, got %d %+v", code, listed.Networks)
	}

	// networks/:id round-trips for the org's own id, and 404s another id.
	code, body = do(t, app, http.MethodGet, "/v1/networks/org-acme", "acme")
	if code != http.StatusOK {
		t.Fatalf("get own network want 200, got %d (%s)", code, body)
	}
	var one networkView
	if err := json.Unmarshal(body, &one); err != nil || one.ID != "org-acme" || one.Nodes != 2 {
		t.Fatalf("get network mismatch: %+v (err %v)", one, err)
	}
	// acme cannot address org-other's network id → 404 (an existence guard, not a peek).
	if code, _ := do(t, app, http.MethodGet, "/v1/networks/org-other", "acme"); code != http.StatusNotFound {
		t.Fatalf("cross-tenant network id want 404, got %d", code)
	}
}

func TestReauthRetryOn401(t *testing.T) {
	f := &fakeZT{
		list401Once: true,
		services:    []map[string]any{{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}}},
	}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/mesh/services", "acme")
	if code != http.StatusOK {
		t.Fatalf("after stale-session retry want 200, got %d (%s)", code, body)
	}
	if f.authCount < 2 {
		t.Fatalf("expected a re-auth after 401 (authCount>=2), got %d", f.authCount)
	}
	if !strings.Contains(string(body), `"svc-a"`) {
		t.Fatalf("retry must return the real data, got %s", body)
	}
}

func TestUnconfiguredFailsClosed503(t *testing.T) {
	t.Setenv("ZT_CONTROLLER_URL", "https://zt-controller.invalid")
	t.Setenv("ZT_CLIENT_ID", "")
	t.Setenv("ZT_CLIENT_SECRET", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	for _, path := range []string{"/v1/networks", "/v1/mesh/services", "/v1/edge/nodes"} {
		if code, _ := do(t, app, http.MethodGet, path, "acme"); code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured GET %s: want 503, got %d", path, code)
		}
	}
}
