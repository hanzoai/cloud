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
// ArgoCD-UI-compatible /v1/deploy/* surface (dashboard.go; no /api/, no inner /v1).

package deploy

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── ArgoCD v1alpha1 JSON (minimal, UI-render-complete) ───────────────────────

type argoListMeta struct {
	// ResourceVersion is the k8s list version a watch would resume from. Always
	// empty: every list on this plane is COMPUTED per request rather than read from
	// one etcd revision, so there is no point to resume from. The live view is the
	// SSE stream, not a resumed watch.
	ResourceVersion string `json:"resourceVersion"`
}

type argoMeta struct {
	// Name is the projected object's name: the App CR's metadata.name for an
	// application, the CD Application's name for a CD row, and the IAM project name
	// for a project.
	Name string `json:"name"`
	// Namespace is the namespace the source object was read from — the tenant or
	// platform namespace for an App CR, CD's controller namespace for a CD row.
	// Empty for a project synthesized here, which lives in no namespace.
	Namespace string `json:"namespace"`
	// UID is the k8s metadata.uid of the source object, which is what the SPA keys
	// a row on across refreshes. Empty for a synthesized project — there is no
	// object to take one from.
	UID string `json:"uid,omitempty"`
	// CreationTimestamp is when the source object was created, RFC 3339 to the
	// second. Empty for a synthesized project.
	CreationTimestamp string `json:"creationTimestamp,omitempty"`
	// Labels are the labels this projection puts on the row, not the source
	// object's full label set. An application projected from an App CR carries
	// hanzo.ai/instance (its name), hanzo.ai/env (main, test or dev, from the
	// namespace it was read from) and hanzo.ai/org when the CR declares a tenant. A
	// Hanzo CD Application carries the CR's own labels verbatim. A project
	// reflected from IAM carries hanzo.ai/org alone.
	Labels map[string]string `json:"labels,omitempty"`
}

type argoSource struct {
	// RepoURL is the git repository the desired state comes from. For an
	// application projected from an App CR it is the fleet manifest repo and is
	// DISPLAY ONLY — an App CR pins an image, and nothing is rendered from this
	// repo to produce it. For a CD row it is the repo CD actually polls.
	RepoURL string `json:"repoURL"`
	// Path is the directory within RepoURL. Display-only alongside a display-only
	// RepoURL; CD's own value for a CD row.
	Path string `json:"path"`
	// TargetRevision is the git ref tracked there — a branch such as "main".
	// Display-only for a projected App CR; the ref CD tracks for a CD row.
	TargetRevision string `json:"targetRevision"`
}

type argoDestination struct {
	// Server is the cluster API URL the application reconciles into. Everything
	// this plane projects lands in the cluster it runs in, so it is
	// https://kubernetes.default.svc — except on a project's destination fence,
	// where "*" means any cluster.
	Server string `json:"server"`
	// Namespace is where in that cluster the workload lands. "*" on a project's
	// destination fence means any namespace.
	Namespace string `json:"namespace"`
	Name      string `json:"name,omitempty"` // ArgoCD allows a destination by cluster name; omitted for the in-cluster projection.
}

type argoSpec struct {
	// Source is where the desired state is declared.
	Source argoSource `json:"source"`
	// Destination is which cluster and namespace it lands in. Zero-valued on a CD
	// row: this projection reports CD's source, not its destination.
	Destination argoDestination `json:"destination"`
	// Project is the AppProject this application is grouped and filtered under. For
	// an App CR it is the app.kubernetes.io/part-of label — the IAM project name —
	// falling back to "default" when the CR carries no such label.
	Project string `json:"project"`
}

