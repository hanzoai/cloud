// gitops.go — GET /v1/deploy/gitops: the CD plane's OWN state, read from Hanzo
// CD's Application CRs (apps.hanzo.ai/v1alpha1, k8s.CDApplications).
//
// This is deliberately NOT another workload board. Every other read in this
// package projects operator App CRs — one row per workload, its declared vs
// running image tag. A CD Application is the layer ABOVE that: the git source CD
// polls, the commit it last applied, and the deploys it has performed. The two
// disagree in exactly the case an operator most needs to see — main carries a new
// image pin, CD has not applied that commit yet, so every App CR still declares
// the old tag and the drift board is legitimately "Synced" while the deploy has
// not landed. Reading the App CRs can never surface that; only the Application's
// applied revision can.
//
// `history` IS the recent-delivery feed: Hanzo CD records each applied revision
// with its start/finish, so the deploy log needs no second store and cannot drift
// from what actually happened.
//
// SuperAdmin-gated (guard) like the rest of the dashboard writes and bootstrap:
// the CD plane is fleet infrastructure with no tenant dimension. READ-ONLY — the
// operator view observes CD, it never drives it (sync policy here is `automated`
// with selfHeal, so the plane reconciles itself; the actionable verb an operator
// has is the per-app reconcile already served by dashSync).

package deploy

