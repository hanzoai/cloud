package deploy

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func projFixture(name, ns, repo, tag, phase string, replicas, ready int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1", "kind": "App",
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "uid-" + name},
		"spec":     map[string]any{"role": "service", "image": map[string]any{"repository": repo, "tag": tag}},
		"status":   map[string]any{"phase": phase, "replicas": replicas, "readyReplicas": ready, "availableReplicas": ready},
	}}
}

// TestProjectApp_ShapeIsUIRenderable asserts the projection produces the exact
// minimal ArgoCD Application JSON the React UI needs (per the api-server
// contract): the fields the list tiles + detail header destructure must all be
// present and correctly typed, or the SPA throws.
func TestProjectApp_ShapeIsUIRenderable(t *testing.T) {
	app := projectApp(projFixture("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1.2.3", "Running", 1, 1), "hanzo", "v1.2.3")

	if app.APIVersion != "argoproj.io/v1alpha1" || app.Kind != "Application" {
		t.Fatalf("wrong TypeMeta: %s/%s", app.APIVersion, app.Kind)
	}
	if app.Metadata.Name != "cloud" || app.Metadata.Namespace != "hanzo" {
		t.Fatalf("wrong metadata: %+v", app.Metadata)
	}
	if app.Spec.Source.RepoURL == "" || app.Spec.Destination.Server == "" {
		t.Fatal("spec.source.repoURL and spec.destination.server are required by the UI")
	}
	// Running + declared==running ⇒ Healthy + Synced (Capitalized ArgoCD vocab).
	if app.Status.Health.Status != "Healthy" {
		t.Fatalf("health = %q, want Healthy", app.Status.Health.Status)
	}
	if app.Status.Sync.Status != "Synced" {
		t.Fatalf("sync = %q, want Synced", app.Status.Sync.Status)
	}

	// The UI's parseAppFields requires status.resources to be an array and
	// status.summary to be an object — assert they marshal as such.
	b, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	status := m["status"].(map[string]any)
	if _, ok := status["resources"].([]any); !ok {
		t.Fatalf("status.resources must be a JSON array, got %T", status["resources"])
	}
	if _, ok := status["summary"].(map[string]any); !ok {
		t.Fatalf("status.summary must be a JSON object, got %T", status["summary"])
	}
	if m["spec"].(map[string]any)["source"].(map[string]any)["repoURL"] == "" {
		t.Fatal("spec.source.repoURL empty")
	}
}

// TestProjectApp_HealthAndSyncVocab covers the native→ArgoCD vocab mapping.
func TestProjectApp_HealthAndSyncVocab(t *testing.T) {
	// 1 desired / 0 ready ⇒ Degraded; declared != running ⇒ OutOfSync.
	app := projectApp(projFixture("chat", "hanzo", "ghcr.io/hanzoai/chat", "v2", "Creating", 1, 0), "hanzo", "v1")
	if app.Status.Health.Status != "Degraded" {
		t.Fatalf("health = %q, want Degraded", app.Status.Health.Status)
	}
	if app.Status.Sync.Status != "OutOfSync" {
		t.Fatalf("sync = %q, want OutOfSync", app.Status.Sync.Status)
	}
}

// TestProjectTree_ShapeIsUIRenderable asserts the ApplicationTree projection.
func TestProjectTree_ShapeIsUIRenderable(t *testing.T) {
	nodes := []Node{
		{ResourceRef: ResourceRef{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "hanzo", Name: "cloud"}, UID: "d1", Health: HealthHealthy, Version: "v1.2.3"},
		{ResourceRef: ResourceRef{Version: "v1", Kind: "Pod", Namespace: "hanzo", Name: "cloud-xyz"}, UID: "p1", Health: HealthHealthy, ParentRefs: []ResourceRef{{Group: "apps", Version: "v1", Kind: "ReplicaSet", Namespace: "hanzo", Name: "cloud-abc"}}},
	}
	tree := projectTree(nodes)
	if len(tree.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(tree.Nodes))
	}
	b, _ := json.Marshal(tree)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["nodes"].([]any); !ok {
		t.Fatal("tree.nodes must be a JSON array")
	}
	if _, ok := m["orphanedNodes"]; !ok {
		t.Fatal("tree.orphanedNodes must be present")
	}
	// Node health mapped to Capitalized vocab; image surfaced as an info item.
	n0 := m["nodes"].([]any)[0].(map[string]any)
	if n0["health"].(map[string]any)["status"] != "Healthy" {
		t.Fatalf("node health = %v", n0["health"])
	}
	if n0["kind"] != "Deployment" || n0["name"] != "cloud" {
		t.Fatalf("node ref wrong: %v", n0)
	}
}