type argoHealth struct {
	// Status is the ArgoCD health vocabulary, Capitalized: Healthy, Progressing,
	// Degraded, Suspended, Missing or Unknown. For an App CR it is derived per
	// object from what the operator reconciled (a workload with every replica ready
	// is Healthy, one scaled to zero is Suspended, a crash-looping pod is
	// Degraded); for a CD row it is the verdict CD wrote.
	Status string `json:"status"`
	// Message is why the status is what it is — "Running: no replicas ready",
	// "iam: CrashLoopBackOff". A Healthy object carries one too ("Running: all
	// replicas ready"), so this is not a failure signal. Always absent on a CD row,
	// which reports no health message.
	Message string `json:"message,omitempty"`
}

type argoSyncStatus struct {
	// Status is the ArgoCD sync vocabulary, Capitalized: Synced, OutOfSync or
	// Unknown. For an App CR it compares the tag the CR DECLARES against the tag
	// the cluster's Deployment is RUNNING — equal is Synced, both known and
	// different is OutOfSync, either unknown is Unknown. For a CD row it is CD's
	// own git-versus-cluster verdict.
	Status string `json:"status"`
	// Revision is what Status was reached against. For an App CR that is the
	// declared IMAGE TAG, not a commit — the CR is image-pinned. For a CD row it is
	// the commit CD last applied.
	Revision string `json:"revision,omitempty"`
}

type argoResourceStatus struct {
	// Group is the object's API group: empty for the core group (Pod, Service,
	// ConfigMap), otherwise apps, networking.k8s.io, autoscaling or policy — and
	// hanzo.ai for the App CR itself.
	Group string `json:"group,omitempty"`
	// Version is the object's API version as the live object reports it: v1 for
	// every kind here except the HorizontalPodAutoscaler, which is autoscaling/v2.
	Version string `json:"version,omitempty"`
	// Kind is the object kind — App, Deployment, ReplicaSet, Pod, Service, Ingress,
	// HorizontalPodAutoscaler, PodDisruptionBudget, ConfigMap. Never Secret: the
	// walk that produces these does not visit them.
	Kind string `json:"kind"`
	// Namespace is the namespace the object was found in — the same one for every
	// entry of an application, since the walk is confined to it.
	Namespace string `json:"namespace,omitempty"`
	// Name is the object's metadata.name.
	Name string `json:"name"`
	// Status is the APPLICATION's sync verdict repeated on every row, not a
	// per-object one. The operator owns these children, so no child has a desired
	// state of its own to compare against.
	Status string `json:"status,omitempty"`
	// Health is this object's own health, derived from its live state by the same
	// rule the resource tree uses.
	Health *argoHealth `json:"health,omitempty"`
}

type argoSummary struct {
	// Images are the container images the application runs. One entry for an App
	// CR, built from its spec.image as "repository:tag" — the bare repository when
	// it declares no tag, and absent when it declares neither. Absent on a CD row,
	// which tracks commits rather than images.
	Images []string `json:"images,omitempty"`
}

type argoStatus struct {
	// Sync is the declared-versus-running verdict and what it was reached against.
	Sync argoSyncStatus `json:"sync"`
	// Health is the application's reconciled health.
	Health argoHealth `json:"health"`
	// Resources are the objects the application owns. EMPTY on the list — filling
	// it would walk the cluster once per row — and populated only by the read of
	// ONE application, which is what makes that the detail view.
	Resources []argoResourceStatus `json:"resources"`
	// Summary is the small aggregate the list column renders: the images.
	Summary argoSummary `json:"summary"`
	// ReconciledAt is when the desired state was last compared against the cluster,
	// RFC 3339. Empty for an App CR — the projection derives its verdict at read
	// time and nothing records a comparison — and CD's own status.reconciledAt for
	// a CD row.
	ReconciledAt string `json:"reconciledAt,omitempty"`
}

type argoApp struct {
	// APIVersion is the constant "argoproj.io/v1alpha1" — the shape, not the source.
	// These are projections of operator App CRs and Hanzo CD Applications; no
	// argoproj.io object is stored anywhere behind this plane.
	APIVersion string `json:"apiVersion"`
	// Kind is the constant "Application".
	Kind string `json:"kind"`
	// Metadata is the projected object's identity.
	Metadata argoMeta `json:"metadata"`
	// Spec is the desired state: where it comes from, where it lands, what project
	// it belongs to.
	Spec argoSpec `json:"spec"`
	// Status is what was observed: the sync verdict, the health, and the owned
	// objects when this is a detail read.
	Status argoStatus `json:"status"`
}

