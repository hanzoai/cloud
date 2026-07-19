// projection.go — the App-CR → ArgoCD `Application` READ PROJECTION.
//
// The CTO decision (have-both): serve the full ArgoCD React UI, but feed it a
// projection of our operator `App` CRs shaped as ArgoCD `Application`s. There is
// NO stored Application/AppProject CRD — each App CR IS projected on the fly, its
// resource tree + health synthesized from the SAME readers the native
// /v1/deploy routes use (listAppCRs/getAppCR/buildTree/resourceHealth), with no
// repo-server and no redis. App CRs stay the single source of truth; the
// Application shape exists only at this API layer.
//
// These `argo*` types are the MINIMAL ArgoCD v1alpha1 JSON the React app renders
// (list + detail + tree). Distinct from the native `Application` (applications.go)
// which backs the native /v1/deploy/applications surface — this backs the
// ArgoCD-UI-compatible /v1/deploy/api/v1/* surface.
package deploy

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ── ArgoCD v1alpha1 JSON (minimal, UI-render-complete) ───────────────────────

type argoListMeta struct {
	ResourceVersion string `json:"resourceVersion"`
}

type argoMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

type argoSource struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`
}

type argoDestination struct {
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
	Name      string `json:"name,omitempty"` // ArgoCD allows a destination by cluster name; omitted for the in-cluster projection.
}

type argoSpec struct {
	Source      argoSource      `json:"source"`
	Destination argoDestination `json:"destination"`
	Project     string          `json:"project"`
}

type argoHealth struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type argoSyncStatus struct {
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
}

type argoResourceStatus struct {
	Group     string      `json:"group,omitempty"`
	Version   string      `json:"version,omitempty"`
	Kind      string      `json:"kind"`
	Namespace string      `json:"namespace,omitempty"`
	Name      string      `json:"name"`
	Status    string      `json:"status,omitempty"`
	Health    *argoHealth `json:"health,omitempty"`
}

type argoSummary struct {
	Images []string `json:"images,omitempty"`
}

type argoStatus struct {
	Sync         argoSyncStatus       `json:"sync"`
	Health       argoHealth           `json:"health"`
	Resources    []argoResourceStatus `json:"resources"`
	Summary      argoSummary          `json:"summary"`
	ReconciledAt string               `json:"reconciledAt,omitempty"`
}

type argoApp struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   argoMeta   `json:"metadata"`
	Spec       argoSpec   `json:"spec"`
	Status     argoStatus `json:"status"`
}

type argoAppList struct {
	APIVersion string       `json:"apiVersion"`
	Kind       string       `json:"kind"`
	Metadata   argoListMeta `json:"metadata"`
	Items      []argoApp    `json:"items"`
}

// ── resource tree (ArgoCD ApplicationTree) ───────────────────────────────────

type argoResourceRef struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

type argoInfoItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type argoNode struct {
	argoResourceRef
	ParentRefs      []argoResourceRef `json:"parentRefs,omitempty"`
	Info            []argoInfoItem    `json:"info,omitempty"`
	Health          *argoHealth       `json:"health,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	CreatedAt       string            `json:"createdAt,omitempty"`
	Images          []string          `json:"images,omitempty"`
}

type argoTree struct {
	Nodes         []argoNode `json:"nodes"`
	OrphanedNodes []argoNode `json:"orphanedNodes"`
	Hosts         []any      `json:"hosts"`
}

// ── projection ───────────────────────────────────────────────────────────────

// argoHealthFrom maps the native lowercase health vocab (resourceHealth) to the
// Capitalized ArgoCD vocab the UI renders.
func argoHealthFrom(native string) string {
	switch native {
	case HealthHealthy:
		return "Healthy"
	case HealthProgressing:
		return "Progressing"
	case HealthDegraded:
		return "Degraded"
	case HealthSuspended:
		return "Suspended"
	case HealthMissing:
		return "Missing"
	default:
		return "Unknown"
	}
}

