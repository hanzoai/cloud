package platform

// The wire, route by route.
//
// Every route on this surface used to be a raw handler that wrote its own status
// and marshalled its own map. Each is now a typed op whose In is decoded for it and
// whose Out is marshalled for it, and the whole value of the conversion depends on
// those two producing the SAME bytes at the SAME code — a caller cannot be asked to
// notice that the server changed how it builds its answers.
//
// So this walks the surface once per route and asserts the two facts a client can
// observe: the status, and the shape. Shape is asserted as the set of JSON keys at
// the top level (or of the first element, for the routes that serve a bare array),
// because that is what a client's decoder binds — a renamed or dropped key breaks
// it, a reordered one does not.
//
// The expectations are written from the ORIGINAL handlers, not from the new ones:
// each was read off the c.JSON(status, …) call the route made before conversion.
// That is what makes this a regression test rather than a mirror.

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// keysOf returns the top-level JSON keys of an object, or of the first element of
// an array. An empty array has no shape to state and yields nil, which the table
// spells as an empty want.
func keysOf(t *testing.T, body []byte) []string {
	t.Helper()
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err == nil {
		return slices.Sorted(maps.Keys(obj))
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		t.Fatalf("body is neither an object nor an array of objects: %s", trimmed)
	}
	if len(arr) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(arr[0]))
}

// TestConvertedRoutesAnswerAsBefore is the read half: every GET the surface serves
// for an org with one deployed application, at the status and shape it has always
// answered with.
func TestConvertedRoutesAnswerAsBefore(t *testing.T) {
	app := mountAppK8s(t, fakeK8s())
	seedProject(t, app, "acme", "web")

	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "acme", map[string]any{
		"name": "api", "source": "image",
		"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"},
		"port":  8080,
	})
	if code != http.StatusCreated {
		t.Fatalf("create app: %d (%s)", code, body)
	}
	var created appView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create app body: %v", err)
	}
	// A create answers 201 with the application — the status the raw handler wrote
	// by hand and the typed op now declares.
	if got, want := keysOf(t, body), "buildType,createdAt,domains,env,environment,id,image,name,namespace,org,port,projectId,replicas,repo,slug,source,status,updatedAt"; strings.Join(got, ",") != want {
		t.Errorf("POST apps shape =\n  %s\nwant\n  %s", strings.Join(got, ","), want)
	}

	if code, body = do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "acme", map[string]any{"tag": "1.27"}); code != http.StatusAccepted {
		t.Fatalf("deploy: %d (%s)", code, body)
	}
	var dep deploymentView
	_ = json.Unmarshal(body, &dep)
	if dep.ID == "" {
		t.Fatalf("deploy answered no deployment id: %s", body)
	}

	for _, tc := range []struct {
		name   string
		path   string
		status int
		keys   string // comma-joined, sorted; of the object, or of an array's first element
	}{
		{"projects", "/v1/platform/projects", http.StatusOK, "applications,createdAt,name,org,slug"},
		{"project", "/v1/platform/projects/web", http.StatusOK, "applications,createdAt,name,org,slug"},
		{"apps", "/v1/platform/projects/web/apps", http.StatusOK, "buildType,createdAt,currentDeploymentId,domains,env,environment,id,image,name,namespace,org,port,projectId,replicas,repo,slug,source,status,updatedAt"},
		{"app", "/v1/platform/projects/web/apps/api", http.StatusOK, "buildType,createdAt,currentDeploymentId,domains,env,environment,id,image,name,namespace,org,port,projectId,replicas,repo,slug,source,status,updatedAt"},
		{"deployments", "/v1/platform/projects/web/apps/api/deployments", http.StatusOK, "applicationId,createdAt,id,image,org,source,status,updatedAt,version"},
		{"deployment", "/v1/platform/projects/web/apps/api/deployments/" + dep.ID, http.StatusOK, "applicationId,createdAt,id,image,org,source,status,updatedAt,version"},
		{"logs", "/v1/platform/projects/web/apps/api/deployments/" + dep.ID + "/logs", http.StatusOK, "deploymentId,logs,source"},
		{"domains", "/v1/platform/projects/web/apps/api/domains", http.StatusOK, "host,kind,primary,status,url,verified"},
		{"environments", "/v1/environments", http.StatusOK, "environments"},
		{"pipelines", "/v1/pipelines", http.StatusOK, "pipelines"},
		{"builds", "/v1/builds", http.StatusOK, "builds"},
		{"releases", "/v1/releases", http.StatusOK, "releases"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do(t, app, http.MethodGet, tc.path, "acme", nil)
			if code != tc.status {
				t.Fatalf("GET %s = %d, want %d (%s)", tc.path, code, tc.status, body)
			}
			if got := strings.Join(keysOf(t, body), ","); got != tc.keys {
				t.Errorf("GET %s shape =\n  %s\nwant\n  %s", tc.path, got, tc.keys)
			}
		})
	}
}

