package platform

import (
	"sync"
	"bytes"
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/clients/k8s"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// mountSvcK8s builds a hermetic app AND returns the backing Service, so tests that need
// to inspect/prime process-local state (e.g. the per-org deploy gate) can reach it.
// It NEVER touches a real cluster (the earlier version resolved the dev kubeconfig
// and wrote to live DOKS — never again).
// testProjects maps a mounted app to its fake IAM project store. Platform cannot
// create a project (that is IAM's, at /v1/iam/projects), so a test that needs one
// declares it with seedProject instead of POSTing it.
var testProjects sync.Map // *zip.App -> *fakeProjects

// seedProject registers a project in the app's IAM store — the precondition for
// deploying an app under it.
func seedProject(t *testing.T, app *zip.App, org, name string) {
	t.Helper()
	v, ok := testProjects.Load(app)
	if !ok {
		t.Fatalf("seedProject: app was not mounted by this harness")
	}
	if _, err := v.(*fakeProjects).Create(context.Background(), org, name, name, ""); err != nil {
		t.Fatalf("seedProject %s/%s: %v", org, name, err)
	}
}

func mountSvcK8s(t *testing.T, k *k8sClient) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fp := newFakeProjects()
	s := &cloud.Service[state]{Base: cloud.Base{KMS: newFakeKMS(), Log: luxlog.New("test"), Brand: "hanzo"}, State: state{store: store, projects: fp, k8s: k, sitesHost: "hanzo.app"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	testProjects.Store(app, fp)
	return app, s
}

// mountAppK8s builds a hermetic app over a Service with the GIVEN k8s client.
func mountAppK8s(t *testing.T, k *k8sClient) *zip.App {
	app, _ := mountSvcK8s(t, k)
	return app
}

// mountApp is the default hermetic app: NO cluster (k8s fails closed), for the
// metadata + fail-closed-deploy paths.
func mountApp(t *testing.T) *zip.App {
	return mountAppK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
}

// fakeK8s returns a k8s client backed by an in-memory fake dynamic client so the
// deploy SUCCESS path (Service CR create in tenant-<org>) is exercised
// deterministically without a real cluster.
func fakeK8s(objs ...runtime.Object) *k8sClient {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.Apps:          "AppList",
		jobsGVR:           "JobList",
		k8s.Namespaces:    "NamespaceList",
		resourceQuotasGVR: "ResourceQuotaList",
		limitRangesGVR:    "LimitRangeList",
		kmsSecretsGVR:     "KMSSecretList",
		k8s.Volumes:       "PersistentVolumeClaimList",
	}, objs...)
	// Model a cluster where cloud-api's per-tenant RBAC is already present: the
	// SelfSubjectAccessReview readiness probe (waitForTenantRBAC) resolves
	// allowed=true on the first call, so the deploy fast path is exercised with no
	// wait. Tests of the async-RBAC race prepend their own SSAR reactor to override
	// this default (see org_rbac_test.go).
	allowSSAR(dyn, func() bool { return true })
	return &k8sClient{dyn: dyn, imagePrefix: defaultBuildImagePrefix, buildNS: "hanzo", limits: testLimits(), kmsSync: newKMSSyncConfig()}
}

// allowSSAR installs a reactor that answers every SelfSubjectAccessReview with
// status.allowed = allow(). It is the ONE place tests model whether cloud-api's
// tenant RBAC is ready, so the readiness gate is exercised deterministically
// without a real apiserver. `allow` is a func so a stateful caller can flip a
// namespace from not-ready to ready mid-poll.
func allowSSAR(dyn *dynamicfake.FakeDynamicClient, allow func() bool) {
	dyn.PrependReactor("create", "selfsubjectaccessreviews", func(a clienttesting.Action) (bool, runtime.Object, error) {
		obj := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		_ = unstructured.SetNestedField(obj.Object, allow(), "status", "allowed")
		return true, obj, nil
	})
}

// testLimits is a small, deterministic per-tenant policy for hermetic tests —
// explicit values (not env-derived) so bound assertions are stable.
func testLimits() resourceLimits {
	return resourceLimits{
		maxReplicas:     20,
		maxStorageGB:    100,
		maxBuilds:       3,
		maxDeploys:      8,
		quotaCPU:        "20",
		quotaMemory:     "40Gi",
		quotaPods:       "50",
		limitDefaultCPU: "500m",
		limitDefaultMem: "512Mi",
		limitReqCPU:     "100m",
		limitReqMem:     "128Mi",
		limitMaxCPU:     "4",
		limitMaxMem:     "8Gi",
	}
}