// argoSyncFrom maps the native sync verdict to the Capitalized ArgoCD vocab.
func argoSyncFrom(native string) string {
	switch native {
	case SyncSynced:
		return "Synced"
	case SyncOutOfSync:
		return "OutOfSync"
	default:
		return "Unknown"
	}
}

// deployManifestRepo is the desired-state source the projection reports as the
// Application's git source — the manifest repo the engine syncs. Display-only.
const deployManifestRepo = "https://git.hanzo.ai/hanzoai/universe"

// projectApp maps ONE operator App CR (+ its running image tag) to an ArgoCD
// Application. Reuses observeApplication's native derivation, then reshapes to
// the v1alpha1 JSON — one source of truth (the App CR), two shapes.
func projectApp(cr *unstructured.Unstructured, ns, runningTag string) argoApp {
	native := observeApplication(cr, ns, runningTag)
	repository, _, _ := unstructured.NestedString(cr.Object, "spec", "image", "repository")
	tag := native.Version
	image := repository
	if tag != "" {
		image = repository + ":" + tag
	}
	return argoApp{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: argoMeta{
			Name:              native.Name,
			Namespace:         ns,
			UID:               string(cr.GetUID()),
			CreationTimestamp: cr.GetCreationTimestamp().Format("2006-01-02T15:04:05Z07:00"),
			Labels:            map[string]string{"argocd.argoproj.io/instance": native.Name, "hanzo.ai/env": native.Env},
		},
		Spec: argoSpec{
			Source:      argoSource{RepoURL: deployManifestRepo, Path: "infra/k8s/operator/crs", TargetRevision: "main"},
			Destination: argoDestination{Server: inClusterServer, Namespace: ns},
			Project:     "default",
		},
		Status: argoStatus{
			Sync:      argoSyncStatus{Status: argoSyncFrom(native.Sync), Revision: native.Version},
			Health:    argoHealth{Status: argoHealthFrom(native.Health), Message: native.HealthMessage},
			Resources: []argoResourceStatus{},
			Summary:   argoSummary{Images: nonEmpty(image)},
		},
	}
}

