// Copyright © 2026 Hanzo AI. MIT License.

package entitlement

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// Whether an org has turned on one product, published on the internal plane.
//
// The caller is the refusal an ELECTIVE capability installs on its own prefixes
// (manifest.App.Elective, cloud.Elective): every request that reaches /v1/crm
// asks whether this org asked for crm, and a 404 is the answer when it has not.
// That caller runs in the binary serving the capability while the enablements
// live in this app's store, so the question crosses a process boundary — which
// is what the plane is.
//
// It is the twin of flags' hold and deliberately not the same op. A flag says
// whether a capability is finished enough to show a customer; this says whether
// the customer asked for it. Both can be false about the same product for
// unrelated reasons, and one op could not report which.

// exposeHolds publishes the enablement read. Mount calls it.
func exposeHolds() {
	zip.Post[plane.ProductIn, plane.Held](cloud.Plane(), "/entitlement/holds",
		holds,
		zip.WithOperationID(plane.EntitlementHolds),
		zip.WithSummary("Whether the caller's org has one product turned on"))
}

// holds reads one product FOR THE CALLER'S OWN ORG.
//
// The subject is the org, not a person: enablement says whether a customer has
// bought into a product, which is a fact about the tenant. So the caller names
// only the product, and has no way to name a subject at all — the shape that
// makes "check another org's entitlement" unrepresentable rather than merely
// refused.
//
// An unmounted store is an ERROR, never a false. The caller fails closed on it,
// and answering "not held" would make a subsystem that failed to start
// indistinguishable from a customer who has not subscribed — the two states that
// must never be confused, because one is our bug and the other is their choice.
func holds(ctx context.Context, in *plane.ProductIn) (*plane.Held, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("holds: no org on the call")
	}
	product := strings.TrimSpace(in.Product)
	if product == "" {
		return nil, zip.ErrBadRequest("holds: a product is required")
	}
	if mounted == nil || mounted.store == nil {
		return nil, zip.Errorf(503, "holds: the entitlement store is not mounted")
	}
	on, err := mounted.store.Holds(ctx, org, product)
	if err != nil {
		return nil, err
	}
	return &plane.Held{On: on}, nil
}
