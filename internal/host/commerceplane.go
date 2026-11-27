package host

// commerceplane.go — deps.Commerce in a process that does not link the commerce
// plugin. Each app runs as its own process, and only commerce's links the embedded
// commerce module, so entitlement, team and marketing used to get DisabledCommerce
// there and refuse every entitlement check: "commerce enabled but no client factory
// registered". The check crosses the plane to the commerce plugin by name instead,
// stating the org it is for; nothing leaves the cloud pod.

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/sdk"
	"strings"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk/plane"
)

// planeCommerce returns the commerce client that asks the commerce plugin over the
// plane.
func planeCommerce() cloud.CommerceClient { return commercePeer{} }

type commercePeer struct{}

// CheckEntitlement asks the commerce plugin whether orgID's plan licenses productID.
// A read that fails is an error, never an inactive entitlement.
func (commercePeer) CheckEntitlement(ctx context.Context, orgID, productID string) (*cloud.LicenseEntitlement, error) {
	orgID, productID = strings.TrimSpace(orgID), strings.TrimSpace(productID)
	if orgID == "" || productID == "" {
		return nil, errors.New("commerce.CheckEntitlement: empty orgID or productID")
	}
	out, err := commercepeer.BillingEntitlement(plane.For(ctx, orgID), &client.EntitlementIn{Product: productID})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("commerce.CheckEntitlement: commerce answered nothing")
	}
	return &cloud.LicenseEntitlement{ProductID: out.Product, Active: out.Active, Plan: out.Plan, Features: out.Features, ExpiresUnix: out.Expires, EarlyAccess: out.EarlyAccess}, nil
}
