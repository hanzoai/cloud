// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// entitlement_rpc.go — what an org's plan licenses, over the internal plane, for
// the processes that do not link commerce: entitlement, team and marketing ask it
// through deps.Commerce, which in their process is planeCommerce.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeEntitlement publishes the entitlement op. Mount calls it.
func exposeEntitlement() {
	cloud.Plane().Post("/billing/entitlement", planeEntitlement,
		zip.WithOperationID(client.BillingEntitlement),
		zip.WithSummary("Whether an org's plan licenses a product, and what it grants"))
}

// Answers whether the calling org holds an active entitlement for a licensed product,
// and what its plan grants, from commerce's own subscriptions. The org is the caller's,
// never a field of the request. An entitlement that cannot be read is an error, never
// an inactive one: a caller that took a read failure for "not entitled" would refuse a
// paying customer with nothing anywhere to find.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeEntitlement(ctx context.Context, in *client.EntitlementIn) (*client.Entitlement, error) {
	org, err := callerOrg(ctx, "entitlement")
	if err != nil {
		return nil, err
	}
	ent, err := InProcessClient().CheckEntitlement(ctx, org, in.Product)
	if err != nil {
		return nil, zip.Errorf(503, "entitlement unreadable: %v", err)
	}
	return &client.Entitlement{Product: ent.ProductID, Active: ent.Active, Plan: ent.Plan, Features: ent.Features, Expires: ent.ExpiresUnix, EarlyAccess: ent.EarlyAccess}, nil
}
