package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// ── the model, pure ──────────────────────────────────────────────────────────

// The Application name is the generator's, word for word — measured against the
// names cd.hanzo.ai shows for the live fleet.
func TestApplicationNameMirrorsTheGenerator(t *testing.T) {
	for _, tc := range []struct{ dir, file, want string }{
		{"adnexus", "adx", "adnexus-adx"},
		{"hanzo", "papers", "hanzo-papers"},
		{"hanzo", "hanzo-app", "hanzo-app"},
		{"hanzo", "hanzo-app-base", "hanzo-app-base"},
		{"hanzo", "hanzo-chain-exporter", "hanzo-chain-exporter"},
		{"hanzo-superbase", "superbase", "hanzo-superbase"},
		{"hanzo-zt", "hanzo-zt-mcp-gateway", "hanzo-zt-mcp-gateway"},
		{"hanzo-operator-system", "hanzo-operator", "hanzo-operator-system-hanzo-operator"},
		{"hanzo-build", "buildkitd-node", "hanzo-build-buildkitd-node"},
		{"hanzo-mpc", "node", "hanzo-mpc-node"},
		{"kserve", "kserve-controller-manager", "kserve-controller-manager"},
		{"extract-svc", "extract-svc", "extract-svc"},
		{"zen", "zen", "zen"},
		{"cert-manager", "cert-manager-webhook", "cert-manager-webhook"},
		{"bridge", "bridgehead-x", "bridge-bridgehead-x"},
	} {
		if got := applicationName(tc.dir, tc.file); got != tc.want {
			t.Errorf("applicationName(%q, %q) = %q, want %q", tc.dir, tc.file, got, tc.want)
		}
	}
}

// What runs is the declaration merged with its cluster overlay, as CD renders it:
// the overlay's scalars win and its maps merge. The KMS coordinates are read as
// paths with the chart's defaults, never as values.
func TestTheDeclarationIsTheMergeCDRenders(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "hanzo", "web", `# web — hand-maintained.
partOf: shop
component: web
replicas: 0
image:
  repository: ghcr.io/hanzoai/web
  tag: v1.0.0
kmsSecrets:
- name: web-env
  secretsPath: /web
  keys: [DB_URL]
secrets:
  paths:
  - path: /web-boot
    keys: [SIGNING_KEY]
`)
	writeOverlay(t, root, "hanzo", "web", "replicas: 2\nimage:\n  tag: v1.0.1\n")

	d, err := readDeclaration(root, "hanzo", "web")
	if err != nil {
		t.Fatal(err)
	}
	if d.PartOf != "shop" || d.Component != "web" {
		t.Fatalf("project not read: %+v", d)
	}
	if d.Replicas != 2 || !d.replicasSet || d.Tag != "v1.0.1" || d.Repository != "ghcr.io/hanzoai/web" {
		t.Fatalf("the overlay did not merge over the declaration: %+v", d)
	}
	want := []SecretRef{
		{Project: "hanzo", Env: "prod", Path: "/web", Keys: []string{"DB_URL"}},
		{Project: "hanzo", Env: "prod", Path: "/web-boot", Keys: []string{"SIGNING_KEY"}},
	}
	got, _ := json.Marshal(d.Secrets)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("secrets = %s, want %s", got, exp)
	}
	if d.selector["app.kubernetes.io/instance"] != "web" {
		t.Fatalf("default selector = %v", d.selector)
	}
}

