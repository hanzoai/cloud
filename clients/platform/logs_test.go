package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// pod is a tiny builder for a labeled, started pod in ns.
func pod(name, ns string, labels map[string]string, started time.Time) *corev1.Pod {
	st := metav1.NewTime(started)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, CreationTimestamp: st},
		Status:     corev1.PodStatus{StartTime: &st},
	}
}

// clientsetK8s returns a k8sClient whose TYPED client is a fake seeded with pods
// (the fake's GetLogs stream yields the literal "fake logs"). dyn is nil — the log
// path uses only the typed client, and ready() is not on the log path.
func clientsetK8s(pods ...*corev1.Pod) *k8sClient {
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}
	return &k8sClient{clientset: k8sfake.NewSimpleClientset(objs...), buildNS: "hanzo", limits: testLimits()}
}

func TestBuildLogs_StreamsNewestJobPod(t *testing.T) {
	old := pod("pf-build-acme-web-abc-0", "hanzo",
		map[string]string{"job-name": "pf-build-acme-web-abc"}, time.Unix(1000, 0))
	// A retry pod started later must win (newest rollout).
	newer := pod("pf-build-acme-web-abc-1", "hanzo",
		map[string]string{"job-name": "pf-build-acme-web-abc"}, time.Unix(2000, 0))
	// An unrelated pod must be ignored.
	other := pod("someone-else", "hanzo", map[string]string{"job-name": "other"}, time.Unix(3000, 0))

	k := clientsetK8s(old, newer, other)
	logs, ok := k.buildLogs(context.Background(), "pf-build-acme-web-abc")
	if !ok || !strings.Contains(logs, "fake logs") {
		t.Fatalf("buildLogs: want the fake pod-log stream, got ok=%v logs=%q", ok, logs)
	}
}

func TestBuildLogs_NoPodYet_Degrades(t *testing.T) {
	k := clientsetK8s() // no pods
	if logs, ok := k.buildLogs(context.Background(), "pf-build-acme-web-abc"); ok || logs != "" {
		t.Fatalf("buildLogs with no pod: want ('',false), got (%q,%v)", logs, ok)
	}
	// No job name → never queries the cluster.
	if _, ok := k.buildLogs(context.Background(), ""); ok {
		t.Fatalf("buildLogs with empty jobName should not query")
	}
}

func TestAppLogs_ScopedToTenantNamespace(t *testing.T) {
	ns := tenantNamespace("acme")
	appPod := pod("web-7d9-abc", ns, map[string]string{"app.kubernetes.io/instance": "web"}, time.Unix(5000, 0))
	// Same-labeled pod in ANOTHER tenant's namespace must never be read for acme.
	otherPod := pod("web-victim", tenantNamespace("victim"),
		map[string]string{"app.kubernetes.io/instance": "web"}, time.Unix(9000, 0))

	k := clientsetK8s(appPod, otherPod)
	logs, ok := k.appLogs(context.Background(), "acme", "web")
	if !ok || !strings.Contains(logs, "fake logs") {
		t.Fatalf("appLogs: want acme's pod logs, got ok=%v logs=%q", ok, logs)
	}
	// A non-existent app in acme must not resolve another tenant's pod.
	if _, ok := k.appLogs(context.Background(), "acme", "nonexistent"); ok {
		t.Fatalf("appLogs for a non-existent app must not resolve another tenant's pod")
	}
}

func TestAppLogs_NoClientset_Degrades(t *testing.T) {
	k := &k8sClient{clientset: nil, buildNS: "hanzo", limits: testLimits()}
	if logs, ok := k.appLogs(context.Background(), "acme", "web"); ok || logs != "" {
		t.Fatalf("appLogs with no typed client: want ('',false), got (%q,%v)", logs, ok)
	}
}

func TestNewestPod_OrdersByStartTimeThenName(t *testing.T) {
	pods := []corev1.Pod{
		*pod("b", "n", nil, time.Unix(100, 0)),
		*pod("a", "n", nil, time.Unix(200, 0)),
		*pod("c", "n", nil, time.Unix(200, 0)), // tie with a → name breaks (c > a)
	}
	if got := newestPod(pods); got != "c" {
		t.Fatalf("newestPod: want c (newest, name tiebreak), got %q", got)
	}
}

