package platform

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestJobOutcome locks the ONE terminal-state classifier the build cap and the
// reconciler both rely on: a Job is done+succeeded only on succeeded>0 or a true
// Complete condition; done+failed on failed>0 or a true Failed condition; not
// done while still running.
func TestJobOutcome(t *testing.T) {
	job := func(status map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"status": status}}
	}
	cases := []struct {
		name          string
		status        map[string]any
		wantDone      bool
		wantSucceeded bool
	}{
		{"succeeded-count", map[string]any{"succeeded": int64(1)}, true, true},
		{"failed-count", map[string]any{"failed": int64(1)}, true, false},
		{"running", map[string]any{"active": int64(1)}, false, false},
		{"empty", map[string]any{}, false, false},
		{"complete-condition", map[string]any{"conditions": []any{
			map[string]any{"type": "Complete", "status": "True"},
		}}, true, true},
		{"failed-condition", map[string]any{"conditions": []any{
			map[string]any{"type": "Failed", "status": "True"},
		}}, true, false},
		{"complete-condition-false", map[string]any{"conditions": []any{
			map[string]any{"type": "Complete", "status": "False"},
		}}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done, succeeded := jobOutcome(job(c.status))
			if done != c.wantDone || succeeded != c.wantSucceeded {
				t.Fatalf("jobOutcome=(%v,%v) want (%v,%v)", done, succeeded, c.wantDone, c.wantSucceeded)
			}
			// jobFinished must agree with jobOutcome's done bit.
			if jobFinished(job(c.status)) != c.wantDone {
				t.Fatalf("jobFinished disagrees with jobOutcome for %s", c.name)
			}
		})
	}
}

// TestListBuildingDeployments proves the reconciler input query returns ONLY
// "building" deployments, spanning orgs, oldest-first — the restart-safe basis
// for resuming in-flight git builds after a cloud restart.
func TestListBuildingDeployments(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seed := func(org, appSlug, depID, status string, created int64) {
		_ = s.CreateProject(ctx, mkProject(org, "proj", "Proj"))
		proj, _ := s.GetProject(ctx, org, "proj")
		a := mkApp(org, proj.ID, appSlug)
		_ = s.CreateApplication(ctx, a)
		if err := s.InsertDeployment(ctx, Deployment{
			ID: depID, Org: org, ApplicationID: a.ID, Version: 1, Status: status,
			Source: "git", Image: "img", BuildID: "bld_" + depID, CreatedAt: created, UpdatedAt: created,
		}); err != nil {
			t.Fatalf("seed %s: %v", depID, err)
		}
	}

	seed("maxpower", "a", "dep_mp_build", "building", 200)
	seed("acme", "b", "dep_ac_build", "building", 100) // older ⇒ first
	seed("maxpower", "c", "dep_mp_live", "live", 300)
	seed("acme", "d", "dep_ac_err", "error", 400)

	got, err := s.ListBuildingDeployments(ctx)
	if err != nil {
		t.Fatalf("ListBuildingDeployments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 building deployments across orgs, got %d: %+v", len(got), got)
	}
	if got[0].ID != "dep_ac_build" || got[1].ID != "dep_mp_build" {
		t.Fatalf("want oldest-first [dep_ac_build, dep_mp_build], got [%s, %s]", got[0].ID, got[1].ID)
	}
	for _, d := range got {
		if d.Status != "building" {
			t.Fatalf("non-building deployment leaked: %s status=%s", d.ID, d.Status)
		}
	}
}

