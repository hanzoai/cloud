package sync

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// run_plane.go carries a reconcile trigger across a PROCESS boundary.
//
// The ENGINE is this app. The TRIGGERS are not: a GitHub push webhook lands on
// integrations, a native push lands on git, and neither of them is where the
// engine runs. cloud.RegisterSync only registers in-process, so syncFn was nil on
// every path that actually fires — cloud.Sync answered ErrSyncUnavailable while
// this engine was up in another process, and every mirror and every chained
// propagation silently stopped happening, reported as a missing registration
// rather than as the reachable call it was.
//
// The trigger travels instead. The socket has already decided who may ask (0600,
// SO_PEERCRED), and the tenant is the caller's plane identity rather than the
// argument, so a webhook routed to one org can never reconcile another's syncs.

// exposeRun publishes the reconcile on the internal plane. Mount calls it, beside
// cloud.RegisterSync — the two entry points onto the ONE engine, so a co-resident
// trigger and a remote one cannot reconcile differently.
func exposeRun() {
	zip.Post[client.SyncIn, client.SyncRan](cloud.Plane(), "/sync/run", planeRun,
		zip.WithOperationID(client.SyncRun),
		zip.WithSummary("Reconcile the syncs one upstream event fires"))
}

// planeRun reconciles every sync of the CALLER's org whose source matches the
// event, answering how many changed and how many were skipped.
//
// The org is the caller's plane identity and never the argument — client.SyncIn has
// no org field, deliberately, because a trigger able to state the org could
// reconcile another tenant's repositories. Anonymous is refused rather than
// defaulted: an event arriving with no principal must fail, not sync somebody's
// repos.
//
// It calls reconcileEvent, never cloud.Sync. cloud.Sync now falls through to THIS
// op when the local one is nil, so a process serving it that dispatched through
// it would dial its own socket and answer itself, forever.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRun(ctx context.Context, in *client.SyncIn) (*client.SyncRan, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("sync run: org required")
	}
	if mounted.Load() == nil {
		return nil, zip.Errorf(503, "sync not mounted")
	}
	res, err := reconcileEvent(ctx, cloud.SyncEvent{
		Kind: in.Kind, Provider: in.Provider, Org: who.Org, Locator: in.Locator,
		Repo: in.Repo, Ref: in.Ref, Before: in.Before, After: in.After,
		Actor: in.Actor, Token: in.Token, Manual: in.Manual, Hop: in.Hop,
	})
	if err != nil {
		return nil, err
	}
	return &client.SyncRan{Ran: res.Ran, Skipped: res.Skipped}, nil
}