// projectTree reshapes the native buildTree []Node into an ArgoCD ApplicationTree.
func projectTree(nodes []Node) argoTree {
	out := argoTree{Nodes: make([]argoNode, 0, len(nodes)), OrphanedNodes: []argoNode{}, Hosts: []any{}}
	for i := range nodes {
		n := &nodes[i]
		an := argoNode{
			argoResourceRef: argoResourceRef{
				Group: n.Group, Version: n.Version, Kind: n.Kind,
				Namespace: n.Namespace, Name: n.Name, UID: n.UID,
			},
			ResourceVersion: "",
			CreatedAt:       n.CreatedAt,
			Health:          &argoHealth{Status: argoHealthFrom(n.Health), Message: n.HealthMessage},
		}
		for _, p := range n.ParentRefs {
			an.ParentRefs = append(an.ParentRefs, argoResourceRef{
				Group: p.Group, Version: p.Version, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name,
			})
		}
		if n.Version != "" {
			an.Info = append(an.Info, argoInfoItem{Name: "Image Tag", Value: n.Version})
		}
		out.Nodes = append(out.Nodes, an)
	}
	return out
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// ── ArgoCD Cluster projection (destinations → ClusterList) ───────────────────

const (
	// inClusterServer / inClusterName are the destination every operator App CR
	// reconciles into — the cluster this cloud runs in. projectApp synthesizes
	// this destination, so the cluster set derives from it: one value, one home.
	inClusterServer = "https://kubernetes.default.svc"
	inClusterName   = "in-cluster"

	// connectionSuccessful is ArgoCD's ConnectionStatus for a reachable cluster.
	// This plane projects CRs the operator already reconciles INTO the cluster, so
	// the destination is reachable by definition — there is no cluster credential
	// to probe and, by construction (argoCluster has no config field), none to leak.
	connectionSuccessful = "Successful"
)

// argoConnectionState is v1alpha1.ConnectionState — status is what the UI reads;
// message + attemptedAt are optional and omitted from the projection.
type argoConnectionState struct {
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	AttemptedAt string `json:"attemptedAt,omitempty"`
}

// argoClusterInfo is v1alpha1.ClusterInfo reduced to the connection + app count
// the UI's Destination/Clusters view reads. It carries NO credentials.
type argoClusterInfo struct {
	ConnectionState   argoConnectionState `json:"connectionState"`
	ApplicationsCount int                 `json:"applicationsCount"`
	ServerVersion     string              `json:"serverVersion,omitempty"`
}

// argoCluster is v1alpha1.Cluster REDUCED to the projection-safe fields. There is
// DELIBERATELY no `config` field: a projection has no cluster credential to
// surface, so the type physically cannot carry a bearer token, TLS key, or exec
// provider. server + name + connectionState is what the Destination column reads.
type argoCluster struct {
	Server          string              `json:"server"`
	Name            string              `json:"name"`
	ConnectionState argoConnectionState `json:"connectionState"`
	Info            argoClusterInfo     `json:"info"`
}

type argoClusterList struct {
	Metadata argoListMeta  `json:"metadata"`
	Items    []argoCluster `json:"items"`
}

// clusterOf is the (server, name) an App CR reconciles into. Operator App CRs
// declare no destination — they reconcile into THIS cluster — so absent a
// spec.destination the cluster is in-cluster. Reading spec.destination first keeps
// the derivation honest if a CR ever declares one.
func clusterOf(cr *unstructured.Unstructured) (server, name string) {
	server, _, _ = unstructured.NestedString(cr.Object, "spec", "destination", "server")
	name, _, _ = unstructured.NestedString(cr.Object, "spec", "destination", "name")
	if server == "" {
		server = inClusterServer
	}
	if name == "" {
		if server == inClusterServer {
			name = inClusterName
		} else {
			name = server
		}
	}
	return server, name
}

// connectionOK is the ConnectionState of a projected (operator-owned) cluster:
// reachable by definition.
func connectionOK() argoConnectionState { return argoConnectionState{Status: connectionSuccessful} }

// projectClusters derives the ArgoCD ClusterList from the destinations the fleet
// reconciles into — deduped by server, counting the applications per cluster. The
// in-cluster destination is ALWAYS present (an empty fleet still has one cluster).
// It cannot emit a cluster credential: argoCluster has no config field.
func projectClusters(crs []unstructured.Unstructured) argoClusterList {
	bySrv := map[string]*argoCluster{}
	order := []string{}
	ensure := func(server, name string) *argoCluster {
		cl, ok := bySrv[server]
		if !ok {
			cl = &argoCluster{Server: server, Name: name, ConnectionState: connectionOK(), Info: argoClusterInfo{ConnectionState: connectionOK()}}
			bySrv[server] = cl
			order = append(order, server)
		}
		return cl
	}
	ensure(inClusterServer, inClusterName) // an empty fleet still has one cluster
	for i := range crs {
		server, name := clusterOf(&crs[i])
		ensure(server, name).Info.ApplicationsCount++
	}
	items := make([]argoCluster, 0, len(order))
	for _, srv := range order {
		items = append(items, *bySrv[srv])
	}
	return argoClusterList{Metadata: argoListMeta{}, Items: items}
}

// ── ArgoCD AppProject projection (distinct App-CR projects → AppProjectList) ──

// appProjectGVR is the ArgoCD AppProject CRD. This plane does not run argocd, so
// the CRD is normally absent — dashProjects checks for it and falls back to
// synthesizing a project set from the distinct App-CR project names.
var appProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// argoGroupKind is metav1.GroupKind — a clusterResourceWhitelist entry.
type argoGroupKind struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
}

