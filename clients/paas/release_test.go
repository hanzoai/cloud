package paas

import (
	"context"
	"github.com/hanzoai/cloud/clients/k8s"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// appCRObj builds a real-shaped operator App CR — the kind the fleet runs on —
// for the fake cluster.
func appCRObj(name, ns, repo, tag string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "App",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"image": map[string]any{"repository": repo, "tag": tag, "pullPolicy": "Always"}},
	}}
}

// fakeService builds a hermetic paas Service backed by an in-memory fake dynamic
// client seeded with objs — the release path is exercised without a real cluster.
func fakeService(objs ...runtime.Object) *cloud.Service[state] {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.Apps:        "AppList",
		k8s.Deployments: "DeploymentList",
		// discoverNamespaces lists namespaces to build the scan set; the fake panics
		// on an unregistered list kind, so it is registered here. Seeding no Namespace
		// objects is the honest hermetic default: discovery finds none and the scan
		// set falls back to the first-party set, which is what these tests assert on.
		k8s.Namespaces: "NamespaceList",
	}, objs...)
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{dyn: dyn, scan: &nsCache{}},
	}
}

// declaredImage reads spec.image.{repository,tag} off the live App CR in the fake.
func declaredImage(t *testing.T, s *cloud.Service[state], ns, name string) (repo, tag, pull string) {
	t.Helper()
	obj, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR %s/%s: %v", ns, name, err)
	}
	repo, _, _ = unstructured.NestedString(obj.Object, "spec", "image", "repository")
	tag, _, _ = unstructured.NestedString(obj.Object, "spec", "image", "tag")
	pull, _, _ = unstructured.NestedString(obj.Object, "spec", "image", "pullPolicy")
	return
}

// TestSplitReleaseImage is the clean-semver determinism gate: repository:tag with
// a strict vX.Y.Z tag passes; every mutable/sha/suffixed/bare form is refused —
// the same policy image-update.yml enforces at the CR bump, now native.
func TestSplitReleaseImage(t *testing.T) {
	ok := []struct{ image, repo, tag string }{
		{"ghcr.io/hanzoai/cloud:v1.799.16", "ghcr.io/hanzoai/cloud", "v1.799.16"},
		{"ghcr.io/hanzoai/hanzo-app:v1.42.15", "ghcr.io/hanzoai/hanzo-app", "v1.42.15"},
		{"  ghcr.io/hanzoai/iam:v0.3.0  ", "ghcr.io/hanzoai/iam", "v0.3.0"},
	}
	for _, c := range ok {
		repo, tag, err := splitReleaseImage(c.image)
		if err != nil {
			t.Errorf("splitReleaseImage(%q) unexpected err: %v", c.image, err)
			continue
		}
		if repo != c.repo || tag != c.tag {
			t.Errorf("splitReleaseImage(%q) = (%q,%q), want (%q,%q)", c.image, repo, tag, c.repo, c.tag)
		}
	}
	bad := []string{
		"",                                    // empty
		"ghcr.io/hanzoai/cloud",               // no tag
		"ghcr.io/hanzoai/cloud:latest",        // mutable
		"ghcr.io/hanzoai/cloud:main",          // mutable
		"ghcr.io/hanzoai/cloud:1.799.16",      // bare (no v)
		"ghcr.io/hanzoai/cloud:v1.799.16-mt",  // suffixed
		"ghcr.io/hanzoai/cloud:sha-08d2dea",   // sha
		"ghcr.io/hanzoai/cloud@sha256:abc123", // digest
		"ghcr.io/hanzoai/cloud:v1.799",        // not X.Y.Z
	}
	for _, image := range bad {
		if _, _, err := splitReleaseImage(image); err == nil {
			t.Errorf("splitReleaseImage(%q) = nil err, want rejection", image)
		}
	}
}