// partOf moves as one line, in the file that decides it; a file that names none
// gains the key under its header comment; every other byte stays.
func TestSetPartOfMovesOneLine(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "hanzo", "a", "# a — keep this.\n\npartOf: old # the product\nimage:\n  repository: r\n  tag: t\n")
	writeDecl(t, root, "hanzo", "b", "# b — keep this.\n# second line.\nimage:\n  repository: r\n  tag: t\n")
	writeDecl(t, root, "hanzo", "c", "partOf: base\nimage:\n  repository: r\n  tag: t\n")
	writeOverlay(t, root, "hanzo", "c", "partOf: overlay\n")

	for name, wantRel := range map[string]string{
		"a": declarePath("hanzo", "a"),
		"b": declarePath("hanzo", "b"),
		"c": overlayPath("hanzo", "c"),
	} {
		rel, changed, err := setPartOf(root, "hanzo", name, "shop")
		if err != nil || !changed || rel != wantRel {
			t.Fatalf("setPartOf(%s) = %q, %v, %v; want %q changed", name, rel, changed, err, wantRel)
		}
	}
	if got := readFile(t, root, declarePath("hanzo", "a")); got != "# a — keep this.\n\npartOf: 'shop' # the product\nimage:\n  repository: r\n  tag: t\n" {
		t.Fatalf("a moved more than its line:\n%s", got)
	}
	if got := readFile(t, root, declarePath("hanzo", "b")); got != "# b — keep this.\n# second line.\npartOf: 'shop'\nimage:\n  repository: r\n  tag: t\n" {
		t.Fatalf("b did not gain the key under its header:\n%s", got)
	}
	if got := readFile(t, root, declarePath("hanzo", "c")); got != "partOf: base\nimage:\n  repository: r\n  tag: t\n" {
		t.Fatalf("c's base changed although its overlay decides the project:\n%s", got)
	}
	if _, changed, err := setPartOf(root, "hanzo", "a", "shop"); err != nil || changed {
		t.Fatalf("a second identical move must change nothing: %v %v", changed, err)
	}
}

