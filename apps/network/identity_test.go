package network

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// send is do with a body, for the write half. The caller is "u-<org>", an
// admin of that org, whose IAM subject is "sub-<org>".
func send(t *testing.T, app *zip.App, method, path, org, body string) (int, []byte) {
	t.Helper()
	sub := ""
	if org != "" {
		sub = "sub-" + org
	}
	return sendAs(t, app, method, path, org, sub, body)
}

// sendAs is send with the attested subject stated, "" for none.
func sendAs(t *testing.T, app *zip.App, method, path, org, sub, body string) (int, []byte) {
	t.Helper()
	return call(t, app, method, path, org, sub, body, "X-User-IsOrgAdmin")
}

// member is sendAs for a plain member of org: no admin bit, no machine client.
func member(t *testing.T, app *zip.App, method, path, org, sub, body string) (int, []byte) {
	t.Helper()
	return call(t, app, method, path, org, sub, body, "")
}

// call sends one request as the identity boundary would pass it on: org and
// user validated, sub attested, and authority ("X-User-IsOrgAdmin",
// "X-User-IsApp", or "" for none) minted true.
func call(t *testing.T, app *zip.App, method, path, org, sub, body, authority string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if authority != "" {
		req.Header.Set(authority, "true")
	}
	if sub != "" {
		req.Header.Set("X-Test-Subject", sub)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func ensure(t *testing.T, app *zip.App, org, body string) identityView {
	t.Helper()
	code, raw := send(t, app, http.MethodPost, "/v1/network/identities", org, body)
	if code != http.StatusCreated {
		t.Fatalf("ensure want 201, got %d (%s)", code, raw)
	}
	var v identityView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("shape: %v (%s)", err, raw)
	}
	return v
}

// The caller's IAM subject IS the identity: the controller is told to make
// one whose externalId is the subject and whose only way in is the IAM policy,
// and a second call answers the same identity without writing again.
func TestEnsureIdentityIsTheCallersSubject(t *testing.T) {
	f := &fakeZT{}
	app := mountApp(t, f)

	v := ensure(t, app, "acme", `{}`)
	if v.ID == "" || v.Name != "sub-acme" || v.ExternalID != "sub-acme" {
		t.Fatalf("identity view mismatch: %+v", v)
	}
	made := f.created["identities"]
	if len(made) != 1 {
		t.Fatalf("controller saw %d identity writes, want 1", len(made))
	}
	id := made[0]
	if id["name"] != "sub-acme" || id["externalId"] != "sub-acme" || id["authPolicyId"] != iamPolicy ||
		id["type"] != "Default" || id["isAdmin"] != false {
		t.Fatalf("identity write mismatch: %+v", id)
	}
	if _, has := id["enrollment"]; has {
		t.Fatalf("an IAM identity is never enrolled, got %+v", id["enrollment"])
	}
	if roles, _ := id["roleAttributes"].([]any); len(roles) != 1 || roles[0] != "org-acme" {
		t.Fatalf("roles = %v, want [org-acme]", id["roleAttributes"])
	}

	again := ensure(t, app, "acme", `{"name":"other-name"}`)
	if again.ID != v.ID || again.Name != v.Name {
		t.Fatalf("second ensure answered %+v, want %+v", again, v)
	}
	if n := len(f.created["identities"]) + len(f.created["identity-patches"]); n != 1 {
		t.Fatalf("second ensure wrote to the controller: %d writes", n)
	}
}

// A given name is the identity's name within the org — scoped on the fabric,
// answered in the caller's spelling — and one DNS could not hold is refused.
func TestEnsureIdentityNameOverride(t *testing.T) {
	f := &fakeZT{}
	app := mountApp(t, f)

	v := ensure(t, app, "acme", `{"name":"Laptop"}`)
	if v.Name != "laptop" || v.ExternalID != "sub-acme" {
		t.Fatalf("identity view mismatch: %+v", v)
	}
	if got := f.created["identities"][0]["name"]; got != "laptop.acme" {
		t.Fatalf("fabric name = %v, want laptop.acme", got)
	}
	if code, _ := sendAs(t, app, http.MethodPost, "/v1/network/identities", "other", "sub-other",
		`{"name":"bad_name"}`); code != http.StatusBadRequest {
		t.Fatalf("non-label name want 400, got %d", code)
	}
}

// Every role is scoped to the org, a host role names a service the org
// published, and an identity another org already holds gains this org's roles
// rather than being made twice.
func TestEnsureIdentityScopesAndAddsRoles(t *testing.T) {
	f := &fakeZT{identities: []map[string]any{
		{"id": "i-1", "name": "sub-acme", "externalId": "sub-acme", "roleAttributes": []any{"org-other"}},
	}}
	app := mountApp(t, f)
	if code, body := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s","host":"127.0.0.1","port":6443}`); code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}

	v := ensure(t, app, "acme", `{"roles":["k3s-host","gpu"]}`)
	if v.ID != "i-1" {
		t.Fatalf("ensure made a second identity: %+v", v)
	}
	if len(f.created["identities"]) != 0 || len(f.created["identity-patches"]) != 1 {
		t.Fatalf("want one patch and no create, got %v", f.created)
	}
	got, _ := f.created["identity-patches"][0]["roleAttributes"].([]any)
	want := []any{"org-other", "org-acme", "k3s-host.acme", "gpu.acme"}
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles = %v, want %v", got, want)
		}
	}

	if code, body := send(t, app, http.MethodPost, "/v1/network/identities", "acme",
		`{"roles":["ghost-host"]}`); code != http.StatusBadRequest {
		t.Fatalf("unknown host role want 400, got %d (%s)", code, body)
	}
	// A role that could spell another org's — a tenancy role, or a dotted one
	// whose scoped form ends like another org's — is refused.
	for _, bad := range []string{`"org-other"`, `"gpu.other"`} {
		if code, _ := send(t, app, http.MethodPost, "/v1/network/identities", "acme",
			`{"roles":[`+bad+`]}`); code != http.StatusBadRequest {
			t.Fatalf("role %s want 400, got %d", bad, code)
		}
	}
}