type argoAppList struct {
	// APIVersion is the constant "argoproj.io/v1alpha1".
	APIVersion string `json:"apiVersion"`
	// Kind is the constant "ApplicationList".
	Kind string `json:"kind"`
	// Metadata is the list envelope the SPA expects; it carries no resume point.
	Metadata argoListMeta `json:"metadata"`
	// Items is one entry per operator App CR the caller may see — its own org's, or
	// every platform namespace's for a SuperAdmin — followed, for a SuperAdmin
	// only, by every Hanzo CD Application in the cluster. Empty (never null) rather
	// than absent when the caller owns nothing.
	Items []argoApp `json:"items"`
}

// ── resource tree (ArgoCD ApplicationTree) ───────────────────────────────────

// argoResourceRef addresses ONE live object in the tree. It is also EMBEDDED in
// argoNode, so each of these fields is a property of a node too.
type argoResourceRef struct {
	// Group is the object's API group: empty for the core group (Pod, Service,
	// ConfigMap), otherwise apps, networking.k8s.io, autoscaling or policy — and
	// hanzo.ai for the App CR at the root.
	Group string `json:"group,omitempty"`
	// Version is the object's API version as the live object reports it: v1 for
	// every kind the walk reaches except the HorizontalPodAutoscaler, which is
	// autoscaling/v2.
	Version string `json:"version,omitempty"`
	// Kind is the object kind. The root is the App CR; below it come Deployment,
	// ReplicaSet, Pod, Service, Ingress, HorizontalPodAutoscaler,
	// PodDisruptionBudget and ConfigMap. Never Secret — the walk does not visit
	// them, so no materialized environment can reach the tree.
	Kind string `json:"kind"`
	// Namespace is the namespace the walk ran in, the same for every node of one
	// tree.
	Namespace string `json:"namespace,omitempty"`
	// Name is the object's metadata.name.
	Name string `json:"name"`
	// UID is the object's metadata.uid. Absent on a PARENT reference, which
	// addresses its target by kind and name rather than by identity.
	UID string `json:"uid,omitempty"`
}

// argoInfoItem is one label/value chip the SPA renders on a node.
type argoInfoItem struct {
	// Name is the chip's label. The only one this projection produces is
	// "Image Tag".
	Name string `json:"name"`
	// Value is the chip's value — for "Image Tag", the tag the node runs.
	Value string `json:"value"`
}

type argoNode struct {
	argoResourceRef
	// ParentRefs are the node's edges UPWARD, which is how the SPA draws the DAG
	// from this flat list. Exactly one entry where present: a depth-1 object points
	// at the App CR, a ReplicaSet at its Deployment, a Pod at its ReplicaSet (or at
	// the Deployment whose selector matches it, when the ReplicaSet is gone).
	// Absent on the root.
	ParentRefs []argoResourceRef `json:"parentRefs,omitempty"`
	// Info are the chips shown on the node. At most one: the image tag — the
	// RUNNING tag on a Deployment, ReplicaSet or Pod, and the DECLARED tag on the
	// App CR at the root. Absent on a node that carries no image at all.
	Info []argoInfoItem `json:"info,omitempty"`
	// Health is the node's own derived health. Always present on a node of this
	// tree; a kind with no health signal of its own reports Healthy, since a
	// ConfigMap existing IS its healthy state.
	Health *argoHealth `json:"health,omitempty"`
	// ResourceVersion is the k8s version a watch would resume from. Always empty:
	// the tree is rebuilt from live reads on every request, including on every
	// frame of the SSE stream, so there is no revision to resume from.
	ResourceVersion string `json:"resourceVersion,omitempty"`
	// CreatedAt is the object's creationTimestamp, RFC 3339 UTC to the second.
	// Absent when the object carries none.
	CreatedAt string `json:"createdAt,omitempty"`
	// Images are the container images running on this node. Always absent — the tag
	// travels as the "Image Tag" chip in Info instead, which is where the SPA reads
	// it on a node.
	Images []string `json:"images,omitempty"`
}