// Drift is stated per plane and never guessed: an unreadable cluster is not a
// missing app, a stop is not a missing app, and a digest pin is judged by digest.
func TestAppDrift(t *testing.T) {
	kinds := func(v Verdict) string {
		var k []string
		for _, f := range v.Flags {
			k = append(k, string(f.Kind))
		}
		return strings.Join(k, ",")
	}
	base := Declaration{Name: "web", Repository: "ghcr.io/hanzoai/web", Tag: "v1.0.0"}
	for _, tc := range []struct {
		name    string
		app     ProjectApp
		cluster bool
		want    string
	}{
		{"runs what it declares", ProjectApp{Declaration: base, Running: &Running{Image: "ghcr.io/hanzoai/web:v1.0.0"}}, true, ""},
		{"cd out of sync", ProjectApp{Declaration: base, CD: &CDApp{Name: "hanzo-web", Sync: "OutOfSync"}, Running: &Running{Image: "ghcr.io/hanzoai/web:v1.0.0"}}, true, "out-of-sync"},
		{"pods run another tag", ProjectApp{Declaration: base, Running: &Running{Image: "ghcr.io/hanzoai/web:v0.9.0"}}, true, "un-rolled"},
		{"no pods, replicas unset", ProjectApp{Declaration: base}, true, "missing"},
		{"no pods, stopped on purpose", ProjectApp{Declaration: withReplicas(base, 0)}, true, ""},
		{"cluster unreadable is unknown", ProjectApp{Declaration: base}, false, ""},
		{"a newer release", ProjectApp{Declaration: base, Latest: "v1.2.0", Running: &Running{Image: "ghcr.io/hanzoai/web:v1.0.0"}}, true, "stale"},
		{"digest pin judged by digest", ProjectApp{
			Declaration: Declaration{Name: "web", Repository: "ghcr.io/hanzoai/web", Tag: "v1.0.0", Digest: "sha256:aa"},
			Running:     &Running{Image: "ghcr.io/hanzoai/web:v1.0.0", Digest: "sha256:bb"}}, true, "un-rolled"},
	} {
		if got := kinds(appDrift(tc.app, tc.cluster)); got != tc.want {
			t.Errorf("%s: drift = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func withReplicas(d Declaration, n int) Declaration {
	d.Replicas, d.replicasSet = n, true
	return d
}

// A pod is the app's when it carries every label of the app's selector; the app's
// container is the one running its repository, never a sidecar.
func TestRunningMatchesPodsBySelector(t *testing.T) {
	d := Declaration{Name: "web", Repository: "ghcr.io/hanzoai/web",
		selector: map[string]string{"app.kubernetes.io/instance": "web"}}
	pods := []unstructured.Unstructured{
		*podObj("hanzo", "web-1", map[string]string{"app.kubernetes.io/instance": "web"}, "ghcr.io/hanzoai/web:v1.0.0", true, true),
		*podObj("hanzo", "web-2", map[string]string{"app.kubernetes.io/instance": "web"}, "ghcr.io/hanzoai/web:v1.0.0", false, true),
		*podObj("hanzo", "other", map[string]string{"app.kubernetes.io/instance": "other"}, "ghcr.io/hanzoai/other:v9", true, false),
	}
	r := runningOf(d, pods)
	if r == nil || r.Pods != 2 || r.Ready != 1 || r.Image != "ghcr.io/hanzoai/web:v1.0.0" || r.Digest != "sha256:web" {
		t.Fatalf("running = %+v", r)
	}
	if runningOf(Declaration{selector: map[string]string{"app": "none"}}, pods) != nil {
		t.Fatal("a selector no pod carries matched a pod")
	}
}

// ── the surface ──────────────────────────────────────────────────────────────

// The board groups every declaration by the project its file names, joins CD by
// the values path, reads what the pods run, and states the drift — and an org
// admin sees its own org's projects and nothing of the platform's.
func TestProjectBoard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)

		code, body := doSuper(t, h.app, http.MethodGet, "/v1/platform/projects", nil)
		if code != http.StatusOK {
			t.Fatalf("super list: %d %s", code, body)
		}
		var board projectBoard
		decodeJSON(t, body, &board)
		names := map[string]Project{}
		for _, p := range board.Projects {
			names[p.Org+"/"+p.Name] = p
		}
		insights, ok := names["/insights"]
		if !ok || insights.Apps != 2 || len(insights.Namespaces) != 1 || insights.Namespaces[0] != "hanzo" {
			t.Fatalf("insights = %+v (board %s)", insights, body)
		}
		if insights.Sync.OutOfSync != 1 || insights.Sync.Synced != 1 {
			t.Fatalf("insights sync = %+v", insights.Sync)
		}
		if _, ok := names["acme/shop"]; !ok {
			t.Fatalf("a SuperAdmin must see every owner's projects: %s", body)
		}
		if len(board.Unassigned) != 1 || board.Unassigned[0].Name != "loose" {
			t.Fatalf("unassigned = %+v", board.Unassigned)
		}

		// An org admin sees its own org's projects only.
		code, body = doAdmin(t, h.app, http.MethodGet, "/v1/platform/projects", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("org admin list: %d %s", code, body)
		}
		board = projectBoard{}
		decodeJSON(t, body, &board)
		if len(board.Projects) != 1 || board.Projects[0].Name != "shop" || board.Projects[0].Org != "acme" {
			t.Fatalf("an org admin saw %+v", board.Projects)
		}
		// And may not read the platform's by asking.
		if code, _ := doAdmin(t, h.app, http.MethodGet, "/v1/platform/projects?org=hanzo", "acme", nil); code != http.StatusForbidden {
			t.Fatalf("an org admin read the platform's projects: %d", code)
		}
		// A member with no admin scope reads nothing.
		if code, _ := do(t, h.app, http.MethodGet, "/v1/platform/projects", "acme", nil); code != http.StatusForbidden {
			t.Fatalf("a non-admin read the project board: %d", code)
		}
	})
}