// TestFinalizeLiveIsMonotonic locks the store-level version guard (MED-1): the
// ONE finalize that marks an app live NEVER lets an older version overwrite a
// newer one already live. This is the atomic CAS both the image deploy and the
// git build reconciler rely on, so a build-time inversion cannot regress the
// running version.
func TestFinalizeLiveIsMonotonic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_ = s.CreateProject(ctx, mkProject("maxpower", "web", "Web"))
	proj, _ := s.GetProject(ctx, "maxpower", "web")
	a := mkApp("maxpower", proj.ID, "api")
	_ = s.CreateApplication(ctx, a)

	mk := func(v int) Deployment {
		return Deployment{ID: "dep_v" + itoa(v), Org: "maxpower", ApplicationID: a.ID, Version: v,
			Status: "deploying", Source: "git", Image: "img:v" + itoa(v), CreatedAt: int64(v), UpdatedAt: int64(v)}
	}
	for v := 1; v <= 3; v++ {
		if err := s.InsertDeployment(ctx, mk(v)); err != nil {
			t.Fatalf("insert v%d: %v", v, err)
		}
	}

	// First finalize (v2) advances a fresh app (current_deploy empty).
	if adv, err := s.FinalizeLive(ctx, mk(2), "v2", tenantNamespace("maxpower"), 100); err != nil || !adv {
		t.Fatalf("finalize v2 want advanced, got adv=%v err=%v", adv, err)
	}
	if got, _ := s.GetApplicationByID(ctx, "maxpower", a.ID); got.CurrentDeploy != "dep_v2" || got.Status != "live" || got.ImageTag != "v2" {
		t.Fatalf("app must be live@v2, got status=%s current=%s tag=%s", got.Status, got.CurrentDeploy, got.ImageTag)
	}

	// An OLDER version (v1) must be REFUSED — the live version (2) stands.
	if adv, err := s.FinalizeLive(ctx, mk(1), "v1", tenantNamespace("maxpower"), 200); err != nil || adv {
		t.Fatalf("finalize v1 must be refused (older), got adv=%v err=%v", adv, err)
	}
	if got, _ := s.GetApplicationByID(ctx, "maxpower", a.ID); got.CurrentDeploy != "dep_v2" || got.ImageTag != "v2" {
		t.Fatalf("older v1 finalize REGRESSED the app: current=%s tag=%s", got.CurrentDeploy, got.ImageTag)
	}

	// A NEWER version (v3) advances; re-finalizing the same live version is idempotent.
	if adv, err := s.FinalizeLive(ctx, mk(3), "v3", tenantNamespace("maxpower"), 300); err != nil || !adv {
		t.Fatalf("finalize v3 want advanced, got adv=%v err=%v", adv, err)
	}
	if got, _ := s.GetApplicationByID(ctx, "maxpower", a.ID); got.CurrentDeploy != "dep_v3" || got.ImageTag != "v3" {
		t.Fatalf("app must advance to live@v3, got current=%s tag=%s", got.CurrentDeploy, got.ImageTag)
	}
	if adv, err := s.FinalizeLive(ctx, mk(3), "v3", tenantNamespace("maxpower"), 400); err != nil || !adv {
		t.Fatalf("idempotent re-finalize v3 want advanced, got adv=%v err=%v", adv, err)
	}
}

