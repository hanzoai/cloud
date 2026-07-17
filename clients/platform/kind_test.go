package platform

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestAppsGVR pins the operator App CR identity — the kind a tenant app is
// written as.
func TestAppsGVR(t *testing.T) {
	want := schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "apps"}
	if appsGVR != want {
		t.Fatalf("appsGVR = %v, want %v", appsGVR, want)
	}
}

// TestTenantCRIsWrittenAsApp — a tenant app is declared as an App CR, and no
// Service CR is minted for it.
func TestTenantCRIsWrittenAsApp(t *testing.T) {
	cr := serviceCR("tenant-maxpower", "maxpower", "proj", Application{Slug: "api", Replicas: 1}, "ghcr.io/hanzoai/nginx:1.27")
	if got := cr.Object["kind"]; got != "App" {
		t.Fatalf("kind = %v, want App — a tenant workload is declared as an App CR", got)
	}
	if got := cr.Object["apiVersion"]; got != "hanzo.ai/v1" {
		t.Fatalf("apiVersion = %v, want hanzo.ai/v1", got)
	}
	// A role-less App dispatches to the operator's service profile
	// (classify("") => Dispatch::Service), so the workload reconciles identically.
	spec := cr.Object["spec"].(map[string]any)
	if _, hasRole := spec["role"]; hasRole {
		t.Error("a tenant App must carry no role: the default profile IS the service reconcile")
	}
}

// TestRedeployPatchesTheAppInPlace — a redeploy over an existing App CR
// merge-patches it (new image) rather than minting a twin.
func TestRedeployPatchesTheAppInPlace(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "App",
		"metadata":   map[string]any{"name": "api", "namespace": "tenant-maxpower"},
		"spec":       map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.26"}},
	}}
	k := fakeK8s(app)
	ctx := context.Background()

	if err := k.applyService(ctx, "maxpower", "proj", Application{Slug: "api", Replicas: 1}, "ghcr.io/hanzoai/nginx:1.27"); err != nil {
		t.Fatalf("applyService redeploy: %v", err)
	}
	obj, err := k.dyn.Resource(appsGVR).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("App CR: %v", err)
	}
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	if tag != "1.27" {
		t.Fatalf("App CR tag = %q, want 1.27 — the redeploy must patch it in place", tag)
	}
}

// TestDeleteRemovesTheApp — teardown removes the tenant's App CR so it stops
// reconciling (and stops billing).
func TestDeleteRemovesTheApp(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "App",
		"metadata":   map[string]any{"name": "api", "namespace": "tenant-maxpower"},
		"spec":       map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"}},
	}}
	k := fakeK8s(app)
	ctx := context.Background()

	if err := k.deleteService(ctx, "maxpower", "api"); err != nil {
		t.Fatalf("deleteService: %v", err)
	}
	if _, err := k.dyn.Resource(appsGVR).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{}); err == nil {
		t.Error("App CR survived the delete — it would rebuild the app the tenant deleted")
	}
}

// TestDeleteOfAnAbsentAppIsSuccess — teardown is idempotent.
func TestDeleteOfAnAbsentAppIsSuccess(t *testing.T) {
	k := fakeK8s()
	if err := k.deleteService(context.Background(), "maxpower", "ghost"); err != nil {
		t.Fatalf("deleting an absent app must be success (already gone), got %v", err)
	}
}

// TestResolveCRFindsTheApp — resolution reports the App CR that holds the tenant
// workload, and reports absence honestly.
func TestResolveCRFindsTheApp(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1", "kind": "App",
		"metadata": map[string]any{"name": "new", "namespace": "tenant-maxpower"},
		"spec":     map[string]any{},
	}}
	k := fakeK8s(app)
	ctx := context.Background()

	gvr, found, err := k.resolveCR(ctx, "tenant-maxpower", "new")
	if err != nil || !found {
		t.Fatalf("resolveCR(new): found=%v err=%v", found, err)
	}
	if gvr != appsGVR {
		t.Errorf("resolveCR(new) = %v, want appsGVR", gvr)
	}
	if _, found, err := k.resolveCR(ctx, "tenant-maxpower", "ghost"); found || err != nil {
		t.Errorf("resolveCR(ghost): found=%v err=%v, want found=false err=nil", found, err)
	}
}
