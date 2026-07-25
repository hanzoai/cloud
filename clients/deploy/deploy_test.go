package deploy

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/clients/k8s"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// ── fixtures ────────────────────────────────────────────────────────────────

func fakeService(objs ...runtime.Object) *cloud.Service[state] {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.Apps:         "AppList",
		k8s.Deployments:  "DeploymentList",
		replicaSetsGVR:   "ReplicaSetList",
		podsGVR:          "PodList",
		coreSvcGVR:       "ServiceList",
		ingressGVR:       "IngressList",
		hpaGVR:           "HorizontalPodAutoscalerList",
		pdbGVR:           "PodDisruptionBudgetList",
		configMapsGVR:    "ConfigMapList",
		appProjectGVR:    "AppProjectList",
		middlewaresGVR:   "MiddlewareList",
		ingressRoutesGVR: "IngressRouteList",
	}, objs...)
	return &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{dyn: dyn}}
}

func appCR(kind, ns, name, uid, repo, tag, phase string, replicas, ready int) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": uid},
		"spec":     map[string]any{"image": map[string]any{"repository": repo, "tag": tag}, "role": "web"},
		"status":   map[string]any{"phase": phase, "replicas": int64(replicas), "readyReplicas": int64(ready), "endpoints": []any{"https://" + name + ".hanzo.ai"}},
	}}
}

func deployment(ns, name, uid, ownerUID, image string, desired, ready int) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": uid, "generation": int64(1),
			"ownerReferences": []any{map[string]any{"kind": "Service", "uid": ownerUID}}},
		"spec": map[string]any{"replicas": int64(desired),
			"selector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/instance": name}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": image}}}}},
		"status": map[string]any{"observedGeneration": int64(1), "updatedReplicas": int64(ready), "availableReplicas": int64(ready), "readyReplicas": int64(ready)},
	}
	return &unstructured.Unstructured{Object: obj}
}

func coreService(ns, name, ownerUID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "svc-" + name,
			"ownerReferences": []any{map[string]any{"kind": "Service", "uid": ownerUID}}},
		"spec": map[string]any{"type": "ClusterIP"},
	}}
}

func pod(ns, name, image string, labels map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "pod-" + name, "labels": labels},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": image}}},
		"status":   map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app", "ready": true}}},
	}}
}

// staticSite builds the two objects that ARE a static-plane site: a staticFiles
// Middleware (its S3 origin) named `<slug>-static`, and an IngressRoute whose `/`
// route references it and carries the site host.
func staticSite(ns, slug, host string) []runtime.Object {
	mw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1alpha1", "kind": "Middleware",
		"metadata": map[string]any{"name": slug + "-static", "namespace": ns},
		"spec":     map[string]any{"staticFiles": map[string]any{"root": "s3://cdn/" + slug, "spaMode": true}},
	}}
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1alpha1", "kind": "IngressRoute",
		"metadata": map[string]any{"name": slug + "-route", "namespace": ns},
		"spec": map[string]any{"routes": []any{map[string]any{
			"match":       "Host(`" + host + "`) && PathPrefix(`/`)",
			"middlewares": []any{map[string]any{"name": slug + "-static"}},
		}}},
	}}
	return []runtime.Object{mw, route}
}

// appProjectCR builds a real argoproj.io/v1alpha1 AppProject CR (the "prefer real
// projects" path). sourceRepos are surfaced; anything else on the CR is not.
func appProjectCR(name string, sourceRepos ...string) *unstructured.Unstructured {
	repos := make([]any, 0, len(sourceRepos))
	for _, r := range sourceRepos {
		repos = append(repos, r)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject",
		"metadata": map[string]any{"name": name, "namespace": "argocd"},
		"spec": map[string]any{
			"sourceRepos":  repos,
			"destinations": []any{map[string]any{"server": inClusterServer, "namespace": "hanzo"}},
			// A field the projection must NOT surface (roles carry token metadata).
			"roles": []any{map[string]any{"name": "secret-role", "policies": []any{"p, proj:x:secret-role, *, *, *, allow"}}},
		},
	}}
}

// ── pure health ─────────────────────────────────────────────────────────────