// TestConvertedRoutesRefuseAsBefore is the gate half. Every one of these answered
// 403 as a raw handler reading the request directly; each now resolves its org
// through cloud.Bridge instead, and the refusal must be the same code and the same
// sentence. A conversion that quietly turned a 403 into a 500 — or into a 200 over
// an empty tenant — is the failure this closes.
func TestConvertedRoutesRefuseAsBefore(t *testing.T) {
	app := mountApp(t)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/platform/projects", nil},
		{http.MethodGet, "/v1/platform/projects/web", nil},
		{http.MethodGet, "/v1/platform/projects/web/apps", nil},
		{http.MethodPost, "/v1/platform/projects/web/apps", map[string]any{"name": "x", "source": "image"}},
		{http.MethodGet, "/v1/platform/projects/web/apps/api", nil},
		{http.MethodDelete, "/v1/platform/projects/web/apps/api", nil},
		{http.MethodPut, "/v1/platform/projects/web/apps/api/env", map[string]any{"env": []any{}}},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", map[string]any{}},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/stop", nil},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/start", nil},
		{http.MethodGet, "/v1/platform/projects/web/apps/api/deployments", nil},
		{http.MethodGet, "/v1/platform/projects/web/apps/api/deployments/d1", nil},
		{http.MethodGet, "/v1/platform/projects/web/apps/api/deployments/d1/logs", nil},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/preview", map[string]any{"branch": "b", "image": "x:1"}},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/promote", map[string]any{"tag": "1"}},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/rollback", map[string]any{}},
		{http.MethodGet, "/v1/platform/projects/web/apps/api/domains", nil},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/domains", map[string]any{"host": "a.example.com"}},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/domains/a.example.com/verify", nil},
		{http.MethodDelete, "/v1/platform/projects/web/apps/api/domains/a.example.com", nil},
		{http.MethodPost, "/v1/run", map[string]any{"name": "x", "image": "ghcr.io/hanzoai/nginx:1"}},
		{http.MethodGet, "/v1/environments", nil},
		{http.MethodGet, "/v1/pipelines", nil},
		{http.MethodGet, "/v1/builds", nil},
		{http.MethodGet, "/v1/releases", nil},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// No X-User-Id: a restored client header with no validated principal, the
			// forgeable path the tenant gate exists to refuse.
			code, body := doAs(t, app, tc.method, tc.path, "acme", "", tc.body)
			if code != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403 (%s)", tc.method, tc.path, code, body)
			}
			if !strings.Contains(string(body), "X-Org-Id required") {
				t.Errorf("%s %s refusal = %s, want the tenant gate's own sentence", tc.method, tc.path, body)
			}
		})
	}
}

// TestSelfReleaseRoutesRefuseWithoutSuperAdmin covers the two release reads, whose
// gate is cloud.Super rather than the tenant gate — a different rule, so a
// different sentence, and both must survive the move from the wrapper to the first
// line of the op.
func TestSelfReleaseRoutesRefuseWithoutSuperAdmin(t *testing.T) {
	app := mountApp(t)
	for _, path := range []string{"/v1/runner/releases", "/v1/runner/releases/bld_x"} {
		code, body := do(t, app, http.MethodGet, path, "acme", nil)
		if code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403 (%s)", path, code, body)
		}
		if !strings.Contains(string(body), "SuperAdmin") {
			t.Errorf("GET %s refusal = %s, want the release gate's own sentence", path, body)
		}
	}
}

// TestHealthAnswersItsOwnStatus pins the probe's two outcomes. It is the one route
// that answers a FAILURE with its own typed body rather than the error envelope, so
// the status rides the value (readiness.StatusCode) and the op declares both codes.
// A conversion that let 503 collapse to 200 would report a control plane that can
// deploy when it cannot.
func TestHealthAnswersItsOwnStatus(t *testing.T) {
	// No cluster: degraded, 503, and the real reason on the body.
	code, body := do(t, mountApp(t), http.MethodGet, "/v1/platform/health", "acme", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("health with no cluster = %d, want 503 (%s)", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("health body: %v", err)
	}
	if got["service"] != "platform" || got["status"] != "degraded" || got["k8s"] != false {
		t.Errorf("degraded health = %v, want service=platform status=degraded k8s=false", got)
	}
	if got["error"] == nil || got["error"] == "" {
		t.Errorf("degraded health states no reason: %v", got)
	}
	if _, has := got["crd"]; has {
		t.Errorf("degraded health published crd, which it cannot know without a client: %v", got)
	}

	// A reachable cluster: ok, 200, and crd present and true.
	code, body = do(t, mountAppK8s(t, fakeK8s()), http.MethodGet, "/v1/platform/health", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("health with a cluster = %d, want 200 (%s)", code, body)
	}
	got = nil
	_ = json.Unmarshal(body, &got)
	if got["status"] != "ok" || got["k8s"] != true || got["crd"] != true {
		t.Errorf("healthy = %v, want status=ok k8s=true crd=true", got)
	}
	if _, has := got["error"]; has {
		t.Errorf("healthy published an error key: %v", got)
	}
}

// TestAddDomainStatesWhichOutcomeItWas pins the one route with two success codes.
// A fresh claim is 201 and an idempotent re-add is 200; the ANSWER states which
// (domainView.StatusCode) and the op declares both, so the document publishes what
// the route sends instead of a 200 it does not always send.
func TestAddDomainStatesWhichOutcomeItWas(t *testing.T) {
	app := mountAppK8s(t, fakeK8s())
	seedProject(t, app, "acme", "web")
	if code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "acme", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx"},
	}); code != http.StatusCreated {
		t.Fatalf("create app: %d (%s)", code, body)
	}

	// An org-subtree host is structurally owned, so it goes active at 201.
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/domains", "acme",
		map[string]any{"host": "extra.acme.hanzo.app"})
	if code != http.StatusCreated {
		t.Fatalf("attach subtree host = %d, want 201 (%s)", code, body)
	}
	var v domainView
	_ = json.Unmarshal(body, &v)
	if v.Host != "extra.acme.hanzo.app" || v.Kind != "subtree" || !v.Verified {
		t.Errorf("subtree attach = %+v, want the host active and verified", v)
	}
	// The private outcome flag never reaches the wire.
	if strings.Contains(string(body), "created") {
		t.Errorf("the 201/200 discriminator leaked onto the wire: %s", body)
	}
}