type argoTree struct {
	// Nodes is the FLAT node list, root first: the App CR, then the objects the
	// operator owns, then their ReplicaSets and Pods. The hierarchy is in
	// ParentRefs, not in the ordering.
	Nodes []argoNode `json:"nodes"`
	// OrphanedNodes are objects in the namespace belonging to no application.
	// Always empty: this walk reaches an object only THROUGH ownership from the App
	// CR, so it can never hold one that is orphaned.
	OrphanedNodes []argoNode `json:"orphanedNodes"`
	// Hosts is ArgoCD's per-node machine inventory. Always empty: this plane
	// projects applications and serves no cluster-node view.
	Hosts []any `json:"hosts"`
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
	// Surface the tenant label the App CR already carries so the projection can be
	// grouped/scoped by org; env stays as before. The tenant BOUNDARY is enforced upstream
	// in the handlers (scope.allows) — this only reflects what the CR declares.
	labels := map[string]string{"hanzo.ai/instance": native.Name, "hanzo.ai/env": native.Env}
	if org := orgOf(cr); org != "" {
		labels[orgLabel] = org
	}
	return argoApp{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: argoMeta{
			Name:              native.Name,
			Namespace:         ns,
			UID:               string(cr.GetUID()),
			CreationTimestamp: cr.GetCreationTimestamp().Format("2006-01-02T15:04:05Z07:00"),
			Labels:            labels,
		},
		Spec: argoSpec{
			Source:      argoSource{RepoURL: deployManifestRepo, Path: "infra/k8s/operator/crs", TargetRevision: "main"},
			Destination: argoDestination{Server: inClusterServer, Namespace: ns},
			// spec.project reflects the IAM Project the CR belongs to (app.kubernetes.io/part-of),
			// falling back to "default" when the CR carries no project label. No longer hard-coded.
			Project: projectName(cr),
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
	// Status is ArgoCD's ConnectionStatus — Successful, Failed or Unknown. Always
	// Successful here: the destination is the cluster this process is already
	// running in, so it is reachable by construction and there is no credential to
	// probe.
	Status string `json:"status"`
	// Message is why a connection failed. Always absent, since none does.
	Message string `json:"message,omitempty"`
	// AttemptedAt is when the connection was last probed. Always absent: nothing is
	// probed, and a fabricated timestamp would claim a check that never ran.
	AttemptedAt string `json:"attemptedAt,omitempty"`
}

// argoClusterInfo is v1alpha1.ClusterInfo reduced to the connection + app count
// the UI's Destination/Clusters view reads. It carries NO credentials.
type argoClusterInfo struct {
	// ConnectionState repeats the cluster's own connection state, which is where
	// ArgoCD's UI reads it from on this object.
	ConnectionState argoConnectionState `json:"connectionState"`
	// ApplicationsCount is how many of THE CALLER'S applications reconcile into
	// this cluster, so a tenant sees its own count and a SuperAdmin the fleet's. It
	// is zero for the in-cluster destination when the caller owns nothing, since
	// that destination is listed whether or not anything targets it.
	ApplicationsCount int `json:"applicationsCount"`
	// ServerVersion is the kubernetes version of the destination. Always absent:
	// nothing here queries the API server for it.
	ServerVersion string `json:"serverVersion,omitempty"`
}

// argoCluster is v1alpha1.Cluster REDUCED to the projection-safe fields. There is
// DELIBERATELY no `config` field: a projection has no cluster credential to
// surface, so the type physically cannot carry a bearer token, TLS key, or exec
// provider. server + name + connectionState is what the Destination column reads.
type argoCluster struct {
	// Server is the destination's API URL, and the key the list is deduplicated by.
	// https://kubernetes.default.svc is this cluster.
	Server string `json:"server"`
	// Name is what the Destination column shows: "in-cluster" for this cluster,
	// otherwise whatever spec.destination.name declares, falling back to the server
	// URL when it declares none.
	Name string `json:"name"`
	// ConnectionState is whether the destination is reachable.
	ConnectionState argoConnectionState `json:"connectionState"`
	// Info is the connection state again plus the count of applications targeting
	// this destination.
	Info argoClusterInfo `json:"info"`
}

type argoClusterList struct {
	// Metadata is the list envelope the SPA expects; it carries no resume point.
	Metadata argoListMeta `json:"metadata"`
	// Items is one entry per distinct destination server, in first-seen order with
	// the in-cluster destination first. Never empty: an empty fleet still has the
	// one cluster it would deploy into.
	Items []argoCluster `json:"items"`
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

// argoGroupKind is metav1.GroupKind — a clusterResourceWhitelist entry.
type argoGroupKind struct {
	// Group is the API group a project admits, "*" for any. Empty names the core
	// group.
	Group string `json:"group"`
	// Kind is the kind it admits, "*" for any.
	Kind string `json:"kind"`
}

// argoProjectSpec is the subset of v1alpha1.AppProjectSpec the UI's project filter
// + detail read. Only these fields are surfaced (never the whole CR spec) so a
// real AppProject cannot leak roles/tokens or any field this plane didn't intend.
type argoProjectSpec struct {
	// SourceRepos are the git repos applications in this project may pull from.
	// ["*"] for every project this plane synthesizes or reflects from IAM: the
	// boundary that actually holds on this platform is the IAM org, resolved before
	// a row is ever projected, so the projected fence is deliberately permissive
	// and is NOT an authorization statement.
	SourceRepos []string `json:"sourceRepos"`
	// Destinations are the cluster/namespace pairs it may write to — a single
	// {server:"*", namespace:"*"} on a synthesized project, for the same reason.
	Destinations []argoDestination `json:"destinations"`
	// ClusterResourceWhitelist are the cluster-scoped kinds it may create —
	// [{group:"*", kind:"*"}] on a synthesized project.
	ClusterResourceWhitelist []argoGroupKind `json:"clusterResourceWhitelist"`
	// Description is the project's human label: the IAM project's display name, or
	// its description when it has no display name. Absent when IAM carries neither.
	Description string `json:"description,omitempty"`
}

// argoProject is v1alpha1.AppProject (projected). Project scoping on this platform
// is IAM/Org, not argocd RBAC, so a synthesized project is permissive.
type argoProject struct {
	// APIVersion is the constant "argoproj.io/v1alpha1". A project here is an IAM
	// resource wearing that shape; no argoproj.io object is stored behind it.
	APIVersion string `json:"apiVersion"`
	// Kind is the constant "AppProject".
	Kind string `json:"kind"`
	// Metadata is the project's identity: its name is the key an application's
	// spec.project matches, and is the same string an App CR carries in its
	// app.kubernetes.io/part-of label.
	Metadata argoMeta `json:"metadata"`
	// Spec is the fence the SPA displays — repos, destinations, admitted kinds.
	Spec argoProjectSpec `json:"spec"`
	// Status is always the empty object. A project has no reconciled state here;
	// the field exists because the SPA reads it.
	Status argoProjectStat `json:"status"`
}

// argoProjectStat marshals as the empty status object the UI expects.
type argoProjectStat struct{}

type argoProjectList struct {
	// Metadata is the list envelope the SPA expects; it carries no resume point.
	Metadata argoListMeta `json:"metadata"`
	// Items is the projects visible to the caller — its own organization's, or
	// every organization's for a SuperAdmin. A project named "default" is always
	// present and is prepended when IAM does not carry one, because that is what an
	// application with no project label groups under.
	Items []argoProject `json:"items"`
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