// A project answers each app's four facts: what git declares (merged with the
// overlay), what CD did, what the pods run, and the newest release — and the
// drift between them.
func TestProjectDetail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)
		code, body := doSuper(t, h.app, http.MethodGet, "/v1/platform/projects/insights", nil)
		if code != http.StatusOK {
			t.Fatalf("get project: %d %s", code, body)
		}
		var v projectView
		decodeJSON(t, body, &v)
		if len(v.Members) != 2 {
			t.Fatalf("members = %d", len(v.Members))
		}
		web, worker := v.Members[0], v.Members[1]
		if web.Name != "insights-web" || worker.Name != "insights-worker" {
			t.Fatalf("members out of order: %s, %s", web.Name, worker.Name)
		}
		if web.CD == nil || web.CD.Sync != "Synced" || web.CD.Cluster != "https://kubernetes.default.svc" || web.CD.Applied != "aaa111" {
			t.Fatalf("web cd = %+v", web.CD)
		}
		if web.Running == nil || web.Running.Image != "ghcr.io/hanzoai/insights-web:v2.0.0" || web.Latest != "v2.1.0" {
			t.Fatalf("web running/latest = %+v %q", web.Running, web.Latest)
		}
		if web.Drift.Severity != SeverityYellow || web.Drift.Flags[0].Kind != DriftStale {
			t.Fatalf("web drift = %+v", web.Drift)
		}
		if len(web.Secrets) != 1 || web.Secrets[0].Path != "/insights" {
			t.Fatalf("web secrets = %+v", web.Secrets)
		}
		if worker.Tag != "v2.0.1" || worker.Running == nil || worker.Running.Image != "ghcr.io/hanzoai/insights-worker:v2.0.0" {
			t.Fatalf("worker = %+v", worker)
		}
		if got := worker.Drift.Flags; len(got) != 2 || got[0].Kind != DriftOutOfSync || got[1].Kind != DriftUnrolled {
			t.Fatalf("worker drift = %+v", got)
		}
		if code, _ := doSuper(t, h.app, http.MethodGet, "/v1/platform/projects/nothing", nil); code != http.StatusNotFound {
			t.Fatalf("a project nothing names answered %d", code)
		}
	})
}

