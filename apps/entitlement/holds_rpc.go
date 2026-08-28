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
// The caller is apps/framework's refusal, which runs in the binary serving a
// module while the enablements live in this app's store.

// exposeHolds publishes the enablement read. Mount calls it.
func exposeHolds() {
	zip.Post[plane.ProductIn, plane.Held](cloud.Plane(), "/entitlement/holds",
		holds,
		zip.WithOperationID(plane.EntitlementHolds),
		zip.WithSummary("Whether the caller's org has one product turned on"))
}

// holds reads one product for the caller's own org. The caller names only the
// product, so "check another org's entitlement" is unrepresentable.
//
// An unmounted store is an error, never a false: a subsystem that failed to start
// must not read as a customer who has not subscribed.
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