import (
	"github.com/hanzoai/cloud"
	"context"
	"sort"

	"github.com/hanzoai/cloud/k8s"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// gitOpsHistoryMax caps the per-Application deploy log this endpoint returns.
// Hanzo CD already keeps a bounded history; this only bounds the response.
const gitOpsHistoryMax = 10

// GitOpsDeploy is one revision Hanzo CD actually applied.
type GitOpsDeploy struct {
	// ID is CD's own sequence number for this deploy (status.history[].id). It
	// increases with every applied revision, so the largest id in `history` is the
	// most recent deploy — which is the first entry, since the list is reversed.
	ID int64 `json:"id"`
	// Revision is the git commit this deploy applied, as CD recorded it.
	Revision string `json:"revision"`
	// StartedAt is when CD began applying the revision (deployStartedAt), RFC 3339.
	// Absent when CD recorded none.
	StartedAt string `json:"startedAt,omitempty"`
	// DeployedAt is when the apply finished, RFC 3339. Absent when CD recorded none.
	DeployedAt string `json:"deployedAt,omitempty"`
	// Automated is whether CD started this deploy itself, from its own polling of
	// the tracked git ref (initiatedBy.automated), rather than someone asking for it.
	Automated bool `json:"automated"`
}

// GitOpsOperation is the LAST sync operation and how it ended — the honest answer
// to "did the most recent attempt succeed", which the sync verdict alone does not
// give (an Application is "Synced" to whatever revision it managed to apply).
type GitOpsOperation struct {
	// Phase is how the last sync operation ended, in CD's own vocabulary: Running,
	// Succeeded or Failed. It is never empty — an Application whose phase is empty
	// has no operation at all and omits this whole object.
	Phase string `json:"phase"`
	// Message is CD's account of the phase — "successfully synced (all tasks run)"
	// for a Succeeded operation, the reason it stopped for a Failed one.
	Message string `json:"message,omitempty"`
	// StartedAt is when the operation began, RFC 3339.
	StartedAt string `json:"startedAt,omitempty"`
	// FinishedAt is when it ended, RFC 3339. Absent while the phase is Running.
	FinishedAt string `json:"finishedAt,omitempty"`
	// Revision is the commit this operation ATTEMPTED (operationState.syncResult).
	// It differs from the Application's own revision exactly when the attempt did
	// not land: revision is the last commit CD got applied, this is the last one it
	// tried.
	Revision string `json:"revision,omitempty"`
}

// GitOpsApp is one CD Application: what it tracks, what it has applied, and how
// that went.
type GitOpsApp struct {
	// Name is what CD calls this tracked source, not the workload it deploys —
	// the Application CR's own metadata.name. The fleet ApplicationSet mints these
	// as <namespace>-<app>.
	Name string `json:"name"`
	// Namespace is where the Application OBJECT lives: CD's own controller
	// namespace, which is the same one for every row here. It is NOT the
	// destination the workloads land in — this endpoint lists cluster-wide and
	// never reads spec.destination.
	Namespace string `json:"namespace"`
	// Project is the AppProject fence the sync is admitted under: which repos this
	// Application may pull from and which destinations it may write to. Empty when
	// the CR declares none.
	Project string `json:"project,omitempty"`
	// RepoURL is the git repository CD polls for this Application's desired state.
	RepoURL string `json:"repoURL,omitempty"`
	// Path is the directory inside that repository CD renders, relative to its root.
	Path string `json:"path,omitempty"`
	// TargetRevision is the git ref CD TRACKS — usually a branch such as "main".
	// It is what CD aims at; Revision is what it has reached.
	TargetRevision string `json:"targetRevision,omitempty"`
	// Revision is the commit CD last APPLIED (status.sync.revision). Empty means it
	// has applied none — never read that as the head of TargetRevision.
	Revision string `json:"revision,omitempty"`
	// Sync is CD's verdict on git versus cluster, verbatim: Synced, OutOfSync or
	// Unknown. It is about the applied REVISION, so an Application can be Synced to
	// a commit that is several behind the branch it tracks.
	Sync string `json:"sync"`
	// Health is CD's verdict on the objects it manages, verbatim: Healthy,
	// Progressing, Degraded, Suspended, Missing or Unknown.
	Health string `json:"health"`
	// ReconciledAt is when CD last COMPARED this Application against git, RFC 3339.
	// It moves on every comparison, including ones that applied nothing.
	ReconciledAt string `json:"reconciledAt,omitempty"`
	// Automated is whether CD applies new commits without being asked. It reads the
	// PRESENCE of spec.syncPolicy.automated, which is a block rather than a
	// boolean; false means drift is reported and nothing moves.
	Automated bool `json:"automated"`
	// SelfHeal is whether CD also reverts changes made directly in the cluster
	// (syncPolicy.automated.selfHeal). Meaningless unless Automated.
	SelfHeal bool `json:"selfHeal"`
	// Resources is how MANY objects CD manages for this Application
	// (len(status.resources)) — a count, not the objects. Zero for an Application
	// CD has not reconciled.
	Resources int `json:"resources"`
	// Operation is the last sync attempt and how it ended. Absent when CD has run
	// none, which is the honest gap between "never tried" and "tried and failed".
	Operation *GitOpsOperation `json:"operation,omitempty"`
	// History is the recent deploy log, NEWEST FIRST and capped at ten. CD appends
	// oldest-first and bounds the list itself; the reversal happens here so a
	// caller never has to know the storage order to show what shipped last. Empty
	// (never null) for an Application that has deployed nothing.
	History []GitOpsDeploy `json:"history"`
}

// GitOpsPlane is the reply. `installed` is false — with an empty list and a
// reason — when the CD CRD is not served in this cluster. That is a FACT about
// the cluster, not a failure of this request, so the caller can say "no CD plane
// here" instead of rendering an error it cannot act on. A genuine transport or
// RBAC failure still errors (k8sErr).
type GitOpsPlane struct {
	// Installed is whether this cluster serves the CD Application CRD at all. False
	// is a fact about the cluster, not a failure of the request: the caller says
	// "no CD plane here" rather than rendering an error it cannot act on.
	Installed bool `json:"installed"`
	// Reason says why the plane is absent, in words a caller can show. Empty when
	// Installed.
	Reason string `json:"reason,omitempty"`
	// Applications is every CD Application in the cluster, ordered by namespace
	// then name. Empty (never null) when the plane is not installed, and equally
	// empty when it is installed and tracks nothing — Installed is what separates
	// those two.
	Applications []GitOpsApp `json:"applications"`
}

// GetDeployGitOps lists every Hanzo CD Application in the cluster: the git source
// each one polls, the commit it last APPLIED, how its last sync operation ended,
// and its recent deploy history — newest deploy first, ordered by namespace then
// name.
//
// This is the layer ABOVE the application board, and the two disagree in exactly
// the case an operator most needs to see: main carries a new image pin, CD has
// not applied that commit yet, so every App CR still declares the old tag and the
// application board is legitimately "Synced" while the deploy has not landed.
// Only the applied revision here can show that.
//
// installed is false — with a reason and an empty list — when the CD CRD is not
// served in this cluster. That is a FACT about the cluster rather than a failure
// of the request, so the caller can say "no CD plane here" instead of rendering
// an error it cannot act on; a genuine transport or RBAC failure still errors.
//
// Read-only, and platform SuperAdmin only: the CD plane is fleet infrastructure
// with no tenant dimension. This view observes CD and never drives it — the sync
// policy is automated with self-heal, and the actionable verb an operator has is
// the per-application reconcile at POST /v1/deploy/applications/{name}/sync.
func (o ops) gitops(ctx context.Context, _ *cloud.Unit) (*GitOpsPlane, error) {
	if _, err := superAdminOf(ctx); err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	// Cluster-wide: CD Applications live in the controller's namespace, and which
	// namespace that is, is CD's business — not a constant this plane should hold.
	list, err := o.s.State.dyn.Resource(k8s.CDApplications).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &GitOpsPlane{
				Reason:       "Hanzo CD is not installed in this cluster (no apps.hanzo.ai/Application CRD)",
				Applications: []GitOpsApp{},
			}, nil
		}
		return nil, k8sErr(o.s, "list", err)
	}
	apps := make([]GitOpsApp, 0, len(list.Items))
	for i := range list.Items {
		apps = append(apps, observeGitOpsApp(&list.Items[i]))
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Namespace != apps[j].Namespace {
			return apps[i].Namespace < apps[j].Namespace
		}
		return apps[i].Name < apps[j].Name
	})
	return &GitOpsPlane{Installed: true, Applications: apps}, nil
}

