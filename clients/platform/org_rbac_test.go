package platform

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/clients/k8s"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// fastRBAC shrinks the readiness-wait timings so the race-window tests run in
// milliseconds instead of the production 45s ceiling. Zero fields fall back to the
// production constants (see waitForTenantRBAC), so only tests set these.
func fastRBAC(k *k8sClient) {
	k.rbacReadyTimeout = 2 * time.Second
	k.rbacPollInitial = 1 * time.Millisecond
}

// TestFirstDeployWaitsForOrgRBAC is the cold-start-race regression guard: a
// brand-new tenant's namespace is created, but cloud-api's `cloud-api-platform`
// RoleBinding is projected by the operator ASYNCHRONOUSLY. The deploy path must
// POLL its own access (SelfSubjectAccessReview) and only touch the ResourceQuota /
// Service CR once the grant has landed — so the FIRST deploy succeeds end-to-end
// without a manual retry, exactly the failure found live.
func TestFirstDeployWaitsForOrgRBAC(t *testing.T) {
	k := fakeK8s()
	fastRBAC(k)
	fake := k.dyn.(*dynamicfake.FakeDynamicClient)

	// The operator's RoleBinding lands after the 3rd probe: the first 3 access
	// reviews are denied (RBAC not yet ready), the 4th is allowed. The counter also
	// proves the gate actually polled rather than proceeding blind.
	const denials = 3
	var probes atomic.Int32
	allowSSAR(fake, func() bool { return probes.Add(1) > denials })

	app := mkApp("acme", "proj_1", "api")
	app.Source, app.ImageRepo, app.Replicas = "image", "ghcr.io/hanzoai/nginx", 1

	if err := k.applyService(context.Background(), "acme", "web", app, "ghcr.io/hanzoai/nginx:1.27"); err != nil {
		t.Fatalf("first deploy into a fresh tenant must succeed after RBAC lands, got: %v", err)
	}

	if got := probes.Load(); got != denials+1 {
		t.Fatalf("expected exactly %d access-review probes (deny×%d then allow), got %d", denials+1, denials, got)
	}

	ns := tenantNamespace("acme")
	// The whole deploy completed in-flight: the quota AND the operator Service CR
	// exist in the tenant namespace (nothing was skipped by the wait).
	if _, err := fake.Resource(resourceQuotasGVR).Namespace(ns).Get(context.Background(), tenantQuotaName, metav1.GetOptions{}); err != nil {
		t.Fatalf("ResourceQuota must be created once RBAC is ready: %v", err)
	}
	if _, err := fake.Resource(k8s.Apps).Namespace(ns).Get(context.Background(), app.Slug, metav1.GetOptions{}); err != nil {
		t.Fatalf("Service CR must be created in %s: %v", ns, err)
	}
}

// TestFirstDeployFailsClosedWhenRBACNeverLands proves the wait is BOUNDED and fails
// CLOSED: if the operator never grants access within the window, the deploy returns
// an honest retryable error (→ HTTP 503) and writes NO quota or Service CR — never a
// fabricated success.
func TestFirstDeployFailsClosedWhenRBACNeverLands(t *testing.T) {
	k := fakeK8s()
	k.rbacReadyTimeout = 30 * time.Millisecond
	k.rbacPollInitial = 5 * time.Millisecond
	fake := k.dyn.(*dynamicfake.FakeDynamicClient)
	allowSSAR(fake, func() bool { return false }) // access never granted

	app := mkApp("acme", "proj_1", "api")
	app.Source, app.ImageRepo, app.Replicas = "image", "ghcr.io/hanzoai/nginx", 1

	err := k.applyService(context.Background(), "acme", "web", app, "ghcr.io/hanzoai/nginx:1.27")
	if err == nil {
		t.Fatal("deploy must fail closed when tenant RBAC never lands, got nil")
	}
	if !errors.Is(err, errTenantProvisioning) {
		t.Fatalf("timeout must surface errTenantProvisioning, got: %v", err)
	}
	if got := deployErrStatus(err); got != http.StatusServiceUnavailable {
		t.Fatalf("provisioning error must map to 503 (retryable), got %d", got)
	}

	ns := tenantNamespace("acme")
	// Fail closed: no privileged object was written past the readiness gate.
	if _, gErr := fake.Resource(resourceQuotasGVR).Namespace(ns).Get(context.Background(), tenantQuotaName, metav1.GetOptions{}); !apierrors.IsNotFound(gErr) {
		t.Fatalf("no ResourceQuota may be written when RBAC is not ready, get err=%v", gErr)
	}
	if _, gErr := fake.Resource(k8s.Apps).Namespace(ns).Get(context.Background(), app.Slug, metav1.GetOptions{}); !apierrors.IsNotFound(gErr) {
		t.Fatalf("no Service CR may be written when RBAC is not ready, get err=%v", gErr)
	}
}