// Project CRUD writes the partOf scalars in universe: create names a project on
// the apps it starts with, rename rewrites every member, delete folds members into
// another project, and a move sets one. The default mode is a review branch that
// leaves main untouched.
func TestProjectWritesAreCommitsToUniverse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)

		// Create, on a branch: main is untouched.
		code, body := doSuper(t, h.app, http.MethodPost, "/v1/platform/projects", map[string]any{
			"name": "ops", "apps": []map[string]string{{"org": "hanzo", "name": "loose"}},
		})
		if code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, body)
		}
		var w projectWrite
		decodeJSON(t, body, &w)
		if w.Live || w.Review == "" || w.Commit == "" || len(w.Moved) != 1 || w.Moved[0].From != "" {
			t.Fatalf("create = %+v", w)
		}
		if got := mustGit(t, h.bare, "show", w.Ref+":"+declarePath("hanzo", "loose")); !strings.Contains(got, "partOf: 'ops'") {
			t.Fatalf("the branch does not carry the project:\n%s", got)
		}
		if strings.Contains(showFile(t, h.bare, declarePath("hanzo", "loose")), "partOf") {
			t.Fatal("a branch write reached main")
		}
		// A name that already names apps is 409.
		if code, _ := doSuper(t, h.app, http.MethodPost, "/v1/platform/projects", map[string]any{
			"name": "insights", "apps": []map[string]string{{"org": "hanzo", "name": "loose"}},
		}); code != http.StatusConflict {
			t.Fatalf("creating an existing project answered %d", code)
		}

		// Rename, to main: every member moves in one commit.
		code, body = doSuper(t, h.app, http.MethodPut, "/v1/platform/projects/insights", map[string]any{"name": "analytics", "mode": "commit"})
		if code != http.StatusOK {
			t.Fatalf("rename: %d %s", code, body)
		}
		w = projectWrite{}
		decodeJSON(t, body, &w)
		if !w.Live || len(w.Moved) != 2 {
			t.Fatalf("rename = %+v", w)
		}
		for _, f := range []string{"insights-web", "insights-worker"} {
			if !strings.Contains(showFile(t, h.bare, declarePath("hanzo", f)), "partOf: 'analytics'") {
				t.Fatalf("%s did not move", f)
			}
		}
		if !strings.Contains(showFile(t, h.bare, declarePath("hanzo", "insights-web")), "# insights-web — hand-maintained.") {
			t.Fatal("the rename reflowed a hand-maintained file")
		}
		// Renaming onto an existing project is 409 — that is a fold.
		if code, _ := doSuper(t, h.app, http.MethodPut, "/v1/platform/projects/analytics", map[string]any{"name": "cloud"}); code != http.StatusConflict {
			t.Fatalf("renaming onto a project answered %d", code)
		}

		// Delete folds members into another project, which must exist.
		if code, _ := doSuper(t, h.app, http.MethodDelete, "/v1/platform/projects/analytics?into=ghost", nil); code != http.StatusNotFound {
			t.Fatalf("folding into nothing answered %d", code)
		}
		if code, _ := doSuper(t, h.app, http.MethodDelete, "/v1/platform/projects/analytics", nil); code != http.StatusBadRequest {
			t.Fatalf("a delete naming no destination answered %d", code)
		}
		code, body = doSuper(t, h.app, http.MethodDelete, "/v1/platform/projects/analytics?into=cloud&mode=commit", nil)
		if code != http.StatusOK {
			t.Fatalf("fold: %d %s", code, body)
		}
		if !strings.Contains(showFile(t, h.bare, declarePath("hanzo", "insights-worker")), "partOf: 'cloud'") {
			t.Fatal("the fold did not move the members")
		}

		// Move one app; the overlay decides when it sets the key.
		code, body = doSuper(t, h.app, http.MethodPut, "/v1/platform/apps/cloud/project", map[string]any{"partOf": "core", "org": "hanzo", "mode": "commit"})
		if code != http.StatusOK {
			t.Fatalf("move: %d %s", code, body)
		}
		if got := showFile(t, h.bare, overlayPath("hanzo", "cloud")); !strings.Contains(got, "partOf: 'core'") {
			t.Fatalf("the overlay did not move:\n%s", got)
		}

		// An org admin moves its own org's apps and nothing of the platform's.
		if code, _ := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/cloud/project", "acme", map[string]any{"partOf": "x", "org": "hanzo"}); code != http.StatusForbidden {
			t.Fatalf("an org admin moved a platform app: %d", code)
		}
		if code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/web/project", "acme", map[string]any{"partOf": "store", "mode": "commit"}); code != http.StatusOK {
			t.Fatalf("an org admin moving its own app: %d %s", code, body)
		}
		if code, _ := do(t, h.app, http.MethodPost, "/v1/platform/projects", "acme", map[string]any{"name": "x", "apps": []map[string]string{{"name": "web"}}}); code != http.StatusForbidden {
			t.Fatalf("a non-admin wrote a project: %d", code)
		}
	})
}