// ── clusters projection ───────────────────────────────────────────────────────

// TestProjectClusters_DedupesAndAlwaysInCluster: the fleet's destinations collapse
// to a deduped ClusterList; the in-cluster destination is always present with the
// right per-cluster application count; a CR that declares a distinct destination
// yields a second cluster.
func TestProjectClusters_DedupesAndAlwaysInCluster(t *testing.T) {
	// Empty fleet still projects exactly one cluster (in-cluster), count 0.
	empty := projectClusters(nil)
	if len(empty.Items) != 1 || empty.Items[0].Server != inClusterServer || empty.Items[0].Name != inClusterName {
		t.Fatalf("empty fleet clusters = %+v, want one in-cluster", empty.Items)
	}
	if empty.Items[0].Info.ApplicationsCount != 0 {
		t.Fatalf("empty in-cluster count = %d, want 0", empty.Items[0].Info.ApplicationsCount)
	}
	if empty.Items[0].ConnectionState.Status != "Successful" || empty.Items[0].Info.ConnectionState.Status != "Successful" {
		t.Fatalf("connectionState = %+v, want Successful", empty.Items[0])
	}

	// Two in-cluster apps + one app declaring a distinct destination server.
	a := *projFixture("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1)
	b := *projFixture("iam", "hanzo", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1)
	edge := *projFixture("edge", "hanzo", "ghcr.io/hanzoai/edge", "v1", "Running", 1, 1)
	_ = unstructured.SetNestedField(edge.Object, map[string]any{"server": "https://edge.example:6443", "name": "edge"}, "spec", "destination")

	cl := projectClusters([]unstructured.Unstructured{a, b, edge})
	if len(cl.Items) != 2 {
		t.Fatalf("clusters = %d, want 2 (in-cluster + edge)", len(cl.Items))
	}
	byServer := map[string]argoCluster{}
	for _, c := range cl.Items {
		byServer[c.Server] = c
	}
	if byServer[inClusterServer].Info.ApplicationsCount != 2 {
		t.Fatalf("in-cluster count = %d, want 2", byServer[inClusterServer].Info.ApplicationsCount)
	}
	if e, ok := byServer["https://edge.example:6443"]; !ok || e.Name != "edge" || e.Info.ApplicationsCount != 1 {
		t.Fatalf("edge cluster = %+v (ok=%v), want name=edge count=1", e, ok)
	}
}

// TestProjectClusters_NeverEmitsCredentials is the credential-leak guard: the
// marshaled ClusterList must carry NO config / bearerToken / tlsClientConfig /
// execProviderConfig key anywhere — the argoCluster type has no field for them.
func TestProjectClusters_NeverEmitsCredentials(t *testing.T) {
	// Even a CR polluted with a spec.destination.config must not surface it.
	a := *projFixture("cloud", "hanzo", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1)
	_ = unstructured.SetNestedField(a.Object, map[string]any{
		"server": inClusterServer,
		"config": map[string]any{"bearerToken": "SECRET-DO-NOT-LEAK", "tlsClientConfig": map[string]any{"keyData": "PRIV"}},
	}, "spec", "destination")

	b, err := json.Marshal(projectClusters([]unstructured.Unstructured{a}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blob := string(b)
	for _, forbidden := range []string{"config", "bearerToken", "tlsClientConfig", "execProviderConfig", "keyData", "SECRET-DO-NOT-LEAK", "PRIV"} {
		if strings.Contains(blob, forbidden) {
			t.Fatalf("clusters JSON leaked %q: %s", forbidden, blob)
		}
	}
}

// ── projects projection ───────────────────────────────────────────────────────

// TestProjectedProjectNames_DistinctWithDefault: default is always first; distinct
// spec.project values follow; empty projects collapse to default.
func TestProjectedProjectNames_DistinctWithDefault(t *testing.T) {
	// No projects declared → just default.
	if got := projectedProjectNames([]unstructured.Unstructured{
		*projFixture("cloud", "hanzo", "r", "v1", "Running", 1, 1),
	}); len(got) != 1 || got[0] != "default" {
		t.Fatalf("no-project names = %v, want [default]", got)
	}

	a := *projFixture("cloud", "hanzo", "r", "v1", "Running", 1, 1)
	b := *projFixture("iam", "hanzo", "r", "v1", "Running", 1, 1)
	c := *projFixture("chat", "hanzo", "r", "v1", "Running", 1, 1)
	_ = unstructured.SetNestedField(a.Object, "team-a", "spec", "project")
	_ = unstructured.SetNestedField(b.Object, "team-a", "spec", "project") // dup
	_ = unstructured.SetNestedField(c.Object, "team-b", "spec", "project")
	got := projectedProjectNames([]unstructured.Unstructured{a, b, c})
	if len(got) != 3 || got[0] != "default" {
		t.Fatalf("names = %v, want [default team-a team-b]", got)
	}
	set := map[string]bool{}
	for _, n := range got {
		if set[n] {
			t.Fatalf("duplicate project name %q in %v", n, got)
		}
		set[n] = true
	}
	if !set["team-a"] || !set["team-b"] {
		t.Fatalf("names = %v, want team-a + team-b present", got)
	}
}

// TestSynthProject_Permissive: a synthesized project is a permissive AppProject the
// UI can render + filter on.
func TestSynthProject_Permissive(t *testing.T) {
	p := synthProject("default")
	if p.APIVersion != "argoproj.io/v1alpha1" || p.Kind != "AppProject" || p.Metadata.Name != "default" {
		t.Fatalf("synth project TypeMeta/name wrong: %+v", p)
	}
	if len(p.Spec.SourceRepos) != 1 || p.Spec.SourceRepos[0] != "*" {
		t.Fatalf("sourceRepos = %v, want [*]", p.Spec.SourceRepos)
	}
	if len(p.Spec.Destinations) != 1 || p.Spec.Destinations[0].Server != "*" || p.Spec.Destinations[0].Namespace != "*" {
		t.Fatalf("destinations = %v, want [{*,*}]", p.Spec.Destinations)
	}
	if len(p.Spec.ClusterResourceWhitelist) != 1 || p.Spec.ClusterResourceWhitelist[0].Group != "*" {
		t.Fatalf("clusterResourceWhitelist = %v, want [{*,*}]", p.Spec.ClusterResourceWhitelist)
	}
	// status marshals as an object (UI expects one).
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["status"].(map[string]any); !ok {
		t.Fatalf("project status must be a JSON object, got %T", m["status"])
	}
}

// TestProjectAppProject_SurfacesOnlyIntendedFields: a REAL AppProject CR is
// reshaped to name + the whitelisted spec fields ONLY — roles (token metadata)
// never leak through.
func TestProjectAppProject_SurfacesOnlyIntendedFields(t *testing.T) {
	p := projectAppProject(appProjectCR("team-a", "https://git.hanzo.ai/team-a/*"))
	if p.Metadata.Name != "team-a" {
		t.Fatalf("name = %q, want team-a", p.Metadata.Name)
	}
	if len(p.Spec.SourceRepos) != 1 || p.Spec.SourceRepos[0] != "https://git.hanzo.ai/team-a/*" {
		t.Fatalf("sourceRepos = %v, want the real CR value", p.Spec.SourceRepos)
	}
	if len(p.Spec.Destinations) != 1 || p.Spec.Destinations[0].Namespace != "hanzo" {
		t.Fatalf("destinations = %v, want the real CR value", p.Spec.Destinations)
	}
	b, _ := json.Marshal(p)
	for _, forbidden := range []string{"roles", "secret-role", "policies"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("projected AppProject leaked %q: %s", forbidden, b)
		}
	}
}