func TestResourceHealth(t *testing.T) {
	cases := []struct {
		name string
		obj  *unstructured.Unstructured
		want string
	}{
		{"app cr healthy", appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1.0.0", "Running", 2, 2), HealthHealthy},
		{"app cr rolling", appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1.0.0", "Progressing", 2, 1), HealthProgressing},
		{"app cr degraded", appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1.0.0", "Running", 2, 0), HealthDegraded},
		{"service cr (transition) healthy", appCR("Service", "hanzo", "iam", "u1", "r", "v1.0.0", "Running", 1, 1), HealthHealthy},
		{"app cr suspended (0 desired)", appCR("App", "hanzo", "iam", "u1", "r", "v1.0.0", "", 0, 0), HealthSuspended},
		{"deployment healthy", deployment("hanzo", "iam", "d1", "u1", "ghcr.io/hanzoai/iam:v1", 2, 2), HealthHealthy},
		{"deployment progressing", deployment("hanzo", "iam", "d1", "u1", "ghcr.io/hanzoai/iam:v1", 2, 1), HealthProgressing},
		{"pod running ready", pod("hanzo", "iam-x", "ghcr.io/hanzoai/iam:v1", nil), HealthHealthy},
		{"core service healthy", coreService("hanzo", "iam", "u1"), HealthHealthy},
		{"nil missing", nil, HealthMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := resourceHealth(c.obj)
			if got != c.want {
				t.Errorf("resourceHealth = %q, want %q", got, c.want)
			}
		})
	}

	// Degraded pod: a container in CrashLoopBackOff.
	crash := pod("hanzo", "iam-x", "ghcr.io/hanzoai/iam:v1", nil)
	_ = unstructured.SetNestedSlice(crash.Object, []any{map[string]any{"name": "app", "ready": false, "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}, "status", "containerStatuses")
	if got, _ := resourceHealth(crash); got != HealthDegraded {
		t.Errorf("crashloop pod health = %q, want degraded", got)
	}
}

// ── pure sync ───────────────────────────────────────────────────────────────

func TestSyncStatus(t *testing.T) {
	cases := []struct{ declared, running, want string }{
		{"v1.2.0", "v1.2.0", SyncSynced},
		{"v1.3.0", "v1.2.0", SyncOutOfSync},
		{"v1.2.0", "", SyncUnknown},
		{"", "v1.2.0", SyncUnknown},
	}
	for _, c := range cases {
		if got := syncStatus(c.declared, c.running); got != c.want {
			t.Errorf("syncStatus(%q,%q) = %q, want %q", c.declared, c.running, got, c.want)
		}
	}
}

// ── ref parsing ─────────────────────────────────────────────────────────────

func TestParseRef(t *testing.T) {
	// The App CR resolves; the core/v1 Service (a child object) resolves distinctly.
	if _, gvr, err := parseRef("hanzo.ai:App:hanzo:iam"); err != nil || gvr != k8s.Apps {
		t.Errorf("App ref → (%v, %v), want k8s.Apps", gvr, err)
	}
	if _, gvr, err := parseRef("apps:Deployment:hanzo:iam"); err != nil || gvr != k8s.Deployments {
		t.Errorf("Deployment ref → (%v, %v), want k8s.Deployments", gvr, err)
	}
	if _, _, err := parseRef(":Service:hanzo:iam"); err != nil {
		t.Errorf("core Service ref err = %v, want nil", err)
	}
	// hanzo.ai:Service is not a kind this plane reads — the operator CR is App.
	bad := []string{"", "a:b:c", "hanzo.ai:Service:hanzo:iam", "unknown/Kind:hanzo:iam:x", "apps:Deployment:evil-ns:iam", "apps:Deployment:hanzo:Bad_Name"}
	for _, r := range bad {
		if _, _, err := parseRef(r); err == nil {
			t.Errorf("parseRef(%q) = nil err, want rejection", r)
		}
	}
}

// ── belongs-to-app membership ───────────────────────────────────────────────

func TestBelongsToApp(t *testing.T) {
	cr := appCR("App", "hanzo", "iam", "u1", "r", "v1.0.0", "Running", 1, 1)
	// owned by uid
	if !belongsToApp(deployment("hanzo", "x", "d1", "u1", "img:v1", 1, 1), cr, "iam") {
		t.Error("ownerRef match should belong")
	}
	// name == app
	if !belongsToApp(deployment("hanzo", "iam", "d1", "other", "img:v1", 1, 1), cr, "iam") {
		t.Error("name==app should belong")
	}
	// label instance
	lbl := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "z", "labels": map[string]any{"app.kubernetes.io/instance": "iam"}}}}
	if !belongsToApp(lbl, cr, "iam") {
		t.Error("instance label should belong")
	}
	// unrelated
	other := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "z", "uid": "zz"}}}
	if belongsToApp(other, cr, "iam") {
		t.Error("unrelated object must NOT belong")
	}
}

// ── diff ────────────────────────────────────────────────────────────────────