// Sync is the deploy step: CD applies the compared commit to one Application,
// initiated by the caller, with its own sync options and no pruning. The diff is
// what that would apply. Both are SuperAdmin — CD's own RBAC — on every transport.
func TestSyncRequestsACDOperation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)

		code, body := doSuper(t, h.app, http.MethodGet, "/v1/platform/apps/insights-worker/diff?org=hanzo", nil)
		if code != http.StatusOK {
			t.Fatalf("diff: %d %s", code, body)
		}
		var diff cdDiff
		decodeJSON(t, body, &diff)
		if diff.Sync != "OutOfSync" || diff.Compared != "bbb222" || len(diff.Pending) != 1 || diff.Pending[0].Kind != "Deployment" || len(diff.Resources) != 2 {
			t.Fatalf("diff = %+v", diff)
		}

		code, body = doSuper(t, h.app, http.MethodPost, "/v1/platform/apps/insights-worker/sync?org=hanzo", nil)
		if code != http.StatusAccepted {
			t.Fatalf("sync: %d %s", code, body)
		}
		obj, err := h.dyn.Resource(k8s.CDApplications).Namespace(cdNamespace).Get(context.Background(), "hanzo-insights-worker", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := nested(obj.Object, "operation", "initiatedBy", "username"); got != "z" {
			t.Errorf("initiatedBy = %q, want the caller", got)
		}
		if got := nested(obj.Object, "operation", "sync", "revision"); got != "bbb222" {
			t.Errorf("sync revision = %q, want the compared commit", got)
		}
		if prune, _, _ := unstructured.NestedBool(obj.Object, "operation", "sync", "prune"); prune {
			t.Error("a platform sync pruned")
		}
		if opts, _, _ := unstructured.NestedStringSlice(obj.Object, "operation", "sync", "syncOptions"); len(opts) != 1 || opts[0] != "ServerSideApply=true" {
			t.Errorf("syncOptions = %v, want the Application's own", opts)
		}
		// A second sync while one is queued is 409.
		if code, _ := doSuper(t, h.app, http.MethodPost, "/v1/platform/apps/insights-worker/sync?org=hanzo", nil); code != http.StatusConflict {
			t.Errorf("a second sync answered %d", code)
		}
		// An org admin — even of its own org — may not sync: CD's RBAC is admin-org.
		if code, _ := doAdmin(t, h.app, http.MethodPost, "/v1/platform/apps/web/sync", "acme", nil); code != http.StatusForbidden {
			t.Errorf("an org admin synced: %d", code)
		}
		// And the MCP transport is gated by the same line.
		for _, op := range []string{"post_platform_apps_by_app_sync", "get_platform_apps_by_app_diff", "get_platform_projects", "get_platform_projects_by_project"} {
			code, body := mcpCall(t, h.app, op, map[string]any{"app": "web", "project": "shop", "org": "acme"})
			if low := strings.ToLower(body); code != 200 || !strings.Contains(low, `"iserror":true`) || (!strings.Contains(low, "admin") && !strings.Contains(low, "forbidden")) {
				t.Errorf("%s over MCP answered a non-admin: %d %s", op, code, body)
			}
		}
	})
}

// ── harness ──────────────────────────────────────────────────────────────────

type projectHarness struct {
	app    *zip.App
	dyn    *dynamicfake.FakeDynamicClient
	bare   string
	remote string
}

// newProjectHarness stands up the delivery surface over a real git remote seeded
// with a small fleet, a fake cluster holding its CD Applications and pods, and a
// fake registry — so every plane the board joins is the real transport or its
// in-memory twin.
func newProjectHarness(t *testing.T) *projectHarness {
	t.Helper()
	return harnessOf(t, map[string]string{
		"charts/app/values/hanzo/insights-web.yaml": `# insights-web — hand-maintained.
partOf: insights
image:
  repository: ghcr.io/hanzoai/insights-web
  tag: v2.0.0
kmsSecrets:
- name: insights-env
  secretsPath: /insights
  keys: [DATABASE_URL]
`,
		"charts/app/values/hanzo/insights-worker.yaml": "partOf: insights\nimage:\n  repository: ghcr.io/hanzoai/insights-worker\n  tag: v2.0.1\n",
		"charts/app/values/hanzo/cloud.yaml":           "partOf: cloud\nimage:\n  repository: ghcr.io/hanzoai/cloud\n  tag: v1.0.0\n",
		"infra/aws/values/hanzo/cloud.yaml":            "partOf: cloud\nreplicas: 2\n",
		"charts/app/values/hanzo/loose.yaml":           "image:\n  repository: ghcr.io/hanzoai/loose\n  tag: v1.0.0\n",
		"charts/app/values/acme/web.yaml":              "partOf: shop\nimage:\n  repository: ghcr.io/acme/web\n  tag: v1.0.0\n",
		"infra/k8s/hanzo-cd/applicationset-fleet.yaml": fleetSetFixture,
	},
		cdApplication("hanzo-insights-web", "values/hanzo/insights-web.yaml", "Synced", "aaa111", nil),
		cdApplication("hanzo-insights-worker", "values/hanzo/insights-worker.yaml", "OutOfSync", "bbb222", []any{
			map[string]any{"group": "apps", "kind": "Deployment", "namespace": "hanzo", "name": "insights-worker", "status": "OutOfSync", "health": map[string]any{"status": "Healthy"}},
			map[string]any{"kind": "Service", "namespace": "hanzo", "name": "insights-worker", "status": "Synced"},
		}),
		podObj("hanzo", "insights-web-1", instance("insights-web"), "ghcr.io/hanzoai/insights-web:v2.0.0", true, true),
		podObj("hanzo", "insights-worker-1", instance("insights-worker"), "ghcr.io/hanzoai/insights-worker:v2.0.0", true, true),
		podObj("hanzo", "cloud-1", instance("cloud"), "ghcr.io/hanzoai/cloud:v1.0.0", true, true),
		podObj("acme", "web-1", instance("web"), "ghcr.io/acme/web:v1.0.0", true, true),
	)
}

