package paas

// authz_test.go — the IAM authorization + tenant-confinement contract for the
// /v1/paas fleet board, the twin of clients/platform/runner_test.go. Every route
// is now authorized off ONE IAM identity (SuperAdmin or org-confined OrgAdmin);
// these tests pin that a plain login is refused, an OrgAdmin sees ONLY its own
// org's namespaces, a SuperAdmin sees the fleet, and the deploy path performs a
// real rolling restart bounded by the same confinement.
//
// Identity is injected exactly as runner_test.go's postRunnerAs does: the headers
// SanitizeIdentity mints from a signature-verified JWT (X-User-Id ⇒ Validated,
// X-Org-Id ⇒ Org, X-User-IsOrgAdmin / X-User-IsAdmin for the role). In production
// these are unforgeable (stripped on ingress, re-minted only from validated
// claims); the harness sets them directly, the same trust model the tenant()-gated
// tests use.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// deploymentObj builds a real-shaped Deployment for the fake cluster — the running
// workload behind a Service CR, the object a rolling restart patches.
func deploymentObj(name, ns, image string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{"name": name},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": name, "image": image}}},
			},
		},
	}}
}

// paasApp builds a paas Service over a fake cluster seeded with objs, mounts the
// real routes (so the live guard runs), and returns the app to drive.
func paasApp(t *testing.T, objs ...runtime.Object) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	s := fakeService(objs...)
	s.Base.Log = luxlog.New("test")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return app, s
}

// doAs drives a request as a validated IAM principal with the given role headers.
func doAs(t *testing.T, app *zip.App, method, path, user, org string, orgAdmin, superAdmin bool) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if orgAdmin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if superAdmin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// fleet seeds a realistic hanzo-brand board: two apps in prod, one in test.
func fleet() []runtime.Object {
	return []runtime.Object{
		appCRObj("iam", "hanzo", "ghcr.io/hanzoai/iam", "v1.28.16"),
		deploymentObj("iam", "hanzo", "ghcr.io/hanzoai/iam:v1.28.16"),
		appCRObj("kms", "hanzo", "ghcr.io/hanzoai/kms", "v1.11.7"),
		deploymentObj("kms", "hanzo", "ghcr.io/hanzoai/kms:v1.11.7"),
		appCRObj("chat", "hanzo-testnet", "ghcr.io/hanzoai/chat", "v0.9.0"),
		deploymentObj("chat", "hanzo-testnet", "ghcr.io/hanzoai/chat:v0.9.0"),
	}
}