func TestByteCount(t *testing.T) {
	for in, want := range map[int]string{512: "512 B", 256 << 10: "256 KiB", 3 << 20: "3 MiB"} {
		if got := byteCount(in); got != want {
			t.Errorf("byteCount(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestDeploymentLogs_IncludesLiveBuildLogs proves the /v1/platform deploymentLogs
// handler now surfaces REAL build-pod logs (not the old "phase 2" placeholder) and
// stamps source=build for a git deployment.
func TestDeploymentLogs_IncludesLiveBuildLogs(t *testing.T) {
	buildPod := pod("pf-build-acme-api-bld1-xyz", "hanzo",
		map[string]string{"job-name": "pf-build-acme-api-bld1"}, time.Unix(1, 0))
	app, s := mountSvcK8s(t, clientsetK8s(buildPod))
	ctx := context.Background()
	org := "acme"

	seedGitDeployment(t, s, org, "pf-build-acme-api-bld1")

	body := getLogs(t, app, org, "web", "api", "dep_1")
	if body.Source != "build" {
		t.Fatalf("source: want build, got %q (logs=%q)", body.Source, body.Logs)
	}
	if !strings.Contains(body.Logs, "fake logs") {
		t.Fatalf("deploymentLogs should include the live build pod logs, got:\n%s", body.Logs)
	}
	if strings.Contains(body.Logs, "phase 2") {
		t.Fatalf("deploymentLogs still has the phase-2 placeholder:\n%s", body.Logs)
	}
	_ = ctx
}

// TestDeploymentLogs_DegradesWithoutPod proves the handler returns the honest
// recorded timeline (source=none) when no pod is reachable — never a fabrication.
func TestDeploymentLogs_DegradesWithoutPod(t *testing.T) {
	app, s := mountSvcK8s(t, clientsetK8s()) // clientset present, NO pods
	org := "acme"
	seedGitDeployment(t, s, org, "pf-build-acme-api-bld1")

	body := getLogs(t, app, org, "web", "api", "dep_1")
	if body.Source != "none" {
		t.Fatalf("no-pod source: want none, got %q", body.Source)
	}
	if !strings.Contains(body.Logs, "not available") {
		t.Fatalf("expected the honest 'not available' note, got:\n%s", body.Logs)
	}
	if strings.Contains(body.Logs, "fake logs") {
		t.Fatalf("no pod exists — logs must not fabricate content:\n%s", body.Logs)
	}
}

// seedGitDeployment inserts a project(web)+git app(api)+building deployment(dep_1)
// with a build whose Job is jobName, so the log handler has a git deployment to read.
func seedGitDeployment(t *testing.T, s *cloud.Service[state], org, jobName string) {
	t.Helper()
	ctx := context.Background()
	// ProjectStore is read-only (projects are IAM's), so seed through the fake itself.
	if _, err := s.State.projects.(*fakeProjects).Create(ctx, org, "web", "Web", ""); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := s.State.store.CreateApplication(ctx, Application{ID: "app_1", Org: org, ProjectID: "web", Slug: "api",
		Name: "API", Source: "git", RepoURL: "https://github.com/acme/api", Status: "building", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	if err := s.State.store.InsertBuild(ctx, Build{ID: "bld_1", Org: org, ApplicationID: "app_1", DeploymentID: "dep_1",
		Status: "building", JobName: jobName, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("InsertBuild: %v", err)
	}
	if err := s.State.store.InsertDeployment(ctx, Deployment{ID: "dep_1", Org: org, ApplicationID: "app_1", Version: 1,
		Status: "building", Source: "git", BuildID: "bld_1", Image: "ghcr.io/hanzoai/tenant-acme/api:main",
		CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("InsertDeployment: %v", err)
	}
}

// logsBody is the deploymentLogs JSON shape.
type logsBody struct {
	DeploymentID string `json:"deploymentId"`
	Source       string `json:"source"`
	Logs         string `json:"logs"`
}

// getLogs drives GET .../deployments/:id/logs as a validated caller for org.
func getLogs(t *testing.T, app *zip.App, org, project, appSlug, depID string) logsBody {
	t.Helper()
	path := "/v1/platform/projects/" + project + "/apps/" + appSlug + "/deployments/" + depID + "/logs"
	code, raw := do(t, app, http.MethodGet, path, org, nil)
	if code != http.StatusOK {
		t.Fatalf("deploymentLogs %s: want 200, got %d (%s)", path, code, raw)
	}
	var body logsBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode logs body: %v (%s)", err, raw)
	}
	return body
}
