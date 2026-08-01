package deploy

// The applications an operator actually has.
//
// /v1/deploy/applications is the documented list, and for the whole estate it
// listed nothing. It projects `hanzo.ai/v1` App CRs, of which the production
// cluster holds ZERO — the CRD is served, and nothing has ever created an
// instance of it (this package's own pin.go and release.go both record hitting
// that: "The CRD kind exists, but there has never been a `cloud` CR for it to
// patch"). The 328 applications that ARE deployed are `apps.hanzo.ai/v1alpha1`
// Applications, reconciled by Hanzo CD, and they were reachable only through
// /v1/deploy/gitops.
//
// So an operator asking the obvious endpoint the obvious question got `items:
// []`, correctly and uselessly. Two kinds, one word, and the documented one was
// the empty one.
//
// WHICH ONE IS THE ONE. They are not interchangeable and collapsing them would be
// wrong: `hanzo.ai/v1 App` is the TENANT plane — per-tenant namespace, labelled
// with its org, what a customer deploys — and `apps.hanzo.ai/v1alpha1
// Application` is the PLATFORM plane, what CD reconciles for the estate. The
// answer is therefore not "pick a kind" but "answer for the caller": a tenant
// asks about its own apps, an operator asks about the fleet, and this endpoint
// already knows which one it is talking to, because scope.namespaces() already
// branches on exactly that.
//
// So the tenant path is untouched — an org member reads its own namespace and its
// own org's CRs, and gains nothing here — and a platform SuperAdmin additionally
// gets the CD plane folded in. That is the same source /v1/deploy/gitops reads,
// under the same superAdmin fact, so no CR becomes visible to anyone who could not
// already read it: this widens the ENDPOINT, never the audience.
//
// The projection is honest about what it is. This endpoint already presents itself
// as an argoproj.io/v1alpha1 ApplicationList, so emitting real CD Applications in
// that envelope is MORE faithful than emitting App CRs relabelled into it, which is
// what it did before.

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hanzoai/cloud/apps/k8s"
)

// cdApplications lists the CD plane's Applications, cluster-wide, projected into
// the same argoApp the rest of this list carries. An absent CRD is not an error —
// a cluster without Hanzo CD simply contributes nothing, exactly as /v1/deploy/
// gitops treats it.
func (o ops) cdApplications(ctx context.Context) ([]argoApp, error) {
	list, err := o.s.State.dyn.Resource(k8s.CDApplications).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]argoApp, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, projectCDApp(observeGitOpsApp(&list.Items[i]), &list.Items[i]))
	}
	return out, nil
}

// projectCDApp maps one observed CD Application onto argoApp. It reuses
// observeGitOpsApp — the ONE reading of a CD Application in this package — so the
// list and /v1/deploy/gitops can never disagree about an application's sync state,
// health, or the revision it has applied.
func projectCDApp(g GitOpsApp, raw *unstructured.Unstructured) argoApp {
	return argoApp{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: argoMeta{
			Name:              g.Name,
			Namespace:         g.Namespace,
			UID:               string(raw.GetUID()),
			CreationTimestamp: raw.GetCreationTimestamp().Format("2006-01-02T15:04:05Z07:00"),
			Labels:            raw.GetLabels(),
		},
		Spec: argoSpec{
			Source: argoSource{
				RepoURL:        g.RepoURL,
				Path:           g.Path,
				TargetRevision: g.TargetRevision,
			},
			Project: g.Project,
		},
		Status: argoStatus{
			Sync:         argoSyncStatus{Status: g.Sync, Revision: g.Revision},
			Health:       argoHealth{Status: g.Health},
			Resources:    []argoResourceStatus{},
			Summary:      argoSummary{},
			ReconciledAt: g.ReconciledAt,
		},
	}
}
