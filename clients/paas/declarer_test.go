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

// TestResolveTargetFindsTheApp — resolution reports the namespace an App lives in,
// scanning production first.
func TestResolveTargetFindsTheApp(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.801.38"))
	ns, err := resolveTarget(s, context.Background(), "cloud")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if ns != "hanzo" {
		t.Errorf("ns = %q, want hanzo", ns)
	}
}

// TestResolveTargetIsNotFoundForAnAbsentWorkload — fail closed, no silent default.
func TestResolveTargetIsNotFoundForAnAbsentWorkload(t *testing.T) {
	s := fakeService()
	if _, err := resolveTarget(s, context.Background(), "ghost"); err == nil {
		t.Fatal("resolveTarget on an absent workload returned nil error, want not-found")
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
