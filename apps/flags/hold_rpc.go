// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// Whether an org holds one flag, published on the internal plane.
//
// The caller is the refusal a capability that is not yet ga installs on its own
// prefixes (HIP-0139 §8, cloud.Stage): every request that reaches /v1/ads asks
// whether this org has been let into `ads`, and a 404 is the answer when it has
// not. That caller runs in the binary serving the capability, and the definitions
// live in this app's per-org store, so the question crosses a process boundary —
// which is what the plane is.

// exposeHold publishes the flag read. Mount calls it.
func exposeHold() {
	zip.Post[plane.FlagIn, plane.Flag](cloud.Plane(), "/flags/hold",
		hold,
		zip.WithOperationID(plane.FlagsHold),
		zip.WithSummary("Whether the caller's org holds one flag"))
}

// hold evaluates one flag FOR THE CALLER'S OWN ORG.
//
// The subject is the org, not a person: a capability flag says whether a customer
// has been let into a product, which is a fact about the tenant. So the org is
// both the tenant whose definitions are read and the distinct_id they are
// evaluated against, and a caller has no way to name either.
//
// It goes through [Assign], which is the ONE bucketing in this app — the same
// pure function of (key, subject, definition) that /v1/flags and the experiments
// primitive run. A second evaluator here would be a second answer to "does this
// org hold X", and the two would disagree on the day a rollout percentage is set.
//
// A flag this org has no definition for is OFF, which is a real answer and the
// right one: a capability nobody was let into is held by nobody. An engine that
// cannot answer is an ERROR, never a false — the caller fails closed on it, and
// silently reporting "not held" would make an outage indistinguishable from a
// decision.
func hold(ctx context.Context, in *plane.FlagIn) (*plane.Flag, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("hold: no org on the call")
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("hold: a flag key is required")
	}
	// The org's DEFAULT project. A capability is held by a tenant, and a project
	// is a subdivision inside one — reading a project scope off the call would let
	// two projects of one customer disagree about which products exist.
	a, err := Assign(org, "", key, org, nil)
	if err != nil {
		return nil, err
	}
	return &plane.Flag{On: a.On}, nil
}