// TestReleaseServiceRefusesAGitDeclaredApp proves the seam refuses a real App CR
// (git-declared, selfHeal-reverted): a valid semver image yields the refusal that
// names where to commit the tag, and the CR is left untouched.
func TestReleaseServiceRefusesAGitDeclaredApp(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	ns, tag, changed, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.800.0")
	if err == nil || changed {
		t.Fatalf("git-declared App: got (changed=%v,err=%v), want (false, refusal)", changed, err)
	}
	if ns != "hanzo" || tag != "v1.800.0" {
		t.Errorf("refusal should still report (ns=hanzo,tag=v1.800.0), got (%q,%q)", ns, tag)
	}
	if !strings.Contains(err.Error(), "declared in git") || !strings.Contains(err.Error(), "crs/cloud.yaml") {
		t.Errorf("refusal must name where to commit; got %q", err)
	}
	// CR must be unchanged.
	if _, gotTag, _ := declaredImage(t, s, "hanzo", "cloud"); gotTag != "v1.799.16" {
		t.Errorf("CR tag mutated to %q on a refused release", gotTag)
	}
}

// TestReleaseServiceRejectsFloating proves a non-semver image is refused by the
// clean-semver gate BEFORE the App is even resolved — the CR is left untouched.
func TestReleaseServiceRejectsFloating(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	if _, _, changed, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:latest"); err == nil || changed {
		t.Fatalf("floating tag: got (changed=%v,err=%v), want (false, error)", changed, err)
	}
	// CR must be unchanged.
	if _, tag, _ := declaredImage(t, s, "hanzo", "cloud"); tag != "v1.799.16" {
		t.Errorf("CR tag mutated to %q on a rejected release", tag)
	}
}

// TestReleaseServiceUnknown proves a service with no operator CR is an honest
// error, never a silent success.
func TestReleaseServiceUnknown(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	if _, _, changed, err := releaseService(s, context.Background(), "ghost", "ghcr.io/hanzoai/ghost:v1.0.0"); err == nil || changed {
		t.Fatalf("unknown service: got (changed=%v,err=%v), want (false, error)", changed, err)
	}
}

// TestReleaseServiceMainFirst proves a bare release resolves the production
// namespace (hanzo) before test/dev, even when the App exists in several — the
// refusal reports that namespace.
func TestReleaseServiceMainFirst(t *testing.T) {
	s := fakeService(
		appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"),
		appCRObj("cloud", "hanzo-testnet", "ghcr.io/hanzoai/cloud", "v1.799.16"),
	)
	ns, _, changed, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.800.0")
	if err == nil || changed {
		t.Fatalf("git-declared App: got (changed=%v,err=%v), want (false, refusal)", changed, err)
	}
	if ns != "hanzo" {
		t.Fatalf("resolved ns = %q, want hanzo (main first)", ns)
	}
}

// TestReleaseServiceFailClosed proves that with no cluster client the release
// fails closed (never a fabricated success).
func TestReleaseServiceFailClosed(t *testing.T) {
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{initErr: "no cluster (test)"}}
	if _, _, changed, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.0.0"); err == nil || changed {
		t.Fatalf("no cluster: got (changed=%v,err=%v), want (false, error)", changed, err)
	}
}

// TestRegisterReleaserRoundTrip proves the build.go inversion seam: after
// registerReleaser, cloud.OnServiceRelease dispatches to the paas primitive — the
// path clients/platform/release.go drives on a self-release. The App is
// git-declared, so the dispatch surfaces the refusal and the CR is untouched.
func TestRegisterReleaserRoundTrip(t *testing.T) {
	s := fakeService(appCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	registerReleaser(s)
	t.Cleanup(func() { cloud.RegisterServiceReleaser(nil) })

	if !cloud.ServiceReleaserRegistered() {
		t.Fatal("ServiceReleaserRegistered() = false after registerReleaser")
	}
	err := cloud.OnServiceRelease(context.Background(), cloud.ServiceReleaseEvent{
		Service: "cloud", Image: "ghcr.io/hanzoai/cloud:v1.801.0", SHA: "deadbeef",
	})
	if err == nil {
		t.Fatal("OnServiceRelease dispatched to a git-declared App; want the refusal error")
	}
	if _, tag, _ := declaredImage(t, s, "hanzo", "cloud"); tag != "v1.799.16" {
		t.Errorf("CR tag = %q after OnServiceRelease, want v1.799.16 (untouched)", tag)
	}
}