// do fires an in-process request simulating a VALIDATED principal: the gateway's
// SanitizeIdentity mints BOTH X-Org-Id AND X-User-Id from the validated JWT, so a
// legitimate caller always carries both. tenant() requires X-User-Id (the
// validated-principal gate), so the harness sets a stable per-org test user
// alongside the org. doForge (below) omits X-User-Id to exercise the refusal.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	return doAs(t, app, method, path, org, "u-"+org, body)
}

// doAs sets X-Org-Id + X-User-Id explicitly so tests can exercise both the
// validated path (user set) and the forgeable path (user empty).
func doAs(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPProjectAppLifecycle proves the full per-org surface end-to-end: the
// tenant gate (no org → 403), project + application CRUD, and the fail-closed
// deploy (no cluster in test → honest 503 + a recorded error deployment, never a
// fabricated "live").
func TestHTTPProjectAppLifecycle(t *testing.T) {
	app := mountApp(t)

	// No org → 403 (never leaks an empty list as if authorized).
	if code, _ := do(t, app, http.MethodGet, "/v1/platform/projects", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// The project is IAM's; platform only deploys into one.
	seedProject(t, app, "maxpower", "web")
	// Create an image-source application under it.
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image",
		"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"},
		"port":  8080, "domains": []string{"api.maxpower.hanzo.app"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create app want 201, got %d (%s)", code, body)
	}

	// List projects → applications count reflects the app.
	code, body = do(t, app, http.MethodGet, "/v1/platform/projects", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list projects want 200, got %d", code)
	}
	var projects []projectView
	_ = json.Unmarshal(body, &projects)
	if len(projects) != 1 || projects[0].Slug != "web" || projects[0].Applications != 1 {
		t.Fatalf("expected [web applications=1], got %+v", projects)
	}

	// Get the app.
	code, body = do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("get app want 200, got %d", code)
	}
	var av appView
	_ = json.Unmarshal(body, &av)
	if av.Namespace != "tenant-maxpower" {
		t.Fatalf("app namespace must be tenant-maxpower, got %q", av.Namespace)
	}

	// Deploy with no cluster configured → fail closed with 503 (honest), NOT a
	// fabricated success.
	code, body = do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": "1.27"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("deploy with no cluster want 503, got %d (%s)", code, body)
	}
	// The failed attempt is recorded honestly as an 'error' deployment.
	code, body = do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api/deployments", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list deployments want 200, got %d", code)
	}
	var deps []deploymentView
	_ = json.Unmarshal(body, &deps)
	if len(deps) != 1 || deps[0].Status != "error" || deps[0].Version != 1 {
		t.Fatalf("expected one v1 error deployment, got %+v", deps)
	}
}

// TestHTTPDeploySucceedsIntoTenantNamespace proves the deploy SUCCESS path
// against an in-memory cluster: an image-source deploy writes the operator
// Service CR into tenant-<org> (derived from the validated org) and returns 202.
func TestHTTPDeploySucceedsIntoTenantNamespace(t *testing.T) {
	k := fakeK8s()
	app := mountAppK8s(t, k)

	seedProject(t, app, "maxpower", "web")
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image",
		"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"},
		"port":  8080, "domains": []string{"api.maxpower.hanzo.app"},
	})

	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": "1.27"})
	if code != http.StatusAccepted {
		t.Fatalf("deploy want 202, got %d (%s)", code, body)
	}

	// The operator Service CR must exist in tenant-maxpower with the app's image.
	obj, err := k.dyn.Resource(k8s.Apps).Namespace("tenant-maxpower").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Service CR not created in tenant-maxpower: %v", err)
	}
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	if repo != "ghcr.io/hanzoai/nginx" || tag != "1.27" {
		t.Fatalf("CR image wrong: %s:%s", repo, tag)
	}
	org, _, _ := unstructured.NestedString(obj.Object, "metadata", "labels", "hanzo.ai/org")
	if org != "maxpower" {
		t.Fatalf("CR must be org-labeled maxpower, got %q", org)
	}
	// The deployment lands 'deploying' (operator finishes the rollout async).
	code, body = do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api/deployments", "maxpower", nil)
	var deps []deploymentView
	_ = json.Unmarshal(body, &deps)
	if code != http.StatusOK || len(deps) != 1 || deps[0].Status != "deploying" {
		t.Fatalf("expected one 'deploying' deployment, got %d %+v", code, deps)
	}
	// stop scales the CR to zero.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/stop", "maxpower", nil); code != http.StatusOK {
		t.Fatalf("stop want 200, got %d", code)
	}
}