func TestComputeDiff(t *testing.T) {
	// No annotation → source none, not modified.
	live := deployment("hanzo", "iam", "d1", "u1", "ghcr.io/hanzoai/iam:v1", 2, 2)
	if src, mod, _ := computeDiff(live); src != "none" || mod {
		t.Errorf("no-annotation diff = (%q,%v), want (none,false)", src, mod)
	}
	// Annotation identical to live (minus status/volatile) → not modified.
	desired := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "iam", "namespace": "hanzo"},
		"spec":     map[string]any{"replicas": int64(2), "selector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/instance": "iam"}}, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/hanzoai/iam:v1"}}}}}}
	db, _ := json.Marshal(desired)
	_ = unstructured.SetNestedField(live.Object, map[string]any{lastAppliedAnnotation: string(db)}, "metadata", "annotations")
	if src, mod, _ := computeDiff(live); src != deployDesiredTODO || mod {
		t.Errorf("identical-desired diff = (%q,%v), want (%q,false)", src, mod, deployDesiredTODO)
	}
	// Annotation with a different image → modified.
	desired["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "ghcr.io/hanzoai/iam:v2"
	db2, _ := json.Marshal(desired)
	_ = unstructured.SetNestedField(live.Object, map[string]any{lastAppliedAnnotation: string(db2)}, "metadata", "annotations")
	if _, mod, _ := computeDiff(live); !mod {
		t.Error("changed-image diff = not modified, want modified")
	}
}

// ── observe mapping ─────────────────────────────────────────────────────────

func TestObserveApplication(t *testing.T) {
	cr := appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1.28.16", "Running", 2, 2)
	a := observeApplication(cr, "hanzo", "v1.28.15")
	if a.Name != "iam" || a.Env != "main" || a.Role != "web" || a.Version != "v1.28.16" || a.RunningVersion != "v1.28.15" {
		t.Errorf("observeApplication basic fields wrong: %+v", a)
	}
	if a.Health != HealthHealthy || a.Sync != SyncOutOfSync {
		t.Errorf("health/sync = %q/%q, want healthy/out-of-sync", a.Health, a.Sync)
	}
	if len(a.Endpoints) != 1 || a.Endpoints[0] != "https://iam.hanzo.ai" {
		t.Errorf("endpoints = %v", a.Endpoints)
	}
}

// ── CR resolution ────────────────────────────────────────────────────────────

func TestGetAppCR(t *testing.T) {
	s := fakeService(appCR("App", "hanzo", "iam", "u-app", "ghcr.io/hanzoai/iam", "v2.0.0", "Running", 1, 1))
	obj, gvr, err := getAppCR(s, context.Background(), "hanzo", "iam")
	if err != nil {
		t.Fatalf("getAppCR: %v", err)
	}
	if gvr != k8s.Apps {
		t.Fatalf("gvr = %v, want k8s.Apps", gvr)
	}
	if tag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag"); tag != "v2.0.0" {
		t.Fatalf("resolved tag = %q, want v2.0.0", tag)
	}
	// Missing → IsNotFound.
	if _, _, err := getAppCR(s, context.Background(), "hanzo", "ghost"); err == nil {
		t.Fatal("getAppCR(ghost) = nil err, want NotFound")
	}
}

func TestListAppCRs(t *testing.T) {
	s := fakeService(
		appCR("App", "hanzo", "iam", "u1", "r", "v2.0.0", "Running", 1, 1),
		appCR("App", "hanzo", "cloud", "u3", "r", "v1.799.0", "Running", 1, 1),
	)
	crs, err := listAppCRs(s, context.Background(), "hanzo")
	if err != nil {
		t.Fatalf("listAppCRs: %v", err)
	}
	if len(crs) != 2 {
		t.Fatalf("listAppCRs len = %d, want 2 (iam, cloud)", len(crs))
	}
	byName := map[string]string{}
	for i := range crs {
		tag, _, _ := unstructured.NestedString(crs[i].Object, "spec", "image", "tag")
		byName[crs[i].GetName()] = tag
	}
	if byName["iam"] != "v2.0.0" {
		t.Errorf("iam tag = %q, want v2.0.0", byName["iam"])
	}
	if byName["cloud"] != "v1.799.0" {
		t.Errorf("cloud tag = %q", byName["cloud"])
	}
}

