package platform

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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── the contract's addresses exist, exactly as published ─────────────────────

// Every path the API contract names must be REGISTERED at that literal spelling.
// A 404 here means a generated SDK — and the platform front — calls an address
// the fleet routes nowhere, which is the defect manifest/apps.go exists to catch
// one layer up and this catches at the source.
func TestDeliveryRoutesAreRegisteredAtTheirPublishedPaths(t *testing.T) {
	app := mountDelivery(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/platform/apps"},
		{http.MethodGet, "/v1/platform/apps"},
		{http.MethodGet, "/v1/platform/apps/web"},
		{http.MethodGet, "/v1/platform/apps/web/cd"},
		{http.MethodGet, "/v1/platform/cd"},
		{http.MethodGet, "/v1/platform/ci"},
	} {
		code, body := doAdmin(t, app, tc.method, tc.path, "acme", nil)
		if code == http.StatusNotFound && strings.Contains(string(body), "Cannot") {
			t.Errorf("%s %s is not registered: %s", tc.method, tc.path, body)
		}
	}
}

// ── the gate ─────────────────────────────────────────────────────────────────

// Every route on this surface is cloud.Admin: it declares deployments. A
// validated member with no admin scope is refused at the route, before any
// handler observes the request.
func TestDeliveryRefusesANonAdmin(t *testing.T) {
	app := mountDelivery(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/platform/apps"},
		{http.MethodGet, "/v1/platform/apps"},
		{http.MethodGet, "/v1/platform/cd"},
		{http.MethodGet, "/v1/platform/ci"},
	} {
		if code, _ := do(t, app, tc.method, tc.path, "acme", nil); code != http.StatusForbidden {
			t.Errorf("%s %s admitted a non-admin: %d", tc.method, tc.path, code)
		}
	}
	// And an anonymous caller, at every scope.
	if code, _ := doAs(t, app, http.MethodGet, "/v1/platform/apps", "acme", "", nil); code != http.StatusForbidden {
		t.Errorf("an unvalidated caller was admitted: %d", code)
	}
}

// An org is its name, so reaching the platform's own directories is reaching
// another org — and reaching a RESERVED one is reaching the namespace family
// whose fence admits every namespace and cluster-scoped RBAC. Both must be
// REFUSED, never quietly downgraded to the caller's own org: a silent
// substitution makes an escape attempt indistinguishable from a normal request
// in the logs and in the response.
func TestReservedAndForeignOrgsAreRefusedForAnOrgAdmin(t *testing.T) {
	app := mountDelivery(t)
	for _, org := range []string{"hanzo", "hanzo-cd", "kube-system", "admin", "zen", "globex"} {
		code, body := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
			"repo": "https://github.com/acme/web", "org": org,
		})
		if code != http.StatusForbidden {
			t.Errorf("an org admin reached org %q: %d %s", org, code, body)
			continue
		}
		if !strings.Contains(string(body), "SuperAdmin") {
			t.Errorf("the refusal for %q must name what is required: %s", org, body)
		}
		// The read side too: a board must not read another fence's inventory.
		if code, _ := doAdmin(t, app, http.MethodGet, "/v1/platform/apps?org="+org, "acme", nil); code != http.StatusForbidden {
			t.Errorf("an org admin read org %q's inventory: %d", org, code)
		}
	}
}

// A caller whose OWN org is reserved is still refused. An IAM org named
// `kube-system` does not thereby own Kubernetes — and with the `tenant-` prefix
// gone, this refusal is the whole of what keeps those two name spaces apart.
func TestAnOrgAdminWhoseOwnOrgIsReservedIsRefused(t *testing.T) {
	app := mountDelivery(t)
	for _, org := range []string{"kube-system", "hanzo", "hanzo-cd", "default", "admin"} {
		code, body := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", org, map[string]any{
			"repo": "https://github.com/acme/web",
		})
		if code != http.StatusForbidden {
			t.Errorf("an org admin OF %q declared into it: %d %s", org, code, body)
		}
	}
}