func appsTotal(t *testing.T, body []byte) int {
	t.Helper()
	var out struct {
		Apps    []AppView `json:"apps"`
		Summary struct {
			Total int `json:"total"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode apps: %v (%s)", err, body)
	}
	return out.Summary.Total
}

// ── the guard: role gate ─────────────────────────────────────────────────────

// An unauthenticated request (no validated principal) is refused — the board never
// serves an anonymous caller, exactly as before the broadening.
func TestGuard_Unauthenticated_403(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	if code, _ := doAs(t, app, http.MethodGet, "/v1/paas/apps", "", "", false, false); code != http.StatusForbidden {
		t.Fatalf("anonymous: want 403, got %d", code)
	}
}

// A validated principal who is a plain member (no admin bit) is refused: a login is
// necessary but not sufficient — the board is an operator surface.
func TestGuard_NonAdminMember_403(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	if code, _ := doAs(t, app, http.MethodGet, "/v1/paas/apps", "u-plain", "hanzo", false, false); code != http.StatusForbidden {
		t.Fatalf("plain member: want 403, got %d", code)
	}
}

// ── listApps: tenant confinement ─────────────────────────────────────────────

// An OrgAdmin of the platform org sees its own org's whole board (all hanzo* ns).
func TestListApps_OrgAdmin_SeesOwnOrg(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	code, body := doAs(t, app, http.MethodGet, "/v1/paas/apps", "z-uuid", "hanzo", true, false)
	if code != http.StatusOK {
		t.Fatalf("hanzo org-admin: want 200, got %d (%s)", code, body)
	}
	if n := appsTotal(t, body); n != 3 {
		t.Fatalf("hanzo org-admin: want 3 apps (2 prod + 1 test), got %d (%s)", n, body)
	}
}

// A foreign OrgAdmin (owns no scanned namespace) sees an EMPTY board — never the
// platform fleet. This is the cross-tenant-leak guard.
func TestListApps_ForeignOrgAdmin_EmptyBoard(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	code, body := doAs(t, app, http.MethodGet, "/v1/paas/apps", "acme-admin", "acme", true, false)
	if code != http.StatusOK {
		t.Fatalf("acme org-admin: want 200, got %d (%s)", code, body)
	}
	if n := appsTotal(t, body); n != 0 {
		t.Fatalf("acme org-admin must see 0 hanzo apps, got %d (%s)", n, body)
	}
}

// A forged X-Org-Id cannot widen the view: a foreign OrgAdmin passing ?org=hanzoai
// (the image namespace) still sees nothing — confinement is at the namespace scan,
// keyed on the validated org, before the query filter runs.
func TestListApps_ForeignOrgAdmin_QueryCannotWiden(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	code, body := doAs(t, app, http.MethodGet, "/v1/paas/apps?org=hanzoai", "acme-admin", "acme", true, false)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if n := appsTotal(t, body); n != 0 {
		t.Fatalf("?org=hanzoai must not widen an acme admin's view: got %d apps (%s)", n, body)
	}
}

// A SuperAdmin sees the whole fleet regardless of its own org.
func TestListApps_SuperAdmin_SeesFleet(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	code, body := doAs(t, app, http.MethodGet, "/v1/paas/apps", "root", "admin", false, true)
	if code != http.StatusOK {
		t.Fatalf("superadmin: want 200, got %d (%s)", code, body)
	}
	if n := appsTotal(t, body); n != 3 {
		t.Fatalf("superadmin: want the whole fleet (3), got %d (%s)", n, body)
	}
}

// ── getApp: tenant confinement ───────────────────────────────────────────────

// A foreign OrgAdmin cannot read one of the platform's apps — a clean 404, no
// existence leak.
func TestGetApp_ForeignOrgAdmin_404(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	if code, _ := doAs(t, app, http.MethodGet, "/v1/paas/apps/iam", "acme-admin", "acme", true, false); code != http.StatusNotFound {
		t.Fatalf("acme admin reading hanzo/iam: want 404, got %d", code)
	}
}

// The platform OrgAdmin reads its own app row.
func TestGetApp_OrgAdmin_200(t *testing.T) {
	app, _ := paasApp(t, fleet()...)
	code, body := doAs(t, app, http.MethodGet, "/v1/paas/apps/iam", "z-uuid", "hanzo", true, false)
	if code != http.StatusOK {
		t.Fatalf("hanzo admin reading iam: want 200, got %d (%s)", code, body)
	}
}

// ── deploy: rolling restart, org-confined ────────────────────────────────────

// restartedAt reads the pod-template restart annotation off the live Deployment in
// the fake, "" when absent.
func restartedAt(t *testing.T, s *cloud.Service[state], ns, name string) string {
	t.Helper()
	obj, err := s.State.dyn.Resource(deploymentsGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment %s/%s: %v", ns, name, err)
	}
	v, _, _ := unstructured.NestedString(obj.Object, "spec", "template", "metadata", "annotations", restartedAtAnnotation)
	return v
}

// An OrgAdmin rolling-restarts its own app: 202 + the Deployment carries a fresh
// restartedAt annotation (the kubectl rollout restart mechanism).
func TestDeploy_OrgAdmin_RollingRestart(t *testing.T) {
	app, s := paasApp(t, fleet()...)
	if got := restartedAt(t, s, "hanzo", "iam"); got != "" {
		t.Fatalf("precondition: iam should have no restart stamp, got %q", got)
	}
	code, body := doAs(t, app, http.MethodPost, "/v1/paas/apps/iam/deploy", "z-uuid", "hanzo", true, false)
	if code != http.StatusAccepted {
		t.Fatalf("hanzo admin deploy iam: want 202, got %d (%s)", code, body)
	}
	var resp struct {
		OK          bool   `json:"ok"`
		App         string `json:"app"`
		Namespace   string `json:"namespace"`
		RestartedAt string `json:"restartedAt"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode deploy resp: %v (%s)", err, body)
	}
	if !resp.OK || resp.App != "iam" || resp.Namespace != "hanzo" || resp.RestartedAt == "" {
		t.Fatalf("unexpected deploy resp: %+v", resp)
	}
	if got := restartedAt(t, s, "hanzo", "iam"); got != resp.RestartedAt {
		t.Fatalf("Deployment restart stamp = %q, want %q (the patch must land)", got, resp.RestartedAt)
	}
}

// A foreign OrgAdmin cannot restart the platform's app — 404, and the Deployment is
// UNTOUCHED (no restart stamp). This is the mutating-path confinement.
func TestDeploy_ForeignOrgAdmin_404_NoMutation(t *testing.T) {
	app, s := paasApp(t, fleet()...)
	code, _ := doAs(t, app, http.MethodPost, "/v1/paas/apps/iam/deploy", "acme-admin", "acme", true, false)
	if code != http.StatusNotFound {
		t.Fatalf("acme admin restarting hanzo/iam: want 404, got %d", code)
	}
	if got := restartedAt(t, s, "hanzo", "iam"); got != "" {
		t.Fatalf("a refused deploy must not mutate the Deployment; got restart stamp %q", got)
	}
}

// ?env selects the namespace within the caller's authorized set: an OrgAdmin can
// restart the test-env app by naming it, and prod is untouched.
func TestDeploy_OrgAdmin_EnvSelectsNamespace(t *testing.T) {
	app, s := paasApp(t, fleet()...)
	code, body := doAs(t, app, http.MethodPost, "/v1/paas/apps/chat/deploy?env=test", "z-uuid", "hanzo", true, false)
	if code != http.StatusAccepted {
		t.Fatalf("deploy chat @test: want 202, got %d (%s)", code, body)
	}
	if got := restartedAt(t, s, "hanzo-testnet", "chat"); got == "" {
		t.Fatalf("chat in hanzo-testnet should carry a restart stamp")
	}
}

// ── unit: the namespace→org map ──────────────────────────────────────────────

func TestNsOrg(t *testing.T) {
	cases := map[string]string{
		"hanzo":         "hanzo",
		"hanzo-testnet": "hanzo",
		"hanzo-devnet":  "hanzo",
	}
	for ns, want := range cases {
		if got := nsOrg(ns); got != want {
			t.Errorf("nsOrg(%q) = %q, want %q", ns, got, want)
		}
	}
}
