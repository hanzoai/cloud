package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zap-proto/zip"
)

// crImage reads the operator Service CR's spec.image repo:tag from tenant-<org>.
func crImage(t *testing.T, k *k8sClient, ns, name string) string {
	t.Helper()
	obj, err := k.dyn.Resource(appsGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Service CR %s/%s: %v", ns, name, err)
	}
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	return repo + ":" + tag
}

// seedImageApp creates a project + image-source app for the org (helper).
func seedImageApp(t *testing.T, app *zip.App, org, project, name string) {
	t.Helper()
	do(t, app, http.MethodPost, "/v1/platform/projects", org, map[string]any{"name": project})
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/"+project+"/apps", org, map[string]any{
		"name": name, "source": "image",
		"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"port":  8080,
	})
	if code != http.StatusCreated {
		t.Fatalf("seed app want 201, got %d (%s)", code, body)
	}
}

// TestPreviewDeploysBranchTargetIsolatedFromProd proves a preview deploy: POST
// .../preview with {branch,image} deploys the image to a per-branch target that is
// its OWN application (slug "<app>-<branch>") with its OWN default host in the SAME
// tenant namespace — writing a distinct Service CR the operator reconciles — and
// returns the preview URL, all WITHOUT touching the prod app's CR.
func TestPreviewDeploysBranchTargetIsolatedFromProd(t *testing.T) {
	k := fakeK8s()
	app := mountAppK8s(t, k)
	seedImageApp(t, app, "maxpower", "web", "api")

	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/preview", "maxpower", map[string]any{
		"branch": "feat-x",
		"image":  "ghcr.io/hanzoai/nginx:pr-42",
	})
	if code != http.StatusAccepted {
		t.Fatalf("preview want 202, got %d (%s)", code, body)
	}
	var pv previewView
	if err := json.Unmarshal(body, &pv); err != nil {
		t.Fatalf("decode previewView: %v (%s)", err, body)
	}
	if want := "https://api-feat-x.maxpower.hanzo.app"; pv.URL != want {
		t.Fatalf("preview url want %q, got %q", want, pv.URL)
	}
	if pv.App != "api-feat-x" || pv.Branch != "feat-x" {
		t.Fatalf("unexpected preview identity: %+v", pv)
	}
	if pv.Deployment.Status != "deploying" || pv.Deployment.Image != "ghcr.io/hanzoai/nginx:pr-42" {
		t.Fatalf("unexpected preview deployment: %+v", pv.Deployment)
	}

	// The preview Service CR exists in tenant-maxpower under the PREVIEW slug with
	// the preview image and its OWN ingress host.
	if got := crImage(t, k, "tenant-maxpower", "api-feat-x"); got != "ghcr.io/hanzoai/nginx:pr-42" {
		t.Fatalf("preview CR image wrong: %s", got)
	}
	obj, _ := k.dyn.Resource(appsGVR).Namespace("tenant-maxpower").Get(context.Background(), "api-feat-x", metav1.GetOptions{})
	hosts, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "ingress", "hosts")
	if len(hosts) != 1 || hosts[0] != "api-feat-x.maxpower.hanzo.app" {
		t.Fatalf("preview ingress host wrong: %v", hosts)
	}

	// Isolation: prod's app CR ("api") was never written by the preview.
	if _, err := k.dyn.Resource(appsGVR).Namespace("tenant-maxpower").Get(context.Background(), "api", metav1.GetOptions{}); err == nil {
		t.Fatal("preview must NOT create/modify the prod Service CR")
	}

	// Re-previewing the same branch converges the SAME preview app in place (not a
	// second app): the org's project shows exactly prod + one preview app.
	code, _ = do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/preview", "maxpower", map[string]any{
		"branch": "feat-x", "image": "ghcr.io/hanzoai/nginx:pr-43",
	})
	if code != http.StatusAccepted {
		t.Fatalf("re-preview want 202, got %d", code)
	}
	if got := crImage(t, k, "tenant-maxpower", "api-feat-x"); got != "ghcr.io/hanzoai/nginx:pr-43" {
		t.Fatalf("re-preview CR image want pr-43, got %s", got)
	}
	code, listBody := do(t, app, http.MethodGet, "/v1/platform/projects/web/apps", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list apps want 200, got %d", code)
	}
	var apps []appView
	_ = json.Unmarshal(listBody, &apps)
	if len(apps) != 2 {
		t.Fatalf("want exactly prod + 1 preview app, got %d: %+v", len(apps), apps)
	}
}

