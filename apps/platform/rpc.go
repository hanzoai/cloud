package platform

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// rpc.go — platform's methods on the internal plane.
//
// The fleet board is a READ of what the operator already reconciled, so it
// crosses as the projection admin renders, not as the k8s client that produced
// it. Reading it by import cost the caller client-go + apimachinery and still
// returned nothing: the in-process seam it resolved is a package global that
// only exists in a binary which mounts platform.

// exposeFleet publishes the observer's view, bound to the service that owns the
// k8s client. fleetRoutes registers it, so the method is live exactly when the
// board it mirrors is — and it reads s directly rather than a package global,
// because that global IS the in-process seam the caller was just moved off.
//
// AUTHORIZATION IS listFleet's, NOT A SECOND COPY. The role gate (mayObserve)
// and the tenant confinement (scopeNamespaces) are the ones the HTTP handler
// applies, reached here from the delegated capability instead of a request. That
// matters in both directions. A non-super caller is confined to its own org's
// namespaces AT THE SCAN, before any CR is listed, so it never lists another
// tenant's apps — and the overview KPIs have such a caller already, since
// AdmitScoped admits a white-label tenant's own admin. And the scan set is the
// DISCOVERED one, so a tenant-<org> namespace is exactly as visible here as on
// /v1/platform/fleet; observing through a second path would have given "what is
// the fleet" two answers, which is the duplicate definition this move removes.
//
// An observer with no k8s client answers 503 and says why — the SAME thing
// fleetReady already tells every HTTP route on this board. An unreachable estate
// and an empty estate must never look alike; that is equally true when the
// estate is merely unobservable, and a board reading "0 workloads, fleet ok"
// because no kubeconfig resolved is the same lie as the nil seam this replaced,
// only narrower. Letting the plane call it empty while /v1/platform/fleet calls
// it 503 would also be two answers to one fact. A REAL empty fleet — a client
// that resolved and found nothing — still returns an empty list with no error,
// because that is an observation; and an RBAC denial surfaces as an error, so a
// board never shows a denial as an empty estate.
func exposeFleet(s *cloud.Service[fleetState]) {
	zip.Post[struct{}, plane.Fleet](cloud.Plane(), "/platform/fleet",
		func(ctx context.Context, _ *struct{}) (*plane.Fleet, error) {
			// The input carries nothing and there is nothing for it to carry: every
			// fact that decides WHICH namespaces are observed comes from the caller,
			// so there is no field a caller could name a scope in.
			p := capPrincipal(cloud.Who(ctx))
			if !p.Validated {
				return nil, zip.ErrForbidden("platform fleet: authentication required")
			}
			if !p.mayObserve() {
				return nil, zip.ErrForbidden("platform fleet: admin required")
			}
			if s.State.dyn == nil {
				return nil, zip.Errorf(503, "platform fleet: kubernetes client not configured: %s", s.State.initErr)
			}
			views, err := observeFleet(s, ctx, scopeNamespaces(discoverNamespaces(s, ctx), p))
			if err != nil {
				return nil, err
			}
			out := make([]plane.App, 0, len(views))
			for _, v := range views {
				out = append(out, plane.App{
					Org:  v.Org,
					Name: v.App, Env: v.Env, Repo: v.Repo, Role: v.Role,
					Cluster: v.Cluster, Namespace: v.Namespace,
					Phase: v.Phase, Health: v.Health,
					DeclaredTag: v.DeclaredTag, RunningTag: v.RunningTag, LatestTag: v.LatestTag,
					Registry:      v.Registry,
					DriftSeverity: string(v.Drift.Severity),
				})
			}
			return &plane.Fleet{Apps: out}, nil
		},
		zip.WithOperationID(plane.PlatformFleet),
		zip.WithSummary("Every app this org can observe"))
}
