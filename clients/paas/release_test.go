package paas

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// serviceCRObj builds a real-shaped operator Service CR for the fake cluster.
func serviceCRObj(name, ns, repo, tag string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"image": map[string]any{"repository": repo, "tag": tag, "pullPolicy": "Always"}},
	}}
}

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
// client seeded with objs — the release patch is exercised without a real cluster.
// Both workload kinds are registered, so a test can seed either.
func fakeService(objs ...runtime.Object) *cloud.Service[state] {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		appsGVR:        "AppList",
		servicesGVR:    "ServiceList",
		deploymentsGVR: "DeploymentList",
	}, objs...)
	return &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{dyn: dyn}}
}

// declaredImage reads spec.image.{repository,tag} off the live CR in the fake.
func declaredImage(t *testing.T, s *cloud.Service[state], ns, name string) (repo, tag, pull string) {
	t.Helper()
	obj, err := s.State.dyn.Resource(servicesGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
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

// TestReleaseServicePatchesCR proves the keystone: a proven image patches the
// matching Service CR's spec.image.tag so the operator rolls it out.
func TestReleaseServicePatchesCR(t *testing.T) {
	s := fakeService(serviceCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	ns, tag, changed, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.800.0")
	if err != nil {
		t.Fatalf("releaseService: %v", err)
	}
	if ns != "hanzo" || tag != "v1.800.0" || !changed {
		t.Fatalf("releaseService = (ns=%q,tag=%q,changed=%v), want (hanzo,v1.800.0,true)", ns, tag, changed)
	}
	repo, gotTag, pull := declaredImage(t, s, "hanzo", "cloud")
	if repo != "ghcr.io/hanzoai/cloud" || gotTag != "v1.800.0" || pull != "Always" {
		t.Errorf("CR image = (%q,%q,%q), want (ghcr.io/hanzoai/cloud,v1.800.0,Always)", repo, gotTag, pull)
	}
}

// TestReleaseServiceIdempotent proves re-firing the SAME image is a no-op (no
// operator churn): changed=false, no error.
func TestReleaseServiceIdempotent(t *testing.T) {
	s := fakeService(serviceCRObj("iam", "hanzo", "ghcr.io/hanzoai/iam", "v1.28.16"))
	ns, tag, changed, err := releaseService(s, context.Background(), "iam", "ghcr.io/hanzoai/iam:v1.28.16")
	if err != nil {
		t.Fatalf("releaseService: %v", err)
	}
	if ns != "hanzo" || tag != "v1.28.16" || changed {
		t.Fatalf("idempotent release = (ns=%q,tag=%q,changed=%v), want (hanzo,v1.28.16,false)", ns, tag, changed)
	}
}

// TestReleaseServiceRejectsFloating proves a non-semver image is refused BEFORE
// any patch — the CR is left untouched.
func TestReleaseServiceRejectsFloating(t *testing.T) {
	s := fakeService(serviceCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
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
	s := fakeService(serviceCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	if _, _, changed, err := releaseService(s, context.Background(), "ghost", "ghcr.io/hanzoai/ghost:v1.0.0"); err == nil || changed {
		t.Fatalf("unknown service: got (changed=%v,err=%v), want (false, error)", changed, err)
	}
}

// TestReleaseServiceMainFirst proves a bare release resolves the production
// namespace (hanzo) before test/dev, even when the CR exists in several.
func TestReleaseServiceMainFirst(t *testing.T) {
	s := fakeService(
		serviceCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"),
		serviceCRObj("cloud", "hanzo-testnet", "ghcr.io/hanzoai/cloud", "v1.799.16"),
	)
	ns, _, _, err := releaseService(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.800.0")
	if err != nil {
		t.Fatalf("releaseService: %v", err)
	}
	if ns != "hanzo" {
		t.Fatalf("resolved ns = %q, want hanzo (main first)", ns)
	}
	// testnet CR must be untouched (bare release targets production only).
	if _, tag, _ := declaredImage(t, s, "hanzo-testnet", "cloud"); tag != "v1.799.16" {
		t.Errorf("testnet CR mutated to %q by a production release", tag)
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
// registerReleaser, cloud.OnServiceRelease dispatches to the paas primitive and
// patches the CR — the path clients/platform/release.go drives on a self-release.
func TestRegisterReleaserRoundTrip(t *testing.T) {
	s := fakeService(serviceCRObj("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.799.16"))
	registerReleaser(s)
	t.Cleanup(func() { cloud.RegisterServiceReleaser(nil) })

	if !cloud.ServiceReleaserRegistered() {
		t.Fatal("ServiceReleaserRegistered() = false after registerReleaser")
	}
	if err := cloud.OnServiceRelease(context.Background(), cloud.ServiceReleaseEvent{
		Service: "cloud", Image: "ghcr.io/hanzoai/cloud:v1.801.0", SHA: "deadbeef",
	}); err != nil {
		t.Fatalf("OnServiceRelease: %v", err)
	}
	if _, tag, _ := declaredImage(t, s, "hanzo", "cloud"); tag != "v1.801.0" {
		t.Errorf("CR tag = %q after OnServiceRelease, want v1.801.0", tag)
	}
}