// TestBuildReconcilerRetriesUntilTenantRBACReady is the L2 regression guard: a git
// build whose Job SUCCEEDED but whose tenant is still cold (operator RBAC not yet
// projected) must NOT be permanently failed and must NOT block in-line — the
// reconciler probes readiness once, leaves the deployment "building", and on a later
// tick (once the RoleBinding lands) reconciles it to live. This is the exact
// terminal-on-transient + head-of-line-block bug RED flagged: a fresh tenant whose
// RBAC lags could not self-heal because there is no client to retry a git build.
func TestBuildReconcilerRetriesUntilTenantRBACReady(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	k := fakeK8s()
	fastRBAC(k) // shrink timings; the reconciler probe path must not block regardless
	// Gate RBAC readiness behind a flag the test flips between ticks. A prepended
	// reactor wins over fakeK8s's default-allow, so this fully controls readiness.
	fake := k.dyn.(*dynamicfake.FakeDynamicClient)
	var rbacReady atomic.Bool
	allowSSAR(fake, func() bool { return rbacReady.Load() })
	s := &svc{store: store, k8s: k, log: luxlog.New("test")}

	_ = store.CreateProject(ctx, mkProject("acme", "web", "Web"))
	proj, _ := store.GetProject(ctx, "acme", "web")
	app := mkApp("acme", proj.ID, "api")
	app.Source = "git"
	_ = store.CreateApplication(ctx, app)

	// Seed a git build whose Job has SUCCEEDED, so the ONLY thing gating go-live is
	// the tenant RBAC. CreatedAt is NOW so the build is not overdue (else the
	// not-ready path would honestly fail at the deadline instead of retrying).
	now := time.Now().Unix()
	depID, bldID, job := "dep_1", "bld_1", "pf-build-acme-api-1"
	img := "ghcr.io/hanzoai/tenant-acme/api:main"
	if err := store.InsertBuild(ctx, Build{ID: bldID, Org: "acme", ApplicationID: app.ID, DeploymentID: depID, Status: "building", Image: img, JobName: job, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	if err := store.InsertDeployment(ctx, Deployment{ID: depID, Org: "acme", ApplicationID: app.ID, Version: 1, Status: "building", Source: "git", Image: img, BuildID: bldID, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	jobObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": job, "namespace": k.buildNS},
		"status":   map[string]any{"succeeded": int64(1)},
	}}
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, jobObj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	get := func() Deployment { d, _ := store.GetDeployment(ctx, "acme", app.ID, depID); return d }
	ns := tenantNamespace("acme")

	// Tick 1 — tenant RBAC NOT ready. The deployment must stay "building" (NOT failed),
	// the build must NOT be failed, and no Service CR may exist yet…
	rbacReady.Store(false)
	s.reconcileBuild(ctx, get())
	if d := get(); d.Status != "building" {
		t.Fatalf("tick 1 (RBAC pending) must leave the deployment 'building', got %q (msg=%q)", d.Status, d.Message)
	}
	if b, _ := store.GetBuild(ctx, "acme", bldID); b.Status == "failed" {
		t.Fatal("a transient RBAC-pending must NOT permanently fail the build")
	}
	if _, err := k.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, app.Slug, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no Service CR may be written while RBAC is pending, get err=%v", err)
	}
	// …but the namespace WAS created (that create is what triggers the operator to
	// project cloud-api's RoleBinding), so readiness can ever become true.
	if _, err := k.dyn.Resource(namespacesGVR).Get(ctx, ns, metav1.GetOptions{}); err != nil {
		t.Fatalf("tick 1 must create the tenant namespace (triggers operator RBAC): %v", err)
	}

	// Tick 2 — the operator's RoleBinding has landed → the build reconciles to live.
	rbacReady.Store(true)
	s.reconcileBuild(ctx, get())
	if d := get(); d.Status != "deploying" {
		t.Fatalf("tick 2 (RBAC ready) must advance the deployment to 'deploying', got %q (msg=%q)", d.Status, d.Message)
	}
	if a, _ := store.GetApplicationByID(ctx, "acme", app.ID); a.Status != "live" || a.CurrentDeploy != depID || a.ImageTag != "main" {
		t.Fatalf("app must be live@%s (tag main), got status=%s current=%s tag=%s", depID, a.Status, a.CurrentDeploy, a.ImageTag)
	}
	if b, _ := store.GetBuild(ctx, "acme", bldID); b.Status != "succeeded" {
		t.Fatalf("build must be 'succeeded' after go-live, got %q", b.Status)
	}
	obj, err := k.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, app.Slug, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Service CR must be written once RBAC is ready: %v", err)
	}
	if tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag"); tag != "main" {
		t.Fatalf("live Service CR image tag want 'main', got %q", tag)
	}
}

