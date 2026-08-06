package agents

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// sessions_rpc.go carries the login-manager teardown across a PROCESS boundary.
//
// The SESSIONS are this app's. The REVOKE is link's — it owns the credential row
// and is the surface a human logs out from — and agents ships as its own binary,
// so the direct call in apps/link/adapters.go reached a package that was never
// mounted in the link process. StopSessions answered (0, nil) for that, the
// revoke handler read it as "there were none", and every credential revoke on
// the fleet returned 200 {"sessionsStopped":0} while the sessions it was meant
// to tear down kept running under the revoked account.
//
// The match travels instead, through the SAME StopSessions the co-resident call
// uses — two doors, one teardown — so the actor scoping that bounds a revoke to
// its own user's sessions holds identically across the boundary.

// exposeSessions publishes the teardown and its count on the internal plane.
// Mount calls it, beside the in-process seam.
func exposeSessions() {
	zip.Post[plane.SessionMatchIn, plane.SessionCount](cloud.Plane(), "/agents/sessions/stop", planeStopSessions,
		zip.WithOperationID(plane.AgentsSessionsStop),
		zip.WithSummary("Stop the live sessions a credential revoke tears down"))

	zip.Post[plane.SessionMatchIn, plane.SessionCount](cloud.Plane(), "/agents/sessions/count", planeCountSessions,
		zip.WithOperationID(plane.AgentsSessionsCount),
		zip.WithSummary("Count the live sessions a match selects"))
}

// planeStopSessions tears down every live session of the CALLER's org matching
// the revoking subject, and reports how many it stopped.
//
// The org is the caller's plane identity and never the argument — plane
// .SessionMatchIn has no org field, deliberately, because this op STOPS things
// and a caller able to state the org could stop a co-tenant's work. Anonymous is
// refused rather than defaulted: a teardown arriving with no principal must
// fail, not pick a tenant.
//
// The actor is built HERE, from the org the plane proved and the subject the
// caller names, so the HIGH-1 actor scoping (a revoke stops only that user's own
// sessions) is enforced by the side that owns the store rather than trusted from
// the wire.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeStopSessions(ctx context.Context, in *plane.SessionMatchIn) (*plane.SessionCount, error) {
	m, err := matchFor(ctx, in)
	if err != nil {
		return nil, err
	}
	n, err := StopSessions(ctx, cloud.Who(ctx).Org, m)
	if err != nil {
		return nil, err
	}
	return &plane.SessionCount{Count: n}, nil
}

// planeCountSessions answers the active-session count the device view shows,
// under the same tenancy and actor rules as the stop above.
func planeCountSessions(ctx context.Context, in *plane.SessionMatchIn) (*plane.SessionCount, error) {
	m, err := matchFor(ctx, in)
	if err != nil {
		return nil, err
	}
	n, err := CountActiveSessions(ctx, cloud.Who(ctx).Org, m)
	if err != nil {
		return nil, err
	}
	return &plane.SessionCount{Count: n}, nil
}

// matchFor resolves the caller's proven org and qualifies the subject into an
// actor. It is the ONE place the wire shape becomes a SessionMatch, so the two
// ops cannot come to disagree about which sessions a caller may name.
func matchFor(ctx context.Context, in *plane.SessionMatchIn) (SessionMatch, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return SessionMatch{}, zip.ErrForbidden("agents sessions: org required")
	}
	actor := ""
	if s := strings.TrimSpace(in.Subject); s != "" {
		actor = BillingActor(org, s)
	}
	// An empty actor is left empty on purpose: the guard reads it as "match
	// nothing", so a request that lost its caller identity tears down nothing
	// instead of the org.
	return SessionMatch{Actor: actor, Host: in.Host, Provider: in.Provider, Account: in.Account}, nil
}
