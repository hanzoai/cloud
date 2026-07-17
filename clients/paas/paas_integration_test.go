//go:build paasintegration

// Integration probe against a REAL cluster. Not part of the normal unit suite —
// it is gated behind the `paasintegration` build tag AND requires PAAS_IT=1, so
// `go test ./...` never touches a cluster. Run explicitly:
//
//	PAAS_IT=1 go test -tags paasintegration -run TestIntegration ./clients/paas/ -v
//
// It proves the end-to-end deploy path the standalone platform's deploy-executor
// implemented, now native in cloud:
//   - observeFleet lists the operator Service CRs (the drift board) off the live
//     cluster via the KUBECONFIG fallback in newDynamic.
//   - an IDEMPOTENT same-image merge-patch on a low-risk service (pricing) proves
//     the write path reaches the operator WITHOUT changing what runs (same tag =
//     no rollout). It never mutates a tag, so it cannot perturb live state.
package paas

import (
	"context"
	"os"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

const itService = "pricing" // low-risk service CLAUDE.md already validated

func itClient(t *testing.T) *cloud.Service[state] {
	t.Helper()
	if os.Getenv("PAAS_IT") != "1" {
		t.Skip("set PAAS_IT=1 to run the live-cluster integration probe")
	}
	dyn, err := newDynamic()
	if err != nil {
		t.Fatalf("newDynamic (needs a live KUBECONFIG): %v", err)
	}
	return &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Logger: luxlog.New("paas-it")}, "paas"),
		State: state{dyn: dyn},
	}
}

// TestIntegrationObserveFleet lists the real fleet and asserts the board is
// non-empty and self-consistent (every row has org/app/env/registry and a drift
// verdict). It is READ-ONLY.
func TestIntegrationObserveFleet(t *testing.T) {
	s := itClient(t)
	views, err := observeFleet(s, context.Background())
	if err != nil {
		t.Fatalf("observeFleet: %v", err)
	}
	if len(views) == 0 {
		t.Fatalf("expected a non-empty fleet board")
	}
	t.Logf("observed %d service rows across the platform namespaces", len(views))
	var green, red, yellow int
	sawPricing := false
	for _, v := range views {
		if v.Org == "" || v.App == "" || v.Env == "" || v.Registry == "" {
			t.Errorf("incomplete row: %+v", v)
		}
		switch v.Drift.Severity {
		case SeverityOK:
			green++
		case SeverityRed:
			red++
		case SeverityYellow:
			yellow++
		}
		if v.App == itService && v.Env == "main" {
			sawPricing = true
			t.Logf("pricing row: declared=%s health=%s phase=%s drift=%s endpoints=%v",
				v.DeclaredTag, v.Health, v.Phase, v.Drift.Severity, v.Endpoints)
		}
	}
	t.Logf("drift summary: ok=%d yellow=%d red=%d", green, yellow, red)
	if !sawPricing {
		t.Errorf("expected to observe the %q service in ns hanzo", itService)
	}
}

// TestIntegrationIdempotentDeploy proves the deploy WRITE path reaches the
// operator without changing live state: it reads pricing's CURRENT tag and
// re-patches the CR to the SAME tag. Same image ⇒ the operator sees no change ⇒
// no rollout. This exercises the exact merge-patch the deploy handler issues.
func TestIntegrationIdempotentDeploy(t *testing.T) {
	s := itClient(t)
	ctx := context.Background()

	before, err := s.State.dyn.Resource(appsGVR).Namespace("hanzo").Get(ctx, itService, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s before: %v", itService, err)
	}
	tag, _, _ := unstructured.NestedString(before.Object, "spec", "image", "tag")
	repo, _, _ := unstructured.NestedString(before.Object, "spec", "image", "repository")
	genBefore := before.GetGeneration()
	t.Logf("pricing before: repo=%s tag=%s generation=%d", repo, tag, genBefore)
	if tag == "" {
		t.Fatalf("pricing CR has no spec.image.tag; refusing to patch")
	}

	// Same-image merge-patch (the identical body the deploy handler builds).
	patch := []byte(`{"spec":{"image":{"tag":"` + tag + `","repository":"` + repo + `","pullPolicy":"Always"}}}`)
	after, err := s.State.dyn.Resource(appsGVR).Namespace("hanzo").
		Patch(ctx, itService, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("idempotent patch: %v", err)
	}
	afterTag, _, _ := unstructured.NestedString(after.Object, "spec", "image", "tag")
	genAfter := after.GetGeneration()
	t.Logf("pricing after:  tag=%s generation=%d", afterTag, genAfter)

	if afterTag != tag {
		t.Fatalf("tag changed by an idempotent patch: %s -> %s", tag, afterTag)
	}
	// A same-spec merge-patch must not bump .metadata.generation (no spec change).
	if genAfter != genBefore {
		t.Errorf("generation moved on a no-op patch: %d -> %d (a rollout may have been triggered)", genBefore, genAfter)
	}
	t.Logf("OK: deploy write-path reached the operator; live state unchanged (no rollout)")
}