// A caller may not name the image its declaration pulls, and there is no field
// to try it in — so the refusal is structural. This pins that: an `image` (or
// `repository`, or `namespace`) key in the body is IGNORED, never honoured.
func TestTheBodyCannotNameAnImageOrANamespace(t *testing.T) {
	var req declareReq
	err := json.Unmarshal([]byte(`{"repo":"https://github.com/acme/web",
	  "image":"ghcr.io/hanzoai/cloud:v1","repository":"ghcr.io/evil/x",
	  "namespace":"hanzo","project":"hanzo-platform","path":"../../etc"}`), &req)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if req.Repo != "https://github.com/acme/web" {
		t.Fatalf("repo did not bind: %+v", req)
	}
	// The struct has no field for any of them, so nothing carried through.
	b, _ := json.Marshal(req)
	for _, forbidden := range []string{"image", "repository", "namespace", "project", "path"} {
		if strings.Contains(string(b), `"`+forbidden+`"`) {
			t.Errorf("declareReq carries a %q field; placement and the image must be derived", forbidden)
		}
	}
}

// A body that names neither a repository to build nor a tag to declare names no
// image at all, and is refused rather than declaring something empty.
func TestDeclareRefusesABodyThatNamesNoImage(t *testing.T) {
	app := mountDelivery(t)
	code, body := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{"name": "web"})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400 for a body naming no image, got %d %s", code, body)
	}
}

// ── honest absence ───────────────────────────────────────────────────────────

// ci is declared and not implemented, and says so. An empty run list would be
// indistinguishable from a forge with no runs — the estate's own worst bug shape.
func TestCIAnswersNotImplementedRatherThanAnEmptyList(t *testing.T) {
	app := mountDelivery(t)
	code, body := doAdmin(t, app, http.MethodGet, "/v1/platform/ci", "acme", nil)
	if code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d %s", code, body)
	}
	if strings.Contains(string(body), `"runs":[]`) {
		t.Error("ci fabricated an empty run list")
	}
}

// An unobservable delivery plane must be 503 and say why. "CD has nothing" and
// "CD could not be asked" are opposite facts; a board that renders them alike
// reports a healthy empty fleet during an outage.
func TestCDIsUnavailableNotEmptyWithNoCluster(t *testing.T) {
	app := mountDelivery(t)
	code, body := doAdmin(t, app, http.MethodGet, "/v1/platform/cd", "acme", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 with no cluster client, got %d %s", code, body)
	}
	if strings.Contains(string(body), `"applications":[]`) {
		t.Error("an unreadable delivery plane rendered as an empty one")
	}
}

// ── the CD projection ────────────────────────────────────────────────────────

// Every read is total: an Application CD has never reconciled has no status at
// all, and that must project as empty fields rather than panicking or guessing.
func TestObserveCDAppIsTotal(t *testing.T) {
	bare := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "acme-web", "namespace": cdNamespace},
	}}
	got := observeCDApp(bare)
	if got.Name != "acme-web" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Sync != "" || got.Health != "" || got.Automated || got.SelfHeal {
		t.Fatalf("an unreconciled Application projected non-zero facts: %+v", got)
	}
}

// automated is a BLOCK, not a boolean: its presence is the switch. An
// Application with no automation must never report selfHeal true.
func TestObserveCDAppReadsAutomationAsABlock(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "hanzo-papers"},
		"spec": map[string]any{
			"project":     "hanzo-platform",
			"destination": map[string]any{"namespace": "hanzo"},
			"source": map[string]any{
				"path": "charts/app",
				"helm": map[string]any{"valueFiles": []any{"values/hanzo/papers.yaml"}},
			},
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": false, "selfHeal": true}},
		},
		"status": map[string]any{
			"sync":           map[string]any{"status": "Synced", "revision": "abc123"},
			"health":         map[string]any{"status": "Healthy"},
			"reconciledAt":   "2026-08-06T00:00:00Z",
			"operationState": map[string]any{"phase": "Succeeded"},
		},
	}}
	got := observeCDApp(cr)
	if !got.Automated || !got.SelfHeal {
		t.Errorf("automation not read: %+v", got)
	}
	if got.Sync != "Synced" || got.Revision != "abc123" || got.Health != "Healthy" {
		t.Errorf("status not read: %+v", got)
	}
	if got.Path != "charts/app/values/hanzo/papers.yaml" {
		t.Errorf("path = %q; it must name the values file this API writes", got.Path)
	}
	if got.Namespace != "hanzo" || got.Project != "hanzo-platform" {
		t.Errorf("fence not read: %+v", got)
	}

	// No syncPolicy at all ⇒ neither flag, and specifically not selfHeal.
	delete(cr.Object["spec"].(map[string]any), "syncPolicy")
	if got := observeCDApp(cr); got.Automated || got.SelfHeal {
		t.Errorf("an Application with no syncPolicy reported automation: %+v", got)
	}
}

