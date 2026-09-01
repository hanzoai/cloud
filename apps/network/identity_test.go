package network

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

// send is do with a body, for the write half.
func send(t *testing.T, app *zip.App, method, path, org, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// Minting an identity writes the org's tenancy onto the fabric, never the
// caller's words verbatim: the fabric name carries the org suffix, the org role
// attribute is always present, and every supplied role is scoped — so nothing a
// request can spell claims an attribute another tenant's policy selects.
func TestMintIdentityScopesEveryRole(t *testing.T) {
	f := &fakeZT{}
	app := mountApp(t, f)

	// The "<service>-host" role names a published service, so publish it first.
	code, body := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s","host":"127.0.0.1","port":6443}`)
	if code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}

	code, body = send(t, app, http.MethodPost, "/v1/network/identities", "acme",
		`{"name":"VM-1","roles":["k3s-host","gpu"]}`)
	if code != http.StatusCreated {
		t.Fatalf("mint want 201, got %d (%s)", code, body)
	}
	var v identityView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if v.ID == "" || v.Name != "vm-1" {
		t.Fatalf("identity view mismatch: %+v", v)
	}
	if v.Enrollment == nil || v.Enrollment.JWT == "" {
		t.Fatalf("mint must answer the one-time enrollment JWT, got %+v", v.Enrollment)
	}

	// What the CONTROLLER was told: the scoped name, the Device type, the
	// one-time enrollment, and only scoped role attributes.
	made := f.created["identities"]
	if len(made) != 1 {
		t.Fatalf("controller saw %d identity writes, want 1", len(made))
	}
	id := made[0]
	if id["name"] != "vm-1.acme" || id["type"] != "Device" {
		t.Fatalf("identity write mismatch: %+v", id)
	}
	enr, _ := id["enrollment"].(map[string]any)
	if enr == nil || enr["ott"] != true {
		t.Fatalf("identity must be created with a one-time enrollment, got %+v", id["enrollment"])
	}
	roles, _ := id["roleAttributes"].([]any)
	want := []any{"org-acme", "k3s-host.acme", "gpu.acme"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v", roles, want)
		}
	}

	// A host role for a service the org has not published is refused by name.
	if code, body := send(t, app, http.MethodPost, "/v1/network/identities", "acme",
		`{"name":"vm-2","roles":["ghost-host"]}`); code != http.StatusBadRequest {
		t.Fatalf("unknown host role want 400, got %d (%s)", code, body)
	}
	// And so is a name that could not live in DNS beside the org suffix.
	if code, _ := send(t, app, http.MethodPost, "/v1/network/identities", "acme",
		`{"name":"bad_name"}`); code != http.StatusBadRequest {
		t.Fatalf("non-label name want 400, got %d", code)
	}
}