// harnessOf is newProjectHarness over the universe files and cluster objects the
// caller names.
func harnessOf(t *testing.T, files map[string]string, objs ...runtime.Object) *projectHarness {
	t.Helper()
	inventory.invalidate()
	t.Cleanup(inventory.invalidate)
	latestCache.Lock()
	latestCache.tags = map[string]latestEntry{}
	latestCache.Unlock()

	remote, bare := universeRemoteWith(t, files)
	restore := swapUniverseRemote(remote)
	t.Cleanup(restore)

	reg := fakeTagRegistry(t, map[string][]string{
		"hanzoai/insights-web":    {"v1.9.0", "v2.0.0", "v2.1.0", "latest"},
		"hanzoai/insights-worker": {"v2.0.1"},
	})
	t.Cleanup(swapRegistryBase(reg.URL))

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		k8s.CDApplications: "ApplicationList",
		k8s.Pods:           "PodList",
		k8s.Namespaces:     "NamespaceList",
	}, objs...)

	store, err := openStore(t.TempDir(), testMaster, testKMS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &cloud.Service[state]{
		KMS: newFakeKMS(), Log: luxlog.New("test"), Brand: "hanzo",
		State: state{store: store, sitesHost: "hanzo.app",
			k8s: &k8sClient{initErr: "no cluster (test)", imagePrefix: defaultBuildImagePrefix, limits: testLimits()}},
	}
	fs := &cloud.Service[fleetState]{Log: luxlog.New("test"), Brand: "hanzo", State: fleetState{dyn: dyn, scan: &nsCache{}}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	appsRoutes(app, s, fs)
	return &projectHarness{app: app, dyn: dyn, bare: bare, remote: remote}
}

// doSuper fires a request as the platform SuperAdmin z, home org hanzo.
func doSuper(t *testing.T, app *zip.App, method, path string, body any) (int, []byte) {
	t.Helper()
	req := newJSONRequest(t, method, path, body)
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "z")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Test(req, budget)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decodeJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func instance(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name, "app.kubernetes.io/instance": name}
}

func podObj(ns, name string, labels map[string]string, image string, ready, sidecar bool) *unstructured.Unstructured {
	l := map[string]any{}
	for k, v := range labels {
		l[k] = v
	}
	repo := repoFromImageRef(image)
	containers := []any{map[string]any{"name": "app", "image": image}}
	statuses := []any{map[string]any{"name": "app", "ready": ready, "imageID": repo + "@sha256:" + filepath.Base(strings.TrimPrefix(repo, "ghcr.io/hanzoai/"))}}
	if sidecar {
		containers = append([]any{map[string]any{"name": "otel", "image": "otel/collector:1"}}, containers...)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": l},
		"spec":     map[string]any{"containers": containers},
		"status":   map[string]any{"phase": "Running", "containerStatuses": statuses},
	}}
}

func cdApplication(name, valueFile, sync, revision string, resources []any) *unstructured.Unstructured {
	status := map[string]any{
		"sync":    map[string]any{"status": sync, "revision": revision},
		"health":  map[string]any{"status": "Healthy"},
		"history": []any{map[string]any{"id": int64(0), "revision": "aaa111"}},
	}
	if resources != nil {
		status["resources"] = resources
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.hanzo.ai/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": name, "namespace": cdNamespace},
		"spec": map[string]any{
			"project":     "hanzo-platform",
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "hanzo"},
			"source": map[string]any{"repoURL": "https://github.com/hanzo-inc/universe", "targetRevision": "main", "path": "charts/app",
				"helm": map[string]any{"valueFiles": []any{valueFile}}},
			"syncPolicy": map[string]any{"syncOptions": []any{"ServerSideApply=true"}},
		},
		"status": status,
	}}
}