// TestRollbackResolvesPriorTag proves rollback redeploys the PREVIOUS release's
// image: after two prod deploys (nginx:1 then nginx:2), a bodyless rollback
// resolves the prior tag (nginx:1) from the deployments store and redeploys it as a
// new version through the shared deploy core — and a rollback BY deploymentId
// redeploys that exact deployment's image.
func TestRollbackResolvesPriorTag(t *testing.T) {
	k := fakeK8s()
	app := mountAppK8s(t, k)
	seedImageApp(t, app, "maxpower", "web", "api")

	// Two prod deploys → v1 nginx:1 (live), then v2 nginx:2 (current).
	for _, tag := range []string{"1", "2"} {
		if code, b := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": tag}); code != http.StatusAccepted {
			t.Fatalf("deploy tag %s want 202, got %d (%s)", tag, code, b)
		}
	}
	if got := crImage(t, k, "tenant-maxpower", "api"); got != "ghcr.io/hanzoai/nginx:2" {
		t.Fatalf("after v2 deploy CR image want nginx:2, got %s", got)
	}

	// Bodyless rollback → resolves the prior release (v1 nginx:1) and redeploys it.
	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/rollback", "maxpower", nil)
	if code != http.StatusAccepted {
		t.Fatalf("rollback want 202, got %d (%s)", code, body)
	}
	var rv deploymentView
	_ = json.Unmarshal(body, &rv)
	if rv.Version != 3 || rv.Image != "ghcr.io/hanzoai/nginx:1" {
		t.Fatalf("rollback must redeploy the prior image as v3, got %+v", rv)
	}
	if got := crImage(t, k, "tenant-maxpower", "api"); got != "ghcr.io/hanzoai/nginx:1" {
		t.Fatalf("after rollback CR image want nginx:1, got %s", got)
	}

	// Rollback BY deploymentId → the exact named deployment's image (v2 nginx:2).
	_, deps := listDeps(t, app, "maxpower")
	var v2ID string
	for _, d := range deps {
		if d.Version == 2 {
			v2ID = d.ID
		}
	}
	if v2ID == "" {
		t.Fatal("could not find v2 deployment id")
	}
	code, body = do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/rollback", "maxpower", map[string]any{"deploymentId": v2ID})
	if code != http.StatusAccepted {
		t.Fatalf("rollback-by-id want 202, got %d (%s)", code, body)
	}
	if got := crImage(t, k, "tenant-maxpower", "api"); got != "ghcr.io/hanzoai/nginx:2" {
		t.Fatalf("after rollback-by-id CR image want nginx:2, got %s", got)
	}

	// No prior release to roll back to is an honest 400 (fresh app, one deploy only).
	seedImageApp(t, app, "maxpower", "solo", "only")
	do(t, app, http.MethodPost, "/v1/platform/projects/solo/apps/only/deploy", "maxpower", map[string]any{"tag": "1"})
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/solo/apps/only/rollback", "maxpower", nil); code != http.StatusBadRequest {
		t.Fatalf("rollback with no prior release want 400, got %d", code)
	}
}

// TestPromoteSetsProdImage proves promote makes an already-built artifact the prod
// release, by tag (resolved <imageRepo>:<tag>) and by a prior deployment's exact
// image — reusing the shared deploy core to write the prod app's Service CR.
func TestPromoteSetsProdImage(t *testing.T) {
	k := fakeK8s()
	app := mountAppK8s(t, k)
	seedImageApp(t, app, "maxpower", "web", "api")
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": "1"})

	// Promote by tag → prod CR image becomes nginx:9.
	if code, b := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/promote", "maxpower", map[string]any{"tag": "9"}); code != http.StatusAccepted {
		t.Fatalf("promote by tag want 202, got %d (%s)", code, b)
	}
	if got := crImage(t, k, "tenant-maxpower", "api"); got != "ghcr.io/hanzoai/nginx:9" {
		t.Fatalf("after promote-by-tag CR image want nginx:9, got %s", got)
	}

	// Promote by a prior deployment's id → that deployment's exact image (nginx:1).
	_, deps := listDeps(t, app, "maxpower")
	var v1ID string
	for _, d := range deps {
		if d.Version == 1 {
			v1ID = d.ID
		}
	}
	if code, b := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/promote", "maxpower", map[string]any{"deploymentId": v1ID}); code != http.StatusAccepted {
		t.Fatalf("promote by deploymentId want 202, got %d (%s)", code, b)
	}
	if got := crImage(t, k, "tenant-maxpower", "api"); got != "ghcr.io/hanzoai/nginx:1" {
		t.Fatalf("after promote-by-id CR image want nginx:1, got %s", got)
	}

	// Missing target is a 400 at the boundary.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/promote", "maxpower", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("promote with no target want 400, got %d", code)
	}
}

// TestPreviewFlowsTenantScoped proves the three new routes are org-scoped like the
// rest of platform: a forged org (no validated principal) is refused 403, and a
// cross-tenant caller sees the app as not-found (404) — never another org's target.
func TestPreviewFlowsTenantScoped(t *testing.T) {
	app := mountAppK8s(t, fakeK8s())
	seedImageApp(t, app, "maxpower", "web", "api")

	for _, path := range []string{
		"/v1/platform/projects/web/apps/api/preview",
		"/v1/platform/projects/web/apps/api/promote",
		"/v1/platform/projects/web/apps/api/rollback",
	} {
		// Forged org, no validated principal (X-User-Id empty) → 403 before any work.
		if code, _ := doAs(t, app, http.MethodPost, path, "maxpower", "", map[string]any{"branch": "x", "image": "ghcr.io/hanzoai/nginx:x", "tag": "x"}); code != http.StatusForbidden {
			t.Fatalf("forged-org %s want 403, got %d", path, code)
		}
		// Cross-tenant: acme (validated) cannot reach maxpower's app → 404.
		if code, _ := do(t, app, http.MethodPost, path, "acme", map[string]any{"branch": "x", "image": "ghcr.io/hanzoai/nginx:x", "tag": "x"}); code != http.StatusNotFound {
			t.Fatalf("cross-tenant %s want 404, got %d", path, code)
		}
	}
}