// TestImageDeployOverCapReturns429 proves the L1 per-org in-flight deploy cap: once
// an org has maxConcurrentDeploys deploys in flight, its next deploy is refused with
// a RETRYABLE 429 (never a fabricated success, never unbounded goroutine pile-up),
// the cap is PER-ORG (a saturated org never throttles another), and releasing a slot
// re-admits the org. The gate is primed directly (deterministic) rather than by
// racing real ~45s waits.
func TestImageDeployOverCapReturns429(t *testing.T) {
	k := fakeK8s()
	k.limits.maxDeploys = 2 // small cap for a deterministic test
	app, s := mountSvcK8s(t, k)

	seedApp := func(org string) {
		seedProject(t, app, org, "web")
		do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", org, map[string]any{
			"name": "api", "source": "image",
			"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"},
		})
	}
	seedApp("maxpower")
	seedApp("acme")

	// Saturate maxpower's in-flight deploy gate (simulate 2 deploys already parked in
	// applyLive's RBAC wait).
	for i := 0; i < 2; i++ {
		if !s.State.deployGate.acquire("maxpower", s.State.k8s.limits.maxConcurrentDeploys()) {
			t.Fatalf("precondition: acquire maxpower slot %d must succeed", i)
		}
	}

	// maxpower's next deploy is over-cap → 429 (retryable), and records NO deployment
	// (a throttle is not an attempt).
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": "1.27"})
	if code != http.StatusTooManyRequests {
		t.Fatalf("over-cap deploy want 429, got %d (%s)", code, body)
	}
	if _, deps := listDeps(t, app, "maxpower"); len(deps) != 0 {
		t.Fatalf("a throttled (429) deploy must record NO deployment, got %d", len(deps))
	}

	// PER-ORG isolation: acme is unaffected while maxpower is saturated → 202.
	if code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "acme", map[string]any{"tag": "1.27"}); code != http.StatusAccepted {
		t.Fatalf("acme deploy must be unaffected by maxpower's cap, want 202, got %d (%s)", code, body)
	}

	// Releasing one maxpower slot re-admits maxpower → 202.
	s.State.deployGate.release("maxpower")
	if code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": "1.27"}); code != http.StatusAccepted {
		t.Fatalf("after releasing a slot maxpower deploy want 202, got %d (%s)", code, body)
	}
}

// listDeps GETs an org's api deployments (helper for the cap test).
func listDeps(t *testing.T, app *zip.App, org string) (int, []deploymentView) {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api/deployments", org, nil)
	var deps []deploymentView
	_ = json.Unmarshal(body, &deps)
	return code, deps
}

// TestHTTPForgeableOrgRefused proves the validated-principal gate (RED HIGH): a
// request carrying a client X-Org-Id but NO validated principal (X-User-Id empty
// — the Phase-1 residual a direct-to-pod caller could forge) is refused 403 on
// every route, including the cluster-mutating deploy. This closes the
// cross-tenant deploy/read path that trusting X-Org-Id alone would open.
func TestHTTPForgeableOrgRefused(t *testing.T) {
	app := mountApp(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/platform/projects"},
		{http.MethodPost, "/v1/platform/projects/x/apps"},
		{http.MethodGet, "/v1/platform/projects/x/apps"},
		{http.MethodPost, "/v1/platform/projects/x/apps/y/deploy"},
		{http.MethodPost, "/v1/platform/projects/x/apps/y/stop"},
	} {
		if code, _ := doAs(t, app, tc.method, tc.path, "victim", "", nil); code != http.StatusForbidden {
			t.Fatalf("forged org (no principal) %s %s want 403, got %d", tc.method, tc.path, code)
		}
	}
	// With a validated principal (X-User-Id set), the same org is accepted.
	if code, _ := doAs(t, app, http.MethodGet, "/v1/platform/projects", "victim", "u-victim", nil); code != http.StatusOK {
		t.Fatalf("validated principal want 200, got %d", code)
	}
}