// TestBuildReconcilerVersionMonotonic is the end-to-end MED-1 guard: two git
// builds for the SAME app whose Jobs finish OUT OF ORDER. The newer build (v2)
// completes first and goes live; the older build (v1) then finishes LATE and must
// NOT pin the older commit — its deployment is recorded 'superseded', the app
// stays live@v2, and the running Service CR is never downgraded to v1.
func TestBuildReconcilerVersionMonotonic(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	k := fakeK8s()
	s := &svc{store: store, k8s: k, log: luxlog.New("test")}

	_ = store.CreateProject(ctx, mkProject("maxpower", "web", "Web"))
	proj, _ := store.GetProject(ctx, "maxpower", "web")
	app := mkApp("maxpower", proj.ID, "api")
	app.Source = "git"
	_ = store.CreateApplication(ctx, app)

	seed := func(v int) string {
		depID, bldID := "dep_v"+itoa(v), "bld_v"+itoa(v)
		job := "pf-build-maxpower-api-v" + itoa(v)
		img := "ghcr.io/hanzoai/tenant-maxpower/api:v" + itoa(v)
		if err := store.InsertBuild(ctx, Build{ID: bldID, Org: "maxpower", ApplicationID: app.ID, DeploymentID: depID, Status: "building", Image: img, JobName: job, CreatedAt: int64(v), UpdatedAt: int64(v)}); err != nil {
			t.Fatalf("seed build v%d: %v", v, err)
		}
		if err := store.InsertDeployment(ctx, Deployment{ID: depID, Org: "maxpower", ApplicationID: app.ID, Version: v, Status: "building", Source: "git", Image: img, BuildID: bldID, CreatedAt: int64(v), UpdatedAt: int64(v)}); err != nil {
			t.Fatalf("seed deployment v%d: %v", v, err)
		}
		jobObj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": job, "namespace": k.buildNS},
			"status":   map[string]any{"succeeded": int64(1)},
		}}
		if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, jobObj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed job v%d: %v", v, err)
		}
		return depID
	}
	depV1, depV2 := seed(1), seed(2)
	get := func(id string) Deployment { d, _ := store.GetDeployment(ctx, "maxpower", app.ID, id); return d }

	// INVERSION: the newer build (v2) reconciles FIRST and goes live.
	s.reconcileBuild(ctx, get(depV2))
	if a, _ := store.GetApplicationByID(ctx, "maxpower", app.ID); a.CurrentDeploy != depV2 || a.Status != "live" || a.ImageTag != "v2" {
		t.Fatalf("after v2 want live@v2, got status=%s current=%s tag=%s", a.Status, a.CurrentDeploy, a.ImageTag)
	}
	if d := get(depV2); d.Status != "deploying" {
		t.Fatalf("v2 deployment want 'deploying', got %s", d.Status)
	}

	// The older build (v1) finishes LATE — it must not regress the live version.
	s.reconcileBuild(ctx, get(depV1))
	a, _ := store.GetApplicationByID(ctx, "maxpower", app.ID)
	if a.CurrentDeploy != depV2 || a.ImageTag != "v2" {
		t.Fatalf("late older build v1 REGRESSED live: want current=%s tag=v2, got current=%s tag=%s", depV2, a.CurrentDeploy, a.ImageTag)
	}
	if d := get(depV1); d.Status != "superseded" {
		t.Fatalf("late older deployment v1 want 'superseded', got %s", d.Status)
	}
	if b, _ := store.GetBuild(ctx, "maxpower", "bld_v1"); b.Status != "succeeded" {
		t.Fatalf("v1 image built fine → build 'succeeded', got %s", b.Status)
	}
	// The live workload CR still runs v2 — the older image was never applied.
	obj, err := k.dyn.Resource(servicesGVR).Namespace("tenant-maxpower").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service CR missing: %v", err)
	}
	if tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag"); tag != "v2" {
		t.Fatalf("live workload must stay at v2, got tag %q", tag)
	}
}
