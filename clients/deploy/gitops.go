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
	"net/http"
	"sort"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/k8s"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// gitOpsHistoryMax caps the per-Application deploy log this endpoint returns.
// Hanzo CD already keeps a bounded history; this only bounds the response.
const gitOpsHistoryMax = 10

// GitOpsDeploy is one revision Hanzo CD actually applied.
type GitOpsDeploy struct {
	ID         int64  `json:"id"`
	Revision   string `json:"revision"`
	StartedAt  string `json:"startedAt,omitempty"`
	DeployedAt string `json:"deployedAt,omitempty"`
	Automated  bool   `json:"automated"`
}

// GitOpsOperation is the LAST sync operation and how it ended — the honest answer
// to "did the most recent attempt succeed", which the sync verdict alone does not
// give (an Application is "Synced" to whatever revision it managed to apply).
type GitOpsOperation struct {
	Phase      string `json:"phase"`
	Message    string `json:"message,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Revision   string `json:"revision,omitempty"`
}

// GitOpsApp is one CD Application: what it tracks, what it has applied, and how
// that went.
type GitOpsApp struct {
	Name           string           `json:"name"`
	Namespace      string           `json:"namespace"`
	Project        string           `json:"project,omitempty"`
	RepoURL        string           `json:"repoURL,omitempty"`
	Path           string           `json:"path,omitempty"`
	TargetRevision string           `json:"targetRevision,omitempty"`
	Revision       string           `json:"revision,omitempty"` // the commit last applied
	Sync           string           `json:"sync"`               // Synced|OutOfSync|Unknown
	Health         string           `json:"health"`             // Healthy|Degraded|Progressing|…
	ReconciledAt   string           `json:"reconciledAt,omitempty"`
	Automated      bool             `json:"automated"`
	SelfHeal       bool             `json:"selfHeal"`
	Resources      int              `json:"resources"`
	Operation      *GitOpsOperation `json:"operation,omitempty"`
	History        []GitOpsDeploy   `json:"history"`
}

// GitOpsPlane is the reply. `installed` is false — with an empty list and a
// reason — when the CD CRD is not served in this cluster. That is a FACT about
// the cluster, not a failure of this request, so the caller can say "no CD plane
// here" instead of rendering an error it cannot act on. A genuine transport or
// RBAC failure still errors (k8sErr).
type GitOpsPlane struct {
	Installed    bool        `json:"installed"`
	Reason       string      `json:"reason,omitempty"`
	Applications []GitOpsApp `json:"applications"`
}

// gitOps lists every Hanzo CD Application in the cluster, newest deploy first.
func gitOps(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	// Cluster-wide: CD Applications live in the controller's namespace, and which
	// namespace that is, is CD's business — not a constant this plane should hold.
	list, err := s.State.dyn.Resource(k8s.CDApplications).Namespace("").List(c.Context(), metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return c.JSON(http.StatusOK, GitOpsPlane{
				Reason:       "Hanzo CD is not installed in this cluster (no apps.hanzo.ai/Application CRD)",
				Applications: []GitOpsApp{},
			})
		}
		return k8sErr(s, "list", err)
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
	return c.JSON(http.StatusOK, GitOpsPlane{Installed: true, Applications: apps})
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
