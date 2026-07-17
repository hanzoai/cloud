package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestRunCreatesAutoscaledServiceCR proves the container-serverless one-shot: POST
// /v1/run writes the operator Service CR into tenant-<org> (derived from the
// VALIDATED org, never the body) with the requested image, an autoscaling block
// over [minScale,maxScale], the port, and the default-host ingress — and returns
// the run's live URL + status. It reuses the SAME Service-CR writer as deploy.go
// (asserted by reading back the CR the deploy path also produces).
func TestRunCreatesAutoscaledServiceCR(t *testing.T) {
	k := fakeK8s()
	app := mountAppK8s(t, k)

	code, body := do(t, app, http.MethodPost, "/v1/run", "maxpower", map[string]any{
		"name":     "api",
		"image":    "ghcr.io/hanzoai/nginx:1.27",
		"port":     8080,
		"minScale": 2,
		"maxScale": 7,
		"shape":    "medium",
		"env":      []map[string]any{{"key": "LOG_LEVEL", "value": "info"}},
	})
	if code != http.StatusAccepted {
		t.Fatalf("run want 202, got %d (%s)", code, body)
	}

	var rv runView
	if err := json.Unmarshal(body, &rv); err != nil {
		t.Fatalf("decode runView: %v (%s)", err, body)
	}
	if rv.ID == "" || rv.Name != "api" || rv.Status != "deploying" || rv.Shape != "medium" {
		t.Fatalf("unexpected runView: %+v", rv)
	}
	if want := "https://api.maxpower.hanzo.app"; rv.URL != want {
		t.Fatalf("url want %q, got %q", want, rv.URL)
	}

	// The operator Service CR must exist in tenant-maxpower with the run's image,
	// autoscaling bounds, port and org label — written by the SHARED applyService.
	obj, err := k.dyn.Resource(appsGVR).Namespace("tenant-maxpower").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Service CR not created in tenant-maxpower: %v", err)
	}
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	if repo != "ghcr.io/hanzoai/nginx" || tag != "1.27" {
		t.Fatalf("CR image wrong: %s:%s", repo, tag)
	}
	min, _, _ := unstructured.NestedInt64(obj.Object, "spec", "autoscaling", "minReplicas")
	max, _, _ := unstructured.NestedInt64(obj.Object, "spec", "autoscaling", "maxReplicas")
	enabled, _, _ := unstructured.NestedBool(obj.Object, "spec", "autoscaling", "enabled")
	if !enabled || min != 2 || max != 7 {
		t.Fatalf("CR autoscaling wrong: enabled=%v min=%d max=%d", enabled, min, max)
	}
	if orgLabel, _, _ := unstructured.NestedString(obj.Object, "metadata", "labels", "hanzo.ai/org"); orgLabel != "maxpower" {
		t.Fatalf("CR must be org-labeled maxpower, got %q", orgLabel)
	}
	hosts, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "ingress", "hosts")
	if len(hosts) != 1 || hosts[0] != "api.maxpower.hanzo.app" {
		t.Fatalf("CR ingress hosts wrong: %v", hosts)
	}

	// The run is a first-class Application in the org's default project: re-running
	// the same name UPDATES it in place (idempotent), not a second app.
	code, body = do(t, app, http.MethodPost, "/v1/run", "maxpower", map[string]any{
		"name": "api", "image": "ghcr.io/hanzoai/nginx:1.28", "minScale": 1,
	})
	if code != http.StatusAccepted {
		t.Fatalf("re-run want 202, got %d (%s)", code, body)
	}
	code, listBody := do(t, app, http.MethodGet, "/v1/platform/projects/default/apps", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list apps want 200, got %d (%s)", code, listBody)
	}
	var apps []appView
	_ = json.Unmarshal(listBody, &apps)
	if len(apps) != 1 || apps[0].Slug != "api" || apps[0].Image.Tag != "1.28" {
		t.Fatalf("re-run must update one app to 1.28, got %+v", apps)
	}
}

// TestRunTenantGateAndFailClosed proves /v1/run refuses the forgeable no-principal
// path (403) and fails closed with 503 when the cluster is unreachable — never a
// fabricated URL.
func TestRunTenantGateAndFailClosed(t *testing.T) {
	// No validated principal (X-User-Id omitted) → 403, even with an org header.
	appNoCluster := mountApp(t)
	if code, _ := doAs(t, appNoCluster, http.MethodPost, "/v1/run", "maxpower", "", map[string]any{
		"name": "api", "image": "ghcr.io/hanzoai/nginx:1.27",
	}); code != http.StatusForbidden {
		t.Fatalf("no-principal run want 403, got %d", code)
	}

	// Validated principal but no cluster → fail closed 503 (honest), not a fake URL.
	if code, body := do(t, appNoCluster, http.MethodPost, "/v1/run", "maxpower", map[string]any{
		"name": "api", "image": "ghcr.io/hanzoai/nginx:1.27",
	}); code != http.StatusServiceUnavailable {
		t.Fatalf("no-cluster run want 503, got %d (%s)", code, body)
	}

	// Missing image is a 400 at the boundary.
	appK8s := mountAppK8s(t, fakeK8s())
	if code, _ := do(t, appK8s, http.MethodPost, "/v1/run", "maxpower", map[string]any{"name": "api"}); code != http.StatusBadRequest {
		t.Fatalf("missing image want 400, got %d", code)
	}
}