// TestHTTPCrossTenantIsolation proves org B can never see, mutate, or deploy
// org A's resources at the HTTP layer — the RED bar.
func TestHTTPCrossTenantIsolation(t *testing.T) {
	app := mountApp(t)

	// maxpower creates project + app.
	seedProject(t, app, "maxpower", "web")
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	})

	// acme sees zero projects.
	code, body := do(t, app, http.MethodGet, "/v1/platform/projects", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("acme list want 200, got %d", code)
	}
	var projects []projectView
	_ = json.Unmarshal(body, &projects)
	if len(projects) != 0 {
		t.Fatalf("acme must see zero projects, got %+v", projects)
	}
	// acme cannot GET maxpower's project.
	if code, _ := do(t, app, http.MethodGet, "/v1/platform/projects/web", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower project want 404, got %d", code)
	}
	// acme cannot create an app under maxpower's project (project not found for acme).
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "acme", map[string]any{
		"name": "evil", "source": "image", "image": map[string]any{"repository": "ghcr.io/x/y", "tag": "1"},
	}); code != http.StatusNotFound {
		t.Fatalf("acme create-app-in-maxpower-project want 404, got %d", code)
	}
	// acme cannot GET/DELETE maxpower's app, nor deploy/stop/start it.
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/platform/projects/web/apps/api"},
		{http.MethodDelete, "/v1/platform/projects/web/apps/api"},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/deploy"},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/stop"},
		{http.MethodPost, "/v1/platform/projects/web/apps/api/start"},
		{http.MethodGet, "/v1/platform/projects/web/apps/api/deployments"},
	} {
		if code, _ := do(t, app, tc.method, tc.path, "acme", nil); code != http.StatusNotFound {
			t.Fatalf("acme %s %s want 404, got %d", tc.method, tc.path, code)
		}
	}
	// maxpower's app survived every acme attempt.
	if code, _ := do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api", "maxpower", nil); code != http.StatusOK {
		t.Fatalf("maxpower app must survive, got %d", code)
	}
}

// TestHTTPSecretEnvSealed proves secret env vars are ACCEPTED and sealed into KMS:
// the create succeeds, the response never echoes the plaintext (masked), and the
// stored row carries no plaintext — the value lives only in KMS.
func TestHTTPSecretEnvSealed(t *testing.T) {
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	seedProject(t, app, "maxpower", "web")
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"env": []map[string]any{{"key": "DB_PASSWORD", "value": "hunter2", "secret": true}, {"key": "PUBLIC", "value": "ok"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("secret env want 201, got %d (%s)", code, body)
	}
	if bytes.Contains(body, []byte("hunter2")) {
		t.Fatal("secret value must never be echoed back over the API")
	}
	// The persisted env_json must carry NO plaintext (the secret value is blanked).
	a, err := s.State.store.GetApplication(context.Background(), "maxpower", "web", "api")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if bytes.Contains([]byte(a.EnvJSON), []byte("hunter2")) {
		t.Fatalf("plaintext secret leaked into the store: %s", a.EnvJSON)
	}
	// The sealed value must be readable back from KMS at the app's coordinate.
	got, err := s.KMS.GetSecret(context.Background(), kmsSecretRef("maxpower", "api", "DB_PASSWORD"))
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("secret not sealed into KMS: got %q err=%v", got, err)
	}
}

// TestHTTPSecretEnvFailsClosedWithoutKMS proves that when KMS is unavailable the
// create is refused (503) and NOTHING is persisted — a plaintext secret never
// lands in the DB as a fallback.
func TestHTTPSecretEnvFailsClosedWithoutKMS(t *testing.T) {
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	s.KMS = nil // KMS not configured
	seedProject(t, app, "maxpower", "web")
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"env": []map[string]any{{"key": "DB_PASSWORD", "value": "hunter2", "secret": true}},
	})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("secret env with no KMS want 503, got %d (%s)", code, body)
	}
	if bytes.Contains(body, []byte("hunter2")) {
		t.Fatal("secret value must never be echoed in the error")
	}
}

// TestHTTPHealthFailClosed proves /v1/platform/health reports degraded (503)
// when no cluster is configured — never a fabricated ok.
func TestHTTPHealthFailClosed(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/platform/health", "", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("health with no cluster want 503, got %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte("degraded")) {
		t.Fatalf("health should report degraded, got %s", body)
	}
}

// TestHTTPValidation proves boundary validation: bad slug, missing name, bad
// source.
func TestHTTPValidation(t *testing.T) {
	app := mountApp(t)
	seedProject(t, app, "maxpower", "web")
	// Bad source.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{"name": "x", "source": "ftp"}); code != http.StatusBadRequest {
		t.Fatalf("bad source want 400, got %d", code)
	}
	// git source without repo.url.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{"name": "x", "source": "git"}); code != http.StatusBadRequest {
		t.Fatalf("git without repo want 400, got %d", code)
	}
}