// argoProjectSpec is the subset of v1alpha1.AppProjectSpec the UI's project filter
// + detail read. Only these fields are surfaced (never the whole CR spec) so a
// real AppProject cannot leak roles/tokens or any field this plane didn't intend.
type argoProjectSpec struct {
	SourceRepos              []string          `json:"sourceRepos"`
	Destinations             []argoDestination `json:"destinations"`
	ClusterResourceWhitelist []argoGroupKind   `json:"clusterResourceWhitelist"`
	Description              string            `json:"description,omitempty"`
}

// argoProject is v1alpha1.AppProject (projected). Project scoping on this platform
// is IAM/Org, not argocd RBAC, so a synthesized project is permissive.
type argoProject struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   argoMeta        `json:"metadata"`
	Spec       argoProjectSpec `json:"spec"`
	Status     argoProjectStat `json:"status"`
}

// argoProjectStat marshals as the empty status object the UI expects.
type argoProjectStat struct{}

type argoProjectList struct {
	Metadata argoListMeta  `json:"metadata"`
	Items    []argoProject `json:"items"`
}

// projectedProjectNames is the distinct set of App-CR spec.project values, with
// "default" always first (operator App CRs carry no project → default). Pure.
func projectedProjectNames(crs []unstructured.Unstructured) []string {
	seen := map[string]bool{"default": true}
	order := []string{"default"}
	for i := range crs {
		p, _, _ := unstructured.NestedString(crs[i].Object, "spec", "project")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		order = append(order, p)
	}
	return order
}

// synthProject builds a permissive projected AppProject for a name (project
// scoping here is IAM/Org, not argocd RBAC).
func synthProject(name string) argoProject {
	return argoProject{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "AppProject",
		Metadata:   argoMeta{Name: name},
		Spec: argoProjectSpec{
			SourceRepos:              []string{"*"},
			Destinations:             []argoDestination{{Server: "*", Namespace: "*"}},
			ClusterResourceWhitelist: []argoGroupKind{{Group: "*", Kind: "*"}},
		},
	}
}

// projectAppProject reshapes a REAL argoproj.io/v1alpha1 AppProject CR into the
// projected shape — name + ONLY the spec fields the UI reads. It never passes the
// CR spec through verbatim, so a real project cannot surface roles, jwtToken
// metadata, or any field this plane did not intend.
func projectAppProject(cr *unstructured.Unstructured) argoProject {
	desc, _, _ := unstructured.NestedString(cr.Object, "spec", "description")
	return argoProject{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "AppProject",
		Metadata:   argoMeta{Name: cr.GetName(), Namespace: cr.GetNamespace()},
		Spec: argoProjectSpec{
			SourceRepos:              nestedStringSlice(cr.Object, "spec", "sourceRepos"),
			Destinations:             nestedDestinations(cr.Object, "spec", "destinations"),
			ClusterResourceWhitelist: nestedGroupKinds(cr.Object, "spec", "clusterResourceWhitelist"),
			Description:              desc,
		},
	}
}

// nestedDestinations reads a []{server,namespace,name} slice from a CR.
func nestedDestinations(obj map[string]any, fields ...string) []argoDestination {
	raw, ok, _ := unstructured.NestedSlice(obj, fields...)
	if !ok {
		return nil
	}
	out := make([]argoDestination, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		server, _ := m["server"].(string)
		namespace, _ := m["namespace"].(string)
		name, _ := m["name"].(string)
		out = append(out, argoDestination{Server: server, Namespace: namespace, Name: name})
	}
	return out
}

// nestedGroupKinds reads a []{group,kind} slice from a CR.
func nestedGroupKinds(obj map[string]any, fields ...string) []argoGroupKind {
	raw, ok, _ := unstructured.NestedSlice(obj, fields...)
	if !ok {
		return nil
	}
	out := make([]argoGroupKind, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		group, _ := m["group"].(string)
		kind, _ := m["kind"].(string)
		out = append(out, argoGroupKind{Group: group, Kind: kind})
	}
	return out
}
