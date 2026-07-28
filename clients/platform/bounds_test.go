package platform

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/clients/k8s"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── CRIT-1: build-input validators ────────────────────────────────────────────

func TestValidateRepoURL(t *testing.T) {
	ok := []string{
		"https://github.com/hanzoai/app",
		"https://gitlab.com/group/sub/repo.git",
		"https://bitbucket.org/team/repo",
		"https://raw.github.com/x/y", // subdomain of an allowed apex
	}
	for _, u := range ok {
		if _, err := validateRepoURL(u); err != nil {
			t.Errorf("validateRepoURL(%q) should pass, got %v", u, err)
		}
	}
	bad := map[string]string{
		"":                                  "empty",
		"http://github.com/x/y":             "non-https",
		"git://github.com/x/y":              "non-https scheme",
		"https://attacker.example/x/y":      "non-allowlisted host",
		"https://github.com/x/y;cat /etc":   "shell ';'",
		"https://github.com/x/y|curl":       "shell '|'",
		"https://github.com/x/y --output z": "flag smuggle (space)",
		"https://user:pw@github.com/x/y":    "embedded creds",
		"https://github.com/x/y#frag":       "'#' fragment",
		"https://github.com/x/y`id`":        "backtick",
		"https://github.com/x/y$(id)":       "command sub",
		"ssh://github.com/x/y":              "non-https",
	}
	for u, why := range bad {
		if _, err := validateRepoURL(u); err == nil {
			t.Errorf("validateRepoURL(%q) MUST reject (%s)", u, why)
		}
	}
}

func TestValidateDockerfile(t *testing.T) {
	for _, ok := range []string{"Dockerfile", "docker/Dockerfile", "svc/api/Dockerfile.prod", "a-b_c.d"} {
		if _, err := validateDockerfile(ok); err != nil {
			t.Errorf("validateDockerfile(%q) should pass, got %v", ok, err)
		}
	}
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "a/../b", "df;rm -rf", "df $(id)", "df\nRUN", "", "a//b"} {
		if _, err := validateDockerfile(bad); err == nil {
			t.Errorf("validateDockerfile(%q) MUST reject", bad)
		}
	}
}

func TestValidateGitRef(t *testing.T) {
	for _, ok := range []string{"main", "release/v1.2.3", "feature-x", "deadbeef", "0a1b2c3d4e5f", "v1.0.0"} {
		if _, err := validateGitRef(ok); err != nil {
			t.Errorf("validateGitRef(%q) should pass, got %v", ok, err)
		}
	}
	for _, bad := range []string{"main;curl x|sh", "main #x", "-rf", "a..b", "ref with space", "ref`id`", "ref$(x)", "ref~1", "ref^", "ref:x", "", "a\\b"} {
		if _, err := validateGitRef(bad); err == nil {
			t.Errorf("validateGitRef(%q) MUST reject", bad)
		}
	}
}

// TestHTTPGitAppRejectsUnsafeURL proves createApp rejects an unsafe repo.url at
// the boundary (400) before it is ever persisted.
func TestHTTPGitAppRejectsUnsafeURL(t *testing.T) {
	app := mountApp(t)
	seedProject(t, app, "maxpower", "web")
	code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "git",
		"repo": map[string]any{"url": "https://github.com/x/y;cat /ghcr/config.json #"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("unsafe repo.url want 400, got %d", code)
	}
	// A safe repo.url is accepted.
	code, _ = do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api2", "source": "git",
		"repo": map[string]any{"url": "https://github.com/hanzoai/app", "branch": "main"},
	})
	if code != http.StatusCreated {
		t.Fatalf("safe git app want 201, got %d", code)
	}
}

// ── MED-3: replica clamp ──────────────────────────────────────────────────────

func TestClampReplicas(t *testing.T) {
	r := resourceLimits{maxReplicas: 20}
	cases := map[int]int{-5: 1, 0: 1, 1: 1, 20: 20, 21: 20, 1_000_000: 20}
	for in, want := range cases {
		if got := r.clampReplicas(in); got != want {
			t.Errorf("clampReplicas(%d)=%d want %d", in, got, want)
		}
	}
	// Unset ceiling falls back to the safe default, never 0 (fail-secure).
	if got := (resourceLimits{}).clampReplicas(1_000_000); got != defaultMaxReplicas {
		t.Errorf("unset ceiling must fall back to %d, got %d", defaultMaxReplicas, got)
	}
	if got := (resourceLimits{}).clampReplicas(3); got != 3 {
		t.Errorf("unset ceiling must still honor a sane value, got %d", got)
	}
}

// TestHTTPReplicasClamped proves a tenant cannot request an unbounded replica
// count: createApp stores the clamped value and the rendered Service CR reflects
// it.
func TestHTTPReplicasClamped(t *testing.T) {
	k := fakeK8s()
	app := mountAppK8s(t, k)
	seedProject(t, app, "maxpower", "web")
	_, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"replicas": 1_000_000,
	})
	var av appView
	_ = json.Unmarshal(body, &av)
	if av.Replicas != 20 {
		t.Fatalf("replicas must be clamped to 20, got %d", av.Replicas)
	}
	// Deploy and confirm the rendered CR replica count is bounded too.
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "maxpower", map[string]any{"tag": "1"})
	obj, err := k.dyn.Resource(k8s.Apps).Namespace("tenant-maxpower").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CR: %v", err)
	}
	reps, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if reps != 20 {
		t.Fatalf("CR replicas must be clamped to 20, got %d", reps)
	}
}