// The Application name a Declaration publishes is the key CD files it under, or
// the board joins nothing. Pinned here against the ApplicationSet's own
// `{{ .path.basename }}-{{ .path.filename | trimSuffix ".yaml" }}`.
func TestTheJoinKeyMatchesTheApplicationSetNaming(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "hanzo", "papers", "image:\n  repository: ghcr.io/hanzoai/papers\n  tag: fec5ac6\n")
	d, err := readDeclaration(root, "hanzo", "papers")
	if err != nil {
		t.Fatal(err)
	}
	if d.Application != "hanzo-papers" {
		t.Fatalf("Application = %q, want the generator's hanzo-papers", d.Application)
	}
	if d.Path != "charts/app/values/hanzo/papers.yaml" {
		t.Fatalf("Path = %q", d.Path)
	}
}

// ── harness ──────────────────────────────────────────────────────────────────

// mountDelivery registers the delivery surface exactly as Mount does — both
// services, no cluster — so route registration and the gate are exercised
// through the real composition rather than a hand-built router.
func mountDelivery(t *testing.T) *zip.App {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &cloud.Service[state]{
		Base: cloud.Base{KMS: newFakeKMS(), Log: luxlog.New("test"), Brand: "hanzo"},
		State: state{store: store, sitesHost: "hanzo.app",
			k8s: &k8sClient{initErr: "no cluster (test)", imagePrefix: defaultBuildImagePrefix, limits: testLimits()}},
	}
	fs := &cloud.Service[fleetState]{
		Base:  cloud.Base{Log: luxlog.New("test"), Brand: "hanzo"},
		State: fleetState{initErr: "no cluster (test)"},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// Bridge is what Serve installs in front of every typed route in every
	// process, and it is what parks the request on the context a typed op is
	// handed. Without it the four reads here answer 403 to a genuine admin —
	// `admit` finds no request and fails closed — which is a harness that can
	// only ever measure a refusal. The raw handlers this file used to drive read
	// identity straight off the *zip.Ctx and so never needed it.
	app.Use(cloud.Bridge())
	appsRoutes(app, s, fs)
	return app
}

// doAdmin fires a request as an ORG ADMIN — the scope this whole surface
// requires. X-User-IsOrgAdmin is a header SanitizeIdentity strips on ingress and
// re-mints only from a validated claim, so setting it here is the harness
// standing in for that mint, not a forgery the edge would accept.
func doAdmin(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	req := newJSONRequest(t, method, path, body)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u-"+org)
	req.Header.Set("X-User-IsOrgAdmin", "true")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func newJSONRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	if body == nil {
		return httptest.NewRequest(method, path, nil)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// Building and committing in one call can never succeed — the image the build
// would produce does not exist when the commit proves pullability — so it is
// refused BEFORE a privileged build is spent on it.
func TestBuildAndCommitInOneCallIsRefusedBeforeTheBuild(t *testing.T) {
	app := mountDelivery(t)
	code, body := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
		"repo": "https://github.com/acme/web", "mode": "commit",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400 for build+commit, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "then commit that tag") {
		t.Errorf("the refusal must name the two-step flow: %s", body)
	}
}