// The list is the tenancy filter, and delete may only act on what the caller
// could list: another org's id is 404 with no write reaching the controller.
func TestIdentityListAndDeleteAreTenantScoped(t *testing.T) {
	f := &fakeZT{identities: []map[string]any{
		{"id": "i-a", "name": "vm.acme", "roleAttributes": []string{"org-acme"},
			"enrollment": map[string]any{"ott": map[string]any{"jwt": "keep-me"}}},
		{"id": "i-b", "name": "box.other", "roleAttributes": []string{"org-other"}},
		{"id": "i-c", "name": "stray", "roleAttributes": []string{"env-prod"}}, // no org → invisible to all
	}}
	app := mountApp(t, f)

	code, body := send(t, app, http.MethodGet, "/v1/network/identities", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var listed identityList
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(listed.Identities) != 1 {
		t.Fatalf("acme want 1 identity, got %+v", listed.Identities)
	}
	got := listed.Identities[0]
	if got.ID != "i-a" || got.Name != "vm" {
		t.Fatalf("identity row mismatch: %+v", got)
	}
	// An un-enrolled identity still answers its one-time JWT, so a mislaid
	// token is read again rather than re-minted.
	if got.Enrollment == nil || got.Enrollment.JWT != "keep-me" {
		t.Fatalf("un-enrolled identity must carry its enrollment, got %+v", got.Enrollment)
	}

	// Cross-tenant delete: 404, and the controller never saw a delete.
	if code, _ := send(t, app, http.MethodDelete, "/v1/network/identities/i-b", "acme", ""); code != http.StatusNotFound {
		t.Fatalf("cross-org delete want 404, got %d", code)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("cross-org delete reached the controller: %v", f.deleted)
	}

	// The org's own delete goes through.
	if code, body := send(t, app, http.MethodDelete, "/v1/network/identities/i-a", "acme", ""); code >= 300 {
		t.Fatalf("own delete want 2xx, got %d (%s)", code, body)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "i-a" {
		t.Fatalf("controller deletions = %v, want [i-a]", f.deleted)
	}
}

// Publishing a service writes the whole shape in one request: the two tunneler
// configs, the org-tagged service, and the two policies — hosting held to the
// "<name>-host" attribute this surface alone can scope, dialing open to the
// org's identities and to the cloud's own.
func TestPublishServiceWritesTheWholeShape(t *testing.T) {
	f := &fakeZT{}
	app := mountApp(t, f)

	code, body := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s","host":"127.0.0.1","port":6443}`)
	if code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	var v serviceView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if v.Name != "k3s" || v.DNS != "k3s.acme.ziti" || v.ID == "" {
		t.Fatalf("service view mismatch: %+v", v)
	}

	cfgs := f.created["configs"]
	if len(cfgs) != 2 {
		t.Fatalf("want 2 configs, got %+v", cfgs)
	}
	if cfgs[0]["name"] != "k3s.acme-host" || cfgs[0]["configTypeId"] != hostV1Type {
		t.Fatalf("host config mismatch: %+v", cfgs[0])
	}
	hostData, _ := cfgs[0]["data"].(map[string]any)
	if hostData["protocol"] != "tcp" || hostData["address"] != "127.0.0.1" || hostData["port"] != float64(6443) {
		t.Fatalf("host config data mismatch: %+v", hostData)
	}
	if cfgs[1]["name"] != "k3s.acme-intercept" || cfgs[1]["configTypeId"] != interceptV1Type {
		t.Fatalf("intercept config mismatch: %+v", cfgs[1])
	}
	interceptData, _ := cfgs[1]["data"].(map[string]any)
	if addrs, _ := interceptData["addresses"].([]any); len(addrs) != 1 || addrs[0] != "k3s.acme.ziti" {
		t.Fatalf("intercept addresses mismatch: %+v", interceptData)
	}

	svcs := f.created["services"]
	if len(svcs) != 1 || svcs[0]["name"] != "k3s.acme" || svcs[0]["encryptionRequired"] != true {
		t.Fatalf("service write mismatch: %+v", svcs)
	}
	if roles, _ := svcs[0]["roleAttributes"].([]any); len(roles) != 1 || roles[0] != "org-acme" {
		t.Fatalf("service roles mismatch: %+v", svcs[0]["roleAttributes"])
	}

	pols := f.created["service-policies"]
	if len(pols) != 2 {
		t.Fatalf("want 2 policies, got %+v", pols)
	}
	bind, dial := pols[0], pols[1]
	if bind["type"] != "Bind" || dial["type"] != "Dial" {
		t.Fatalf("policy types mismatch: %+v / %+v", bind, dial)
	}
	if ir, _ := bind["identityRoles"].([]any); len(ir) != 1 || ir[0] != "#k3s-host.acme" {
		t.Fatalf("bind identityRoles mismatch: %+v", bind["identityRoles"])
	}
	if sr, _ := bind["serviceRoles"].([]any); len(sr) != 1 || sr[0] != "@"+v.ID {
		t.Fatalf("bind serviceRoles mismatch: %+v", bind["serviceRoles"])
	}
	// The dial policy admits the org's devices AND the cloud's own identity
	// (the reserved platform org), which is what lets the fleet reach a
	// published apiserver.
	if ir, _ := dial["identityRoles"].([]any); len(ir) != 2 || ir[0] != "#org-acme" || ir[1] != "#org-admin" {
		t.Fatalf("dial identityRoles mismatch: %+v", dial["identityRoles"])
	}

	// The published service is thereafter a row of the org's mesh.
	code, body = send(t, app, http.MethodGet, "/v1/network/services", "acme", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"k3s.acme"`) {
		t.Fatalf("published service must be listed, got %d (%s)", code, body)
	}

	// Refusals: a port off the wire, and a name DNS could not hold.
	if code, _ := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s","host":"127.0.0.1","port":0}`); code != http.StatusBadRequest {
		t.Fatalf("port 0 want 400, got %d", code)
	}
	if code, _ := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s_api","host":"127.0.0.1","port":6443}`); code != http.StatusBadRequest {
		t.Fatalf("non-label name want 400, got %d", code)
	}
}

// The writes stay fail-closed where the reads degrade: an unconfigured
// deployment answers 503 and fabricates nothing.
func TestWritesFailClosedWhenUnconfigured(t *testing.T) {
	t.Setenv("ZT_CONTROLLER_URL", "https://zt-controller.invalid")
	t.Setenv("ZT_CLIENT_ID", "")
	t.Setenv("ZT_CLIENT_SECRET", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/network/identities", `{"name":"vm"}`},
		{http.MethodPost, "/v1/network/services", `{"name":"k3s","host":"127.0.0.1","port":6443}`},
		{http.MethodGet, "/v1/network/identities", ""},
		{http.MethodDelete, "/v1/network/identities/i-a", ""},
	} {
		if code, _ := send(t, app, c.method, c.path, "acme", c.body); code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured %s %s: want 503, got %d", c.method, c.path, code)
		}
	}
}
