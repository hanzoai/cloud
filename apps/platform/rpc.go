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
// returned nothing: the in-process client it resolved is a package global that
// only exists in a binary which mounts platform.

// exposeFleet publishes the observer's view, bound to the service that owns the
// k8s client. fleetRoutes registers it, so the method is live exactly when the
// board it mirrors is — and it reads s directly rather than a package global,
// because that global IS the in-process client the caller was just moved off.
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
// because no kubeconfig resolved is the same lie as the nil client this replaced,
// only narrower. Letting the plane call it empty while /v1/platform/fleet calls
// it 503 would also be two answers to one fact. A REAL empty fleet — a client
// that resolved and found nothing — still returns an empty list with no error,
// because that is an observation; and an RBAC denial surfaces as an error, so a
// board never shows a denial as an empty estate.
func exposeFleet(s *cloud.Service[fleetState]) {
	zip.Post[struct{}, plane.Fleet](cloud.Plane(), "/platform/fleet",
		func(ctx context.Context, _ *cloud.Unit) (*plane.Fleet, error) {
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

// exposePush publishes the git-push-to-deploy trigger, bound to the service that
// owns the application store.
//
// It is the plane half of build.go's RegisterPushBuilder, and the sharpest case
// of the whole class. The push lands on
// GIT's embedded server; the builder is PLATFORM's. Those are two apps and
// therefore two processes, so the in-process registration was nil in the only
// process that ever fires it — and OnGitPush's contract was to return nil when
// unregistered. Every push in the split fleet triggered no build and reported
// success. Nothing logged it, because from the git side nothing had failed.
//
// The org is the CALLER's, never the argument: a push event able to name the org
// could enqueue a build against another tenant's applications, and the build it
// enqueues spends that tenant's compute.
// It reads the `mounted` global at CALL time rather than capturing the service at
// registration, which is what the push builder registered directly above it does.
// Shutdown sets that global back to nil, and a captured pointer would go on
// serving builds out of a torn-down store.
func exposePush() {
	zip.Post[plane.PushIn, plane.Built](cloud.Plane(), "/platform/push",
		func(ctx context.Context, in *plane.PushIn) (*plane.Built, error) {
			who := cloud.Who(ctx)
			if who.Org == "" {
				return nil, zip.ErrForbidden("platform push: org required")
			}
			s := mounted
			if s == nil {
				return nil, zip.Errorf(503, "platform push: platform not mounted")
			}
			launched, err := buildFromPush(s, ctx, cloud.GitPushEvent{
				Org: who.Org, Project: in.Project, Repo: in.Repo,
				Ref: in.Ref, Commit: in.Commit, CloneURL: in.CloneURL,
			})
			if err != nil {
				return nil, err
			}
			return &plane.Built{Repo: in.Repo, Builds: launched}, nil
		},
		zip.WithOperationID(plane.PlatformPush),
		zip.WithSummary("Turn a landed push into a build for every app tracking it"))
}

// exposeRelease publishes the first-party CR rollout, bound to the fleet service
// that owns the k8s client.
//
// Plane half of RegisterServiceReleaser, same story as exposePush: the
// build that PROVES an image and the control plane that PATCHES the CR are
// different apps, so OnServiceRelease's nil-when-unregistered meant every release
// reported a rollout that never touched a CR.
//
// Patched carries releaseService's own `changed`, so a caller learns whether the
// CR actually moved. That distinction is real here: a service declared in git is
// reconciled by Hanzo CD with selfHeal, and releaseService REFUSES to patch it —
// an error, which stays an error. Reporting a refusal as a rollout is the failure
// mode this op exists to end, so it is not smoothed over into a success.
func exposeRelease(s *cloud.Service[fleetState]) {
	zip.Post[plane.ReleaseIn, plane.Released](cloud.Plane(), "/platform/release",
		func(ctx context.Context, in *plane.ReleaseIn) (*plane.Released, error) {
			p := capPrincipal(cloud.Who(ctx))
			if !p.Validated {
				return nil, zip.ErrForbidden("platform release: authentication required")
			}
			if s == nil {
				return nil, zip.Errorf(503, "platform release: platform not mounted")
			}
			_, _, changed, err := releaseService(s, ctx, in.Service, in.Image)
			if err != nil {
				return nil, err
			}
			return &plane.Released{Patched: changed}, nil
		},
		zip.WithOperationID(plane.PlatformRelease),
		zip.WithSummary("Roll a proven, clean-semver image onto its operator Service CR"))
}