// ── MED-3: namespace ResourceQuota + LimitRange ───────────────────────────────

// TestEnsureNamespaceAppliesQuota proves ensureNamespace creates BOTH a
// ResourceQuota and a LimitRange in the tenant namespace, capping the tenant's
// total footprint.
func TestEnsureNamespaceAppliesQuota(t *testing.T) {
	k := fakeK8s()
	ns := tenantNamespace("maxpower")
	if err := k.ensureNamespace(context.Background(), ns, "maxpower"); err != nil {
		t.Fatalf("ensureNamespace: %v", err)
	}
	q, err := k.dyn.Resource(resourceQuotasGVR).Namespace(ns).Get(context.Background(), tenantQuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ResourceQuota not created: %v", err)
	}
	pods, _, _ := unstructured.NestedString(q.Object, "spec", "hard", "pods")
	if pods != "50" {
		t.Fatalf("quota pods want 50, got %q", pods)
	}
	lr, err := k.dyn.Resource(limitRangesGVR).Namespace(ns).Get(context.Background(), tenantLimitRangeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("LimitRange not created: %v", err)
	}
	limits, _, _ := unstructured.NestedSlice(lr.Object, "spec", "limits")
	if len(limits) != 1 {
		t.Fatalf("LimitRange must have one Container limit, got %d", len(limits))
	}
	// Idempotent: a second call must not error (create-or-update).
	if err := k.ensureNamespace(context.Background(), ns, "maxpower"); err != nil {
		t.Fatalf("ensureNamespace second call: %v", err)
	}
}

// ── MED-3: concurrent build cap ───────────────────────────────────────────────

// TestConcurrentBuildCap proves an org cannot spawn more than the configured
// number of in-flight build Jobs: the (maxBuilds+1)-th launch fails with
// errTooManyBuilds and no extra Job is created.
func TestConcurrentBuildCap(t *testing.T) {
	k := fakeK8s() // maxBuilds == 3
	a := mkApp("lux", "proj_x", "api")
	a.Source = "git"
	a.RepoURL = "https://github.com/hanzoai/app"
	a.RepoBranch = "main"
	image := k.buildImageRef("lux", "api", "main")

	for i := 0; i < k.limits.maxBuilds; i++ {
		// Distinct build IDs so each Job gets a distinct (idempotent) name.
		if _, err := k.launchBuildJob(context.Background(), "lux", a, image, "main", "bld_"+itoa(i)); err != nil {
			t.Fatalf("build %d should launch: %v", i, err)
		}
	}
	// The next one is over the cap.
	_, err := k.launchBuildJob(context.Background(), "lux", a, image, "main", "bld_over")
	if err == nil || !strings.Contains(err.Error(), "too many concurrent builds") {
		t.Fatalf("over-cap build want errTooManyBuilds, got %v", err)
	}
	// A DIFFERENT org is unaffected (per-org cap, not global).
	b := mkApp("acme", "proj_y", "api")
	b.Source = "git"
	b.RepoURL = "https://github.com/hanzoai/app"
	b.RepoBranch = "main"
	if _, err := k.launchBuildJob(context.Background(), "acme", b, k.buildImageRef("acme", "api", "main"), "main", "bld_acme"); err != nil {
		t.Fatalf("other org build should launch (per-org cap): %v", err)
	}
}

// TestConcurrentBuildCapIgnoresFinished proves a COMPLETED build Job does not
// count toward the cap (so a busy tenant is not permanently locked out).
func TestConcurrentBuildCapIgnoresFinished(t *testing.T) {
	k := fakeK8s()
	a := mkApp("lux", "proj_x", "api")
	a.Source = "git"
	a.RepoURL = "https://github.com/hanzoai/app"
	a.RepoBranch = "main"
	image := k.buildImageRef("lux", "api", "main")

	// Fill the cap, then mark every Job succeeded.
	for i := 0; i < k.limits.maxBuilds; i++ {
		if _, err := k.launchBuildJob(context.Background(), "lux", a, image, "main", "bld_"+itoa(i)); err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	markAllJobsSucceeded(t, k, "hanzo")
	// Now a new build is allowed again.
	if _, err := k.launchBuildJob(context.Background(), "lux", a, image, "main", "bld_after"); err != nil {
		t.Fatalf("build after completion should launch: %v", err)
	}
}

func markAllJobsSucceeded(t *testing.T, k *k8sClient, ns string) {
	t.Helper()
	list, err := k.dyn.Resource(jobsGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range list.Items {
		obj := list.Items[i].DeepCopy()
		_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "succeeded")
		if _, err := k.dyn.Resource(jobsGVR).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("update job status: %v", err)
		}
	}
}
