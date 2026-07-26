package deploy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// cdApp is a Hanzo CD Application CR as the controller writes it — the shape the
// endpoint reads verbatim.
func cdApp(name, repo, revision, sync, health string, history []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps.hanzo.ai/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": name, "namespace": "hanzo-cd"},
		"spec": map[string]any{
			"project":    "hanzo",
			"source":     map[string]any{"repoURL": repo, "path": "infra/k8s/operator/crs", "targetRevision": "main"},
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": false, "selfHeal": true}},
		},
		"status": map[string]any{
			"sync":           map[string]any{"status": sync, "revision": revision},
			"health":         map[string]any{"status": health},
			"reconciledAt":   "2026-07-25T21:44:10Z",
			"resources":      []any{map[string]any{"kind": "App", "name": "cloud"}},
			"history":        history,
			"operationState": map[string]any{"phase": "Succeeded", "message": "successfully synced (all tasks run)", "startedAt": "2026-07-25T21:44:08Z", "finishedAt": "2026-07-25T21:44:10Z", "syncResult": map[string]any{"revision": revision}},
		},
	}}
}

func cdHistory(id int64, revision, deployedAt string) any {
	return map[string]any{
		"id":              id,
		"revision":        revision,
		"deployStartedAt": deployedAt,
		"deployedAt":      deployedAt,
		"initiatedBy":     map[string]any{"automated": true},
	}
}

// TestGitOpsPlane: the endpoint reports what CD said — the applied revision, its
// sync/health verdict, its automation policy — and returns history NEWEST FIRST
// (CD appends oldest-first, so the reversal is the whole point of the projection).
func TestGitOpsPlane(t *testing.T) {
	s := fakeService(cdApp("universe-crs", "https://github.com/hanzoai/universe", "88f35c42", "Synced", "Healthy", []any{
		cdHistory(130, "296ac190", "2026-07-25T21:24:08Z"),
		cdHistory(131, "5b8a8c95", "2026-07-25T21:31:09Z"),
		cdHistory(132, "88f35c42", "2026-07-25T21:44:10Z"),
	}))
	body := getJSON(t, s, "/v1/deploy/gitops")

	if body["installed"] != true {
		t.Fatalf("installed = %v, want true", body["installed"])
	}
	apps, ok := body["applications"].([]any)
	if !ok || len(apps) != 1 {
		t.Fatalf("applications = %v, want exactly one", body["applications"])
	}
	a := apps[0].(map[string]any)
	if a["name"] != "universe-crs" || a["namespace"] != "hanzo-cd" {
		t.Fatalf("identity = %v/%v, want hanzo-cd/universe-crs", a["namespace"], a["name"])
	}
	if a["revision"] != "88f35c42" || a["sync"] != "Synced" || a["health"] != "Healthy" {
		t.Fatalf("verdict = %v %v %v, want 88f35c42 Synced Healthy", a["revision"], a["sync"], a["health"])
	}
	if a["automated"] != true || a["selfHeal"] != true {
		t.Fatalf("policy = automated %v selfHeal %v, want both true", a["automated"], a["selfHeal"])
	}
	if a["resources"] != float64(1) {
		t.Fatalf("resources = %v, want 1", a["resources"])
	}
	op, _ := a["operation"].(map[string]any)
	if op["phase"] != "Succeeded" {
		t.Fatalf("operation = %v, want phase Succeeded", a["operation"])
	}
	hist, ok := a["history"].([]any)
	if !ok || len(hist) != 3 {
		t.Fatalf("history = %v, want 3 entries", a["history"])
	}
	if h0 := hist[0].(map[string]any); h0["revision"] != "88f35c42" || h0["id"] != float64(132) {
		t.Fatalf("history[0] = %v, want the NEWEST deploy (id 132)", hist[0])
	}
	if h2 := hist[2].(map[string]any); h2["id"] != float64(130) {
		t.Fatalf("history[2] = %v, want the oldest deploy (id 130)", hist[2])
	}
}

// TestGitOpsHistoryCap bounds the response even if CD's history grows.
func TestGitOpsHistoryCap(t *testing.T) {
	var h []any
	for i := 0; i < gitOpsHistoryMax+5; i++ {
		h = append(h, cdHistory(int64(i), "rev", "2026-07-25T21:44:10Z"))
	}
	s := fakeService(cdApp("universe-crs", "https://github.com/hanzoai/universe", "rev", "Synced", "Healthy", h))
	body := getJSON(t, s, "/v1/deploy/gitops")
	a := body["applications"].([]any)[0].(map[string]any)
	if got := len(a["history"].([]any)); got != gitOpsHistoryMax {
		t.Fatalf("history length = %d, want the cap %d", got, gitOpsHistoryMax)
	}
}

// TestGitOpsGuarded: the CD plane is fleet infrastructure — a non-SuperAdmin API
// caller is refused, never given a partial view.
func TestGitOpsGuarded(t *testing.T) {
	s := fakeService()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	resp, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/deploy/gitops", nil))
	if err != nil {
		t.Fatalf("GET /v1/deploy/gitops: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous GET /v1/deploy/gitops = %d, want 403", resp.StatusCode)
	}
}

// TestGitOpsEmptyFleet: no Applications is an empty list on an INSTALLED plane —
// distinct from "no CD here", which the SPA renders differently.
func TestGitOpsEmptyFleet(t *testing.T) {
	body := getJSON(t, fakeService(), "/v1/deploy/gitops")
	if body["installed"] != true {
		t.Fatalf("installed = %v, want true (CRD served, zero apps)", body["installed"])
	}
	if apps, _ := body["applications"].([]any); len(apps) != 0 {
		t.Fatalf("applications = %v, want empty", body["applications"])
	}
	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