// observeGitOpsApp maps one Application CR to its view. Every field is read
// verbatim — the vocabulary (Synced/Healthy/Succeeded) is CD's own, so the
// console renders what CD said rather than a re-derivation that could disagree.
func observeGitOpsApp(cr *unstructured.Unstructured) GitOpsApp {
	obj := cr.Object
	str := func(fields ...string) string { v, _, _ := unstructured.NestedString(obj, fields...); return v }
	bl := func(fields ...string) bool { v, _, _ := unstructured.NestedBool(obj, fields...); return v }

	automated, hasAutomated, _ := unstructured.NestedMap(obj, "spec", "syncPolicy", "automated")
	resources, _, _ := unstructured.NestedSlice(obj, "status", "resources")

	app := GitOpsApp{
		Name:           cr.GetName(),
		Namespace:      cr.GetNamespace(),
		Project:        str("spec", "project"),
		RepoURL:        str("spec", "source", "repoURL"),
		Path:           str("spec", "source", "path"),
		TargetRevision: str("spec", "source", "targetRevision"),
		Revision:       str("status", "sync", "revision"),
		Sync:           str("status", "sync", "status"),
		Health:         str("status", "health", "status"),
		ReconciledAt:   str("status", "reconciledAt"),
		Automated:      hasAutomated && automated != nil,
		SelfHeal:       bl("spec", "syncPolicy", "automated", "selfHeal"),
		Resources:      len(resources),
		History:        gitOpsHistory(obj),
	}
	if phase := str("status", "operationState", "phase"); phase != "" {
		app.Operation = &GitOpsOperation{
			Phase:      phase,
			Message:    str("status", "operationState", "message"),
			StartedAt:  str("status", "operationState", "startedAt"),
			FinishedAt: str("status", "operationState", "finishedAt"),
			Revision:   str("status", "operationState", "syncResult", "revision"),
		}
	}
	return app
}

// gitOpsHistory reads status.history newest-first, capped. CD appends oldest-first
// and bounds the list itself; reversing here means the caller never has to know
// the storage order to show "what shipped last".
func gitOpsHistory(obj map[string]any) []GitOpsDeploy {
	raw, _, _ := unstructured.NestedSlice(obj, "status", "history")
	out := make([]GitOpsDeploy, 0, len(raw))
	for i := len(raw) - 1; i >= 0 && len(out) < gitOpsHistoryMax; i-- {
		entry, ok := raw[i].(map[string]any)
		if !ok {
			continue
		}
		id, _, _ := unstructured.NestedInt64(entry, "id")
		rev, _, _ := unstructured.NestedString(entry, "revision")
		started, _, _ := unstructured.NestedString(entry, "deployStartedAt")
		deployed, _, _ := unstructured.NestedString(entry, "deployedAt")
		automated, _, _ := unstructured.NestedBool(entry, "initiatedBy", "automated")
		out = append(out, GitOpsDeploy{
			ID:         id,
			Revision:   rev,
			StartedAt:  started,
			DeployedAt: deployed,
			Automated:  automated,
		})
	}
	return out
}