// A member of one org takes an identity out of THAT org only: a shared
// identity keeps working in the other, the last org's delete removes it, a
// person may remove themselves, and the controller's administrators are not
// the cloud's to delete.
func TestDeleteLeavesOtherOrgsAlone(t *testing.T) {
	f := &fakeZT{identities: []map[string]any{
		{"id": "i-shared", "name": "sub-p", "externalId": "sub-p",
			"roleAttributes": []string{"org-x", "gpu.x", "org-y", "k3s-host.y"}},
		{"id": "i-self", "name": "sub-x", "externalId": "sub-x", "roleAttributes": []string{"org-x", "org-y"}},
	}}
	app := mountApp(t, f)
	listed := func(org string) map[string][]string {
		t.Helper()
		code, body := send(t, app, http.MethodGet, "/v1/network/identities", org, "")
		var l identityList
		if code != http.StatusOK || json.Unmarshal(body, &l) != nil {
			t.Fatalf("list %s: %d (%s)", org, code, body)
		}
		out := map[string][]string{}
		for _, v := range l.Identities {
			out[v.ID] = v.Roles
		}
		return out
	}

	if code, body := send(t, app, http.MethodDelete, "/v1/network/identities/i-shared", "x", ""); code >= 300 {
		t.Fatalf("x delete want 2xx, got %d (%s)", code, body)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("x's delete removed an identity y still has: %v", f.deleted)
	}
	if roles := listed("y")["i-shared"]; len(roles) != 2 || roles[0] != "org-y" || roles[1] != "k3s-host.y" {
		t.Fatalf("y must keep the shared identity with its own roles, got %v", roles)
	}
	if _, still := listed("x")["i-shared"]; still {
		t.Fatal("x still lists an identity it removed")
	}

	if code, body := send(t, app, http.MethodDelete, "/v1/network/identities/i-shared", "y", ""); code >= 300 {
		t.Fatalf("y delete want 2xx, got %d (%s)", code, body)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "i-shared" {
		t.Fatalf("the last org's delete must remove the identity, deleted %v", f.deleted)
	}

	// The caller of org x is "sub-x", which is i-self's subject.
	if code, body := send(t, app, http.MethodDelete, "/v1/network/identities/i-self", "x", ""); code >= 300 {
		t.Fatalf("self delete want 2xx, got %d (%s)", code, body)
	}
	if len(f.deleted) != 2 || f.deleted[1] != "i-self" {
		t.Fatalf("a person removing themselves must remove the identity, deleted %v", f.deleted)
	}

	if code, _ := send(t, app, http.MethodDelete, "/v1/network/identities/i-cloud", "admin", ""); code != http.StatusForbidden {
		t.Fatalf("deleting a fabric administrator want 403, got %d", code)
	}
}

// No attested subject, no identity: an anonymous caller and a caller whose
// token named no subject are both refused before the controller is written.
func TestEnsureIdentityRefusesACallerWithNoSubject(t *testing.T) {
	f := &fakeZT{}
	app := mountApp(t, f)
	if code, _ := sendAs(t, app, http.MethodPost, "/v1/network/identities", "", "", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("anonymous ensure want 401, got %d", code)
	}
	if code, _ := sendAs(t, app, http.MethodPost, "/v1/network/identities", "acme", "", `{}`); code != http.StatusForbidden {
		t.Fatalf("subject-less ensure want 403, got %d", code)
	}
	if len(f.created) != 0 {
		t.Fatalf("a refused caller reached the controller: %v", f.created)
	}
}

// The controller refusing the write is the caller's answer: an error with the
// controller's reason, never an identity it did not make.
func TestEnsureIdentityControllerRefuses(t *testing.T) {
	f := &fakeZT{refuse: true}
	app := mountApp(t, f)
	code, body := send(t, app, http.MethodPost, "/v1/network/identities", "acme", `{}`)
	if code < 400 || strings.Contains(string(body), `"externalId"`) {
		t.Fatalf("refused write must fail, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "authPolicyId") {
		t.Fatalf("the refusal must carry the controller's reason, got %s", body)
	}
}

// The list is the tenancy filter, and delete may only act on what the caller
// could list: another org's id is 404 with no write reaching the controller.
func TestIdentityListAndDeleteAreTenantScoped(t *testing.T) {
	f := &fakeZT{identities: []map[string]any{
		{"id": "i-a", "name": "vm.acme", "externalId": "sub-a", "roleAttributes": []string{"org-acme"}},
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
	if got.ID != "i-a" || got.Name != "vm" || got.ExternalID != "sub-a" {
		t.Fatalf("identity row mismatch: %+v", got)
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
	var v publishedView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if v.Name != "k3s" || v.DNS != "k3s.acme.zt" || v.ID == "" {
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
	if addrs, _ := interceptData["addresses"].([]any); len(addrs) != 1 || addrs[0] != "k3s.acme.zt" {
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

// Unpublishing takes off exactly what publishing put on — two policies, the
// service, then its configs — and the "<name>-host" role from the org's
// hosts, leaving a policy that selects the service by attribute and every
// other org alone.
func TestUnpublishTakesOffWhatPublishPutOn(t *testing.T) {
	f := &fakeZT{
		policies: []map[string]any{{"id": "pol-all", "name": "everything", "serviceRoles": []any{"#all"}}},
		identities: []map[string]any{
			{"id": "i-host", "name": "sub-acme", "externalId": "sub-acme",
				"roleAttributes": []string{"org-acme", "k3s-host.acme", "gpu.acme"}},
			{"id": "i-other", "name": "sub-y", "externalId": "sub-y",
				"roleAttributes": []string{"org-y", "k3s-host.y"}},
		},
	}
	app := mountApp(t, f)
	code, body := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s","host":"127.0.0.1","port":6443}`)
	if code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	var v publishedView
	_ = json.Unmarshal(body, &v)

	if code, _ := send(t, app, http.MethodDelete, "/v1/network/services/"+v.ID, "y", ""); code != http.StatusNotFound {
		t.Fatalf("another org's unpublish want 404, got %d", code)
	}
	if code, body := send(t, app, http.MethodDelete, "/v1/network/services/"+v.ID, "acme", ""); code >= 300 {
		t.Fatalf("unpublish want 2xx, got %d (%s)", code, body)
	}
	want := []string{"/service-policies/pol-1", "/service-policies/pol-2", "/services/" + v.ID, "/configs/cfg-1", "/configs/cfg-2"}
	if !slices.Equal(f.removed, want) {
		t.Fatalf("removed %v, want %v", f.removed, want)
	}
	if len(f.policies) != 1 || f.policies[0]["id"] != "pol-all" {
		t.Fatalf("a policy selecting by attribute must stay, left %v", f.policies)
	}
	roles := func(id string) []string {
		i := slices.IndexFunc(f.identities, func(m map[string]any) bool { return m["id"] == id })
		var out []string
		switch r := f.identities[i]["roleAttributes"].(type) {
		case []string:
			out = r
		case []any:
			for _, x := range r {
				out = append(out, x.(string))
			}
		}
		return out
	}
	if got := roles("i-host"); !slices.Equal(got, []string{"org-acme", "gpu.acme"}) {
		t.Fatalf("the host keeps every role but the service's, got %v", got)
	}
	if got := roles("i-other"); !slices.Equal(got, []string{"org-y", "k3s-host.y"}) {
		t.Fatalf("another org's identity must be untouched, got %v", got)
	}
	code, body = send(t, app, http.MethodGet, "/v1/network/services", "acme", "")
	if code != http.StatusOK || strings.Contains(string(body), `"k3s.acme"`) {
		t.Fatalf("an unpublished service must not be listed, got %d (%s)", code, body)
	}
}

// The writes stay fail-closed where the reads degrade: an unconfigured
// deployment answers 503 and fabricates nothing.
func TestWritesFailClosedWhenUnconfigured(t *testing.T) {
	t.Setenv("ZT_CONTROLLER_URL", "https://zt-controller.invalid")
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_CLIENT_SECRET", "")
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
		{http.MethodDelete, "/v1/network/services/svc-1", ""},
	} {
		if code, _ := send(t, app, c.method, c.path, "acme", c.body); code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured %s %s: want 503, got %d", c.method, c.path, code)
		}
	}
}

// A member of an org joins its network, sees its own identity and leaves.
// Everything that changes what the network serves or who else is on it is a
// steward's, and a member asking is refused before the controller is written.
// An org can hold people who are not one team, so hosting and removal stay with
// its stewards.
func TestAMemberJoinsButOnlyAStewardChangesTheNetwork(t *testing.T) {
	f := &fakeZT{identities: []map[string]any{
		{"id": "i-host", "name": "grid.acme", "externalId": "sub-grid",
			"roleAttributes": []string{"org-acme", "engine-host.acme"}},
	}}
	app := mountApp(t, f)
	code, body := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"engine","host":"127.0.0.1","port":1235}`)
	if code != http.StatusCreated {
		t.Fatalf("a steward's publish want 201, got %d (%s)", code, body)
	}
	var svc publishedView
	_ = json.Unmarshal(body, &svc)
	writes := func() int {
		n := len(f.deleted) + len(f.removed)
		for _, w := range f.created {
			n += len(w)
		}
		return n
	}
	before := writes()

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/network/services", `{"name":"k8s","host":"127.0.0.1","port":443}`},
		{http.MethodDelete, "/v1/network/services/" + svc.ID, ""},
		{http.MethodPost, "/v1/network/identities", `{"roles":["engine-host"]}`},
		{http.MethodPost, "/v1/network/identities", `{"roles":["gpu"]}`},
	} {
		if code, body := member(t, app, c.method, c.path, "acme", "sub-me", c.body); code != http.StatusForbidden {
			t.Fatalf("member %s %s %s: want 403, got %d (%s)", c.method, c.path, c.body, code, body)
		}
	}
	// Another member's identity is not a member's to see, so not to remove.
	if code, _ := member(t, app, http.MethodDelete, "/v1/network/identities/i-host", "acme", "sub-me", ""); code != http.StatusNotFound {
		t.Fatalf("member deleting another's identity want 404, got %d", code)
	}
	if n := writes(); n != before {
		t.Fatalf("a refused member reached the controller: %d writes", n-before)
	}

	// Joining with the org role alone is every member's.
	code, body = member(t, app, http.MethodPost, "/v1/network/identities", "acme", "sub-me", `{}`)
	if code != http.StatusCreated {
		t.Fatalf("member join want 201, got %d (%s)", code, body)
	}
	var me identityView
	_ = json.Unmarshal(body, &me)
	list := func(send func(*testing.T, *zip.App, string, string, string, string, string) (int, []byte)) []string {
		t.Helper()
		code, body := send(t, app, http.MethodGet, "/v1/network/identities", "acme", "sub-me", "")
		var l identityList
		if code != http.StatusOK || json.Unmarshal(body, &l) != nil {
			t.Fatalf("list: %d (%s)", code, body)
		}
		var ids []string
		for _, v := range l.Identities {
			ids = append(ids, v.ID)
		}
		slices.Sort(ids)
		return ids
	}
	if got := list(member); !slices.Equal(got, []string{me.ID}) {
		t.Fatalf("a member lists %v, want only itself %v", got, me.ID)
	}
	if got := list(sendAs); !slices.Equal(got, []string{"i-host", me.ID}) {
		t.Fatalf("a steward lists %v, want both", got)
	}
	if code, body := member(t, app, http.MethodDelete, "/v1/network/identities/"+me.ID, "acme", "sub-me", ""); code >= 300 {
		t.Fatalf("a member leaving want 2xx, got %d (%s)", code, body)
	}
	if !slices.Contains(f.deleted, me.ID) {
		t.Fatalf("a member leaving must remove its identity, deleted %v", f.deleted)
	}
}

// The org's own machine client is a steward. It holds no admin bit, but its
// org is its owner and cannot be switched, so it acts for that org alone —
// which is how a cluster's service client hosts what the org published.
func TestAnOrgsMachineClientMayHost(t *testing.T) {
	f := &fakeZT{}
	app := mountApp(t, f)
	if code, body := send(t, app, http.MethodPost, "/v1/network/services", "acme",
		`{"name":"k3s","host":"127.0.0.1","port":6443}`); code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	code, body := call(t, app, http.MethodPost, "/v1/network/identities", "acme", "admin/acme-link",
		`{"roles":["k3s-host"]}`, "X-User-IsApp")
	if code != http.StatusCreated {
		t.Fatalf("machine client host role want 201, got %d (%s)", code, body)
	}
	if roles, _ := f.created["identities"][0]["roleAttributes"].([]any); !slices.Equal(roles, []any{"org-acme", "k3s-host.acme"}) {
		t.Fatalf("roles = %v, want [org-acme k3s-host.acme]", roles)
	}
}

// An application an org admin names after a member logs in to the fabric as the
// `sub` IAM signs for it, `acme/bob` — which is also the subject of bob's API
// key. A key is not a credential the fabric admits, so it holds no identity: it
// can neither make one under that string, nor list or remove the application's,
// and nothing it asks reaches the controller. bob himself, on his own token, is
// his UUID and reaches only his.
func TestAKeyNamedLikeAnApplicationHoldsNoIdentity(t *testing.T) {
	const bob = "4f1c0e8a-0000-4000-8000-00000000b0b0"
	f := &fakeZT{identities: []map[string]any{
		{"id": "i-bob", "name": "bob", "externalId": bob, "roleAttributes": []string{"org-acme"}},
		{"id": "i-app", "name": "acme/bob", "externalId": "acme/bob", "roleAttributes": []string{"org-acme"}},
	}}
	app := mountApp(t, f)
	asKey := func(method, path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "acme/bob")
		req.Header.Set("X-Test-Subject", "acme/bob")
		req.Header.Set("X-Test-Kind", "key")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/network/identities", `{}`},
		{http.MethodGet, "/v1/network/identities", ""},
		{http.MethodDelete, "/v1/network/identities/i-app", ""},
	} {
		if code, body := asKey(c.method, c.path, c.body); code != http.StatusForbidden {
			t.Fatalf("bob's key %s %s: want 403, got %d (%s)", c.method, c.path, code, body)
		}
	}
	if len(f.created) != 0 || len(f.deleted) != 0 {
		t.Fatalf("a key reached the controller: created %v, deleted %v", f.created, f.deleted)
	}

	// bob on his own token sees his identity and not the application's.
	code, body := member(t, app, http.MethodGet, "/v1/network/identities", "acme", bob, "")
	var l identityList
	if code != http.StatusOK || json.Unmarshal(body, &l) != nil {
		t.Fatalf("bob's list: %d (%s)", code, body)
	}
	if len(l.Identities) != 1 || l.Identities[0].ID != "i-bob" {
		t.Fatalf("bob lists %+v, want only his own", l.Identities)
	}
	// The application logs in as its own `sub`, and ensuring answers that one.
	code, body = call(t, app, http.MethodPost, "/v1/network/identities", "acme", "acme/bob", `{}`, "X-User-IsApp")
	var v identityView
	if code != http.StatusCreated || json.Unmarshal(body, &v) != nil || v.ID != "i-app" {
		t.Fatalf("the application's ensure: %d (%s), want its own identity", code, body)
	}
}