// A staticFiles Middleware + its IngressRoute project as ONE role:"site"
// Application row: identity is the S3 slug, repository is the S3 origin, endpoint
// is the joined host, and it is always Synced (no image drift for a static site).
func TestListSiteApplications(t *testing.T) {
	objs := []runtime.Object{}
	objs = append(objs, staticSite("hanzo", "gallery", "gallery.hanzo.ai")...)
	// A Middleware with a root but NO IngressRoute → routed:false ⇒ Missing.
	objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1alpha1", "kind": "Middleware",
		"metadata": map[string]any{"name": "orphan-static", "namespace": "hanzo"},
		"spec":     map[string]any{"staticFiles": map[string]any{"root": "s3://cdn/orphan"}},
	}})
	s := fakeService(objs...)

	sites := listSiteApplications(s, context.Background(), "hanzo")
	if len(sites) != 2 {
		t.Fatalf("listSiteApplications len = %d, want 2 (gallery, orphan)", len(sites))
	}
	by := map[string]Application{}
	for _, a := range sites {
		by[a.Name] = a
	}
	g, ok := by["gallery"]
	if !ok {
		t.Fatalf("no gallery site row (got %v)", by)
	}
	if g.Role != "site" {
		t.Errorf("gallery Role = %q, want site", g.Role)
	}
	if g.Repository != "s3://cdn/gallery" {
		t.Errorf("gallery Repository = %q, want s3://cdn/gallery", g.Repository)
	}
	if g.Sync != SyncSynced {
		t.Errorf("gallery Sync = %q, want synced", g.Sync)
	}
	if g.Health != HealthHealthy {
		t.Errorf("gallery Health = %q, want healthy", g.Health)
	}
	if len(g.Endpoints) != 1 || g.Endpoints[0] != "https://gallery.hanzo.ai" {
		t.Errorf("gallery Endpoints = %v, want [https://gallery.hanzo.ai]", g.Endpoints)
	}
	if o := by["orphan"]; o.Health != HealthMissing || len(o.Endpoints) != 0 {
		t.Errorf("orphan site = %+v, want Health=missing, no endpoints", o)
	}
}

// ── tree ────────────────────────────────────────────────────────────────────

func TestBuildTreeOwnership(t *testing.T) {
	cr := appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1.0.0", "Running", 1, 1)
	dep := deployment("hanzo", "iam", "d1", "u1", "ghcr.io/hanzoai/iam:v1.0.0", 1, 1) // owned by CR uid u1
	svc := coreService("hanzo", "iam", "u1")                                          // owned by CR uid u1
	p := pod("hanzo", "iam-abc", "ghcr.io/hanzoai/iam:v1.0.0", map[string]any{"app.kubernetes.io/instance": "iam"})
	s := fakeService(cr, dep, svc, p)

	nodes := buildTree(s, context.Background(), "hanzo", "iam", cr)
	kinds := map[string]bool{}
	var podNode *Node
	for i := range nodes {
		kinds[nodes[i].Kind] = true
		if nodes[i].Kind == "Pod" {
			podNode = &nodes[i]
		}
	}
	for _, want := range []string{"App", "Deployment", "Service", "Pod"} {
		if !kinds[want] {
			t.Errorf("tree missing a %s node (nodes=%d)", want, len(nodes))
		}
	}
	// Root is the App CR, first node.
	if nodes[0].Kind != "App" || nodes[0].Name != "iam" {
		t.Errorf("root node = %s/%s, want App/iam", nodes[0].Kind, nodes[0].Name)
	}
	// The Pod attached to the Deployment via selector fallback.
	if podNode == nil || len(podNode.ParentRefs) != 1 || podNode.ParentRefs[0].Kind != "Deployment" {
		t.Errorf("pod parent = %+v, want a Deployment parentRef", podNode)
	}
}

// ── image ref helpers ───────────────────────────────────────────────────────

func TestImageRefHelpers(t *testing.T) {
	cases := []struct{ ref, repo, tag string }{
		{"ghcr.io/hanzoai/iam:v1.28.16", "ghcr.io/hanzoai/iam", "v1.28.16"},
		{"registry:5000/hanzoai/iam:v1", "registry:5000/hanzoai/iam", "v1"},
		{"ghcr.io/hanzoai/iam@sha256:abc", "ghcr.io/hanzoai/iam", "sha256:abc"},
		{"ghcr.io/hanzoai/iam", "ghcr.io/hanzoai/iam", ""},
	}
	for _, c := range cases {
		if got := repoFromImageRef(c.ref); got != c.repo {
			t.Errorf("repoFromImageRef(%q) = %q, want %q", c.ref, got, c.repo)
		}
		if got := tagFromImageRef(c.ref); got != c.tag {
			t.Errorf("tagFromImageRef(%q) = %q, want %q", c.ref, got, c.tag)
		}
	}
}
