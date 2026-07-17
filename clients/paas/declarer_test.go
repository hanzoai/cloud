package paas

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestAppsGVR pins the operator App CR identity — the kind the fleet runs on. A
// typo here silently blinds the whole board, exactly as reading only `services`
// did (7 rows rendered for a 69-app fleet).
func TestAppsGVR(t *testing.T) {
	want := schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "apps"}
	if appsGVR != want {
		t.Fatalf("appsGVR = %v, want %v", appsGVR, want)
	}
}

// TestCRGVRsOrder pins the read order: the fleet's kind first, the kind it
// collapsed from second. Order is load-bearing — when both kinds claim a name,
// the first one wins the row, and App is the kind that owns the Deployment.
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

// TestObserveFleetSeesAppCRs is the regression that matters: the live fleet is
// declared as App CRs, and the board rendered none of them. Seed only App CRs —
// the shape of the real `hanzo` namespace — and the board must list them.
func TestObserveFleetSeesAppCRs(t *testing.T) {
	s := fakeService(
		appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"),
		appCRObj("console", "hanzo", "ghcr.io/hanzoai/console", "v8.4.0"),
		appCRObj("iam", "hanzo", "ghcr.io/hanzoai/iam", "v1.28.16"),
	)
	views, err := observeFleet(s, context.Background())
	if err != nil {
		t.Fatalf("observeFleet: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("board rendered %d rows for a 3-App fleet, want 3 — reading only `services` is what blinded it", len(views))
	}
	byName := map[string]string{}
	for _, v := range views {
		byName[v.App] = v.DeclaredTag
	}
	for app, tag := range map[string]string{"cloud": "v1.801.38", "console": "v8.4.0", "iam": "v1.28.16"} {
		if byName[app] != tag {
			t.Errorf("app %s declaredTag = %q, want %q", app, byName[app], tag)
		}
	}
}

// TestObserveFleetSeesBothKinds — the fleet is mid-collapse: system workloads are
// App CRs, untransitioned ones are still Service CRs. The board must show every
// workload regardless of which kind declares it.
func TestObserveFleetSeesBothKinds(t *testing.T) {
	s := fakeService(
		appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"),
		serviceCRObj("legacy", "hanzo", "ghcr.io/hanzoai/legacy", "v0.1.0"),
	)
	views, err := observeFleet(s, context.Background())
	if err != nil {
		t.Fatalf("observeFleet: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("got %d rows, want 2 (one App + one Service)", len(views))
	}
}

// TestObserveFleetDedupesACollidingName — when an App CR and a Service CR both
// claim one name, there is still only ONE workload: a Deployment has one
// controller ownerRef, and the operator's Claim guard gives it to the App. The
// board reports one row, from the App.
func TestObserveFleetDedupesACollidingName(t *testing.T) {
	s := fakeService(
		appCRObj("commerce-admin", "hanzo", "ghcr.io/hanzoai/commerce-admin", "0.2.0-amd64"),
		serviceCRObj("commerce-admin", "hanzo", "ghcr.io/hanzoai/commerce-admin", "0.1.0-amd64"),
	)
	views, err := observeFleet(s, context.Background())
	if err != nil {
		t.Fatalf("observeFleet: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("got %d rows for one workload, want 1 — a colliding name is one workload, not two", len(views))
	}
	if views[0].DeclaredTag != "0.2.0-amd64" {
		t.Fatalf("declaredTag = %q, want the App's 0.2.0-amd64 — the App owns the Deployment", views[0].DeclaredTag)
	}
}

// TestResolveTargetPrefersTheAppKind — resolution reports which kind holds the
// workload, because the deploy path's behavior depends on it.
func TestResolveTargetPrefersTheAppKind(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"))
	ns, gvr, err := resolveTarget(s, context.Background(), "cloud")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if ns != "hanzo" {
		t.Errorf("ns = %q, want hanzo", ns)
	}
	if gvr != appsGVR {
		t.Errorf("gvr = %v, want appsGVR", gvr)
	}
}

// TestResolveTargetFindsAServiceKind — a Service-declared workload still resolves,
// and reports the Service kind so the deploy path patches it.
func TestResolveTargetFindsAServiceKind(t *testing.T) {
	s := fakeService(serviceCRObj("legacy", "hanzo-devnet", "ghcr.io/hanzoai/legacy", "v0.1.0"))
	ns, gvr, err := resolveTarget(s, context.Background(), "legacy")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if ns != "hanzo-devnet" {
		t.Errorf("ns = %q, want hanzo-devnet", ns)
	}
	if gvr != servicesGVR {
		t.Errorf("gvr = %v, want servicesGVR", gvr)
	}
}

// TestResolveTargetIsNotFoundForAnAbsentWorkload — fail closed, no silent default.
func TestResolveTargetIsNotFoundForAnAbsentWorkload(t *testing.T) {
	s := fakeService()
	if _, _, err := resolveTarget(s, context.Background(), "ghost"); err == nil {
		t.Fatal("resolveTarget on an absent workload returned nil error, want not-found")
	}
}

// TestKindAtPinsTheNamespace — an explicit namespace must not silently fall back
// to another namespace's workload of the same name.
func TestKindAtPinsTheNamespace(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"))
	if _, err := kindAt(s, context.Background(), "hanzo-devnet", "cloud"); err == nil {
		t.Fatal("kindAt found `cloud` in hanzo-devnet, where it does not exist — an explicit namespace must be honored")
	}
	gvr, err := kindAt(s, context.Background(), "hanzo", "cloud")
	if err != nil {
		t.Fatalf("kindAt(hanzo, cloud): %v", err)
	}
	if gvr != appsGVR {
		t.Errorf("gvr = %v, want appsGVR", gvr)
	}
}

// TestReleaseRefusesAGitDeclaredWorkload — an App CR is synced from universe by
// Hanzo CD with selfHeal, so patching it here is reverted on the next sync. The
// release must refuse and say where the declaration lives, rather than report a
// success that silently rolls back.
func TestReleaseRefusesAGitDeclaredWorkload(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"))
	_, _, changed, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.801.39")
	if err == nil {
		t.Fatal("released a git-declared App CR; want a refusal (the patch would be reverted by selfHeal)")
	}
	if changed {
		t.Error("changed = true on a refused release")
	}
	for _, want := range []string{"declared in git", "crs/cloud.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name where to deploy instead; got %q, want it to contain %q", err, want)
		}
	}
	// The CR must be untouched — a refusal writes nothing.
	obj, gErr := s.State.dyn.Resource(appsGVR).Namespace("hanzo").Get(context.Background(), "cloud", metav1.GetOptions{})
	if gErr != nil {
		t.Fatalf("get App CR: %v", gErr)
	}
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	if tag != "v1.801.38" {
		t.Errorf("refused release still mutated the CR: tag = %q, want v1.801.38", tag)
	}
}

// TestReleaseStillPatchesAServiceDeclaredWorkload — a Service CR has no git
// declarer (cloud and kubectl write them directly), so the release still patches.
func TestReleaseStillPatchesAServiceDeclaredWorkload(t *testing.T) {
	s := fakeService(serviceCRObj("legacy", "hanzo-devnet", "ghcr.io/hanzoai/legacy", "v0.1.0"))
	ns, tag, changed, err := releaseService(s, context.Background(), "legacy", "ghcr.io/hanzoai/legacy:v0.2.0")
	if err != nil {
		t.Fatalf("releaseService on a Service-declared workload: %v", err)
	}
	if !changed || ns != "hanzo-devnet" || tag != "v0.2.0" {
		t.Fatalf("got (ns=%q tag=%q changed=%v), want (hanzo-devnet, v0.2.0, true)", ns, tag, changed)
	}
}

// TestObserveFleetSkipsAnEmptyNamespace — a namespace with no workloads is not an
// error; the board still renders the reachable ones.
func TestObserveFleetSkipsAnEmptyNamespace(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"))
	views, err := observeFleet(s, context.Background())
	if err != nil {
		t.Fatalf("observeFleet must tolerate empty namespaces: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("got %d rows, want 1", len(views))
	}
}
