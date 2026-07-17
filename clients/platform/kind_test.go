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

// TestCRGVRsOrder pins the resolution order: the kind we write first, the kind we
// used to write second.
func TestCRGVRsOrder(t *testing.T) {
	got := crGVRs()
	want := []schema.GroupVersionResource{appsGVR, servicesGVR}
	if len(got) != len(want) {
		t.Fatalf("crGVRs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("crGVRs()[%d] = %v, want %v", i, got[i], want[i])
		}
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

// TestRedeployOfALegacyServiceCRDoesNotMintATwin — an app deployed before the
// collapse is still a Service CR. Re-declaring it as an App would leave two CRs
// claiming one Deployment, which the operator resolves only by flipping its
// ownerRef. Patch the kind it actually is.
func TestRedeployOfALegacyServiceCRDoesNotMintATwin(t *testing.T) {
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": "api", "namespace": "tenant-maxpower"},
		"spec":       map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.26"}},
	}}
	k := fakeK8s(legacy)
	ctx := context.Background()

	if err := k.applyService(ctx, "maxpower", "proj", Application{Slug: "api", Replicas: 1}, "ghcr.io/hanzoai/nginx:1.27"); err != nil {
		t.Fatalf("applyService over a legacy Service CR: %v", err)
	}
	// No App twin.
	if _, err := k.dyn.Resource(appsGVR).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{}); err == nil {
		t.Fatal("minted an App twin next to the live Service CR — two declarers for one Deployment")
	}
	// The legacy CR carries the new image.
	obj, err := k.dyn.Resource(servicesGVR).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("legacy Service CR: %v", err)
	}
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	if tag != "1.27" {
		t.Fatalf("legacy CR tag = %q, want 1.27 — the redeploy must patch the kind it is", tag)
	}
}

// TestDeleteRemovesBothKinds — either kind alone re-materializes the Deployment,
// so a teardown that removes only one brings the app back minutes later, still
// billing.
func TestDeleteRemovesBothKinds(t *testing.T) {
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": "api", "namespace": "tenant-maxpower"},
		"spec":       map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.26"}},
	}}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "App",
		"metadata":   map[string]any{"name": "api", "namespace": "tenant-maxpower"},
		"spec":       map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"}},
	}}
	k := fakeK8s(legacy, app)
	ctx := context.Background()

	if err := k.deleteService(ctx, "maxpower", "api"); err != nil {
		t.Fatalf("deleteService: %v", err)
	}
	for _, gvr := range crGVRs() {
		if _, err := k.dyn.Resource(gvr).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{}); err == nil {
			t.Errorf("%s CR survived the delete — it would rebuild the app the tenant deleted", gvr.Resource)
		}
	}
}

// TestDeleteOfAnAbsentAppIsSuccess — teardown is idempotent.
func TestDeleteOfAnAbsentAppIsSuccess(t *testing.T) {
	k := fakeK8s()
	if err := k.deleteService(context.Background(), "maxpower", "ghost"); err != nil {
		t.Fatalf("deleting an absent app must be success (already gone), got %v", err)
	}
}

// TestResolveCRFindsEitherKind — resolution reports the kind that holds the CR,
// and reports absence honestly.
func TestResolveCRFindsEitherKind(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1", "kind": "App",
		"metadata": map[string]any{"name": "new", "namespace": "tenant-maxpower"},
		"spec":     map[string]any{},
	}}
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1", "kind": "Service",
		"metadata": map[string]any{"name": "old", "namespace": "tenant-maxpower"},
		"spec":     map[string]any{},
	}}
	k := fakeK8s(app, legacy)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		want schema.GroupVersionResource
	}{
		{"new", appsGVR},
		{"old", servicesGVR},
	} {
		gvr, found, err := k.resolveCR(ctx, "tenant-maxpower", tc.name)
		if err != nil || !found {
			t.Fatalf("resolveCR(%s): found=%v err=%v", tc.name, found, err)
		}
		if gvr != tc.want {
			t.Errorf("resolveCR(%s) = %v, want %v", tc.name, gvr, tc.want)
		}
	}
	if _, found, err := k.resolveCR(ctx, "tenant-maxpower", "ghost"); found || err != nil {
		t.Errorf("resolveCR(ghost): found=%v err=%v, want found=false err=nil", found, err)
	}
}