// fakeTagRegistry answers the anonymous token flow and a tag listing per repo.
func fakeTagRegistry(t *testing.T, tags map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"anon"}`))
			return
		}
		repo, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")
		if list, found := tags[repo]; ok && found {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": list})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// universeRemoteWith serves a bare repository seeded with files over smart HTTP,
// the transport the real write speaks.
func universeRemoteWith(t *testing.T, files map[string]string) (url, bare string) {
	t.Helper()
	gitBin := kit.Tool(t, "git")[0]
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	for rel, body := range files {
		p := filepath.Join(seed, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, "", "init", "-q", "-b", universeBranch, seed)
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "commit", "-qm", "seed")
	bare = filepath.Join(root, "universe.git")
	mustGit(t, "", "clone", "-q", "--bare", seed, bare)
	servable(t, bare)
	srv := httptest.NewServer(&cgi.Handler{
		Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/universe.git", bare
}

func writeOverlay(t *testing.T, root, org, name, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(overlayPath(org, name)))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Identical project writes racing on one main tip are one write: every caller is
// answered with the same review branch and commit, and none with a 502.
func TestIdenticalProjectWritesRacingAllLand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)
		const n = 6
		codes := make([]int, n)
		writes := make([]projectWrite, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, body := doSuper(t, h.app, http.MethodPost, "/v1/platform/projects",
					map[string]any{"name": "ops", "apps": []map[string]string{{"org": "hanzo", "name": "loose"}}})
				codes[i] = code
				if code == http.StatusCreated {
					decodeJSON(t, body, &writes[i])
				} else {
					t.Errorf("write %d: %d %s", i, code, body)
				}
			}()
		}
		wg.Wait()
		for i := 1; i < n; i++ {
			if codes[i] != http.StatusCreated || writes[i].Ref != writes[0].Ref || writes[i].Commit != writes[0].Commit {
				t.Fatalf("write %d landed %s@%s, write 0 %s@%s", i, writes[i].Ref, writes[i].Commit, writes[0].Ref, writes[0].Commit)
			}
		}
		if got := strings.TrimSpace(mustGit(t, h.bare, "rev-parse", "refs/heads/"+writes[0].Ref)); got != writes[0].Commit {
			t.Fatalf("the branch holds %s, the writes answered %s", got, writes[0].Commit)
		}
	})
}

// A project write reached over MCP runs the same admit as its route: an
// anonymous caller is refused before anything reaches universe.
func TestProjectWritesRefuseAnAnonymousToolCall(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)
		for name, want := range map[string]string{
			"post_platform_projects":              "admin required",
			"put_platform_projects_by_project":    "admin required",
			"delete_platform_projects_by_project": "admin required",
			"put_platform_apps_by_app_project":    "admin required",
			"post_platform_apps_by_app_sync":      "SuperAdmin required",
		} {
			msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": name, "arguments": map[string]any{
					"name": "ops", "project": "cloud", "into": "insights", "app": "cloud", "org": "hanzo", "partOf": "ops",
					"apps": []any{map[string]any{"org": "hanzo", "name": "loose"}}}}})
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(msg)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := h.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			var rpc struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			decodeJSON(t, b, &rpc)
			if !rpc.Result.IsError || len(rpc.Result.Content) == 0 || rpc.Result.Content[0].Text != want {
				t.Fatalf("%s answered an anonymous caller %s, want the refusal %q", name, b, want)
			}
		}
		if refs := strings.TrimSpace(mustGit(t, h.bare, "for-each-ref", "refs/heads/"+declareBranchPrefix)); refs != "" {
			t.Fatalf("an anonymous tool call pushed to universe:\n%s", refs)
		}
	})
}