// TestOrgRBACWaitAbortsOnContextCancel proves the wait honors ctx cancellation
// (client disconnect / server shutdown): it returns promptly with the context error,
// NOT the timeout error, and does not spin to the full deadline.
func TestOrgRBACWaitAbortsOnContextCancel(t *testing.T) {
	k := fakeK8s()
	k.rbacReadyTimeout = 10 * time.Second // long, so only cancellation can end the wait
	k.rbacPollInitial = 5 * time.Millisecond
	fake := k.dyn.(*dynamicfake.FakeDynamicClient)
	allowSSAR(fake, func() bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()

	start := time.Now()
	err := k.waitForTenantRBAC(ctx, tenantNamespace("acme"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait must return context.Canceled, got: %v", err)
	}
	if errors.Is(err, errTenantProvisioning) {
		t.Fatal("a cancelled wait must NOT masquerade as a provisioning timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel must abort promptly, waited %s", elapsed)
	}
}

// TestExistingOrgDeployHasNoWait is the auto-glue no-regression guard: when
// cloud-api's RBAC is already present (an onboarded tenant), the readiness gate
// resolves on the FIRST probe with no sleep — the fast deploy path is unchanged.
func TestExistingOrgDeployHasNoWait(t *testing.T) {
	k := fakeK8s()
	fake := k.dyn.(*dynamicfake.FakeDynamicClient) // production timings (unshrunk)

	var probes atomic.Int32
	allowSSAR(fake, func() bool { probes.Add(1); return true }) // RBAC already granted

	start := time.Now()
	if err := k.waitForTenantRBAC(context.Background(), tenantNamespace("maxpower")); err != nil {
		t.Fatalf("ready tenant must pass immediately, got: %v", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("ready tenant must resolve in exactly ONE probe (no wait), got %d probes", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ready-tenant probe must not sleep, took %s", elapsed)
	}
}

// TestFreshOrgDeployFailsClosedOn503NotCreate502 confirms the I2 fresh-org path at
// the HTTP layer: a deploy into a genuinely fresh org (whose namespace does NOT
// exist yet) CREATES the namespace — the trigger for the operator's async RoleBinding
// — and then, when RBAC has not yet landed, fails closed with a graceful, RETRYABLE
// 503, NOT a raw 502 at namespace-Create. The presence of the created namespace
// proves the 503 means "RBAC pending", never "could not create the namespace".
func TestFreshOrgDeployFailsClosedOn503NotCreate502(t *testing.T) {
	k := fakeK8s()
	k.rbacReadyTimeout = 30 * time.Millisecond
	k.rbacPollInitial = 5 * time.Millisecond
	fake := k.dyn.(*dynamicfake.FakeDynamicClient)
	allowSSAR(fake, func() bool { return false }) // RBAC never lands
	app := mountAppK8s(t, k)

	seedProject(t, app, "freshco", "web")
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "freshco", map[string]any{
		"name": "api", "source": "image",
		"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1.27"},
	})

	// Precondition: the tenant namespace does NOT exist (genuinely fresh org).
	ns := tenantNamespace("freshco")
	if _, err := k.dyn.Resource(k8s.Namespaces).Get(context.Background(), ns, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("precondition: fresh org namespace must not exist yet, get err=%v", err)
	}

	code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "freshco", map[string]any{"tag": "1.27"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("fresh-org deploy (RBAC pending) want 503, got %d (%s)", code, body)
	}
	// The namespace WAS created — so the 503 is RBAC-pending, not a Create failure (502).
	if _, err := k.dyn.Resource(k8s.Namespaces).Get(context.Background(), ns, metav1.GetOptions{}); err != nil {
		t.Fatalf("fresh-org deploy must CREATE the namespace (503 = RBAC pending, not create-502): %v", err)
	}
	// No Service CR was written past the readiness gate (fail-closed).
	if _, err := k.dyn.Resource(k8s.Apps).Namespace(ns).Get(context.Background(), "api", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no Service CR may exist when RBAC never lands, get err=%v", err)
	}
	// The failed attempt is recorded honestly as an 'error' deployment (not fabricated).
	code, listBody := do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api/deployments", "freshco", nil)
	var deps []deploymentView
	_ = json.Unmarshal(listBody, &deps)
	if code != http.StatusOK || len(deps) != 1 || deps[0].Status != "error" {
		t.Fatalf("fresh-org 503 must record one honest 'error' deployment, got code=%d deps=%+v", code, deps)
	}
}
