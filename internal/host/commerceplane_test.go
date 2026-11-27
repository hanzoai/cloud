package host

import (
	"context"
	"slices"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// The entitlement check from a process that does not link commerce crosses the plane
// to the commerce plugin, acts for the org it names, and reads the answer whole. A
// commerce that cannot answer is an error, never an inactive entitlement.
func TestPlaneCommerceAsksTheCommercePluginForTheOrg(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	var sawOrg, sawProduct string
	cloud.Plane().Post("/billing/entitlement",
		func(ctx context.Context, in *client.EntitlementIn) (*client.Entitlement, error) {
			sawOrg, sawProduct = cloud.Who(ctx).Org, in.Product
			if in.Product == "broken" {
				return nil, zip.Errorf(503, "entitlement unreadable")
			}
			return &client.Entitlement{Product: in.Product, Active: true, Plan: "max-5x", Features: []string{"inference"}, Expires: 1900000000}, nil
		},
		zip.WithOperationID(client.BillingEntitlement))
	stop, err := cloud.ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("ServePlane(commerce): %v", err)
	}
	defer func() { _ = stop() }()

	ent, err := planeCommerce().CheckEntitlement(context.Background(), "maxco", "engine")
	if err != nil {
		t.Fatalf("CheckEntitlement: %v", err)
	}
	if sawOrg != "maxco" || sawProduct != "engine" {
		t.Fatalf("the op acted for org %q product %q, want maxco engine", sawOrg, sawProduct)
	}
	if !ent.Active || ent.ProductID != "engine" || ent.Plan != "max-5x" || !slices.Equal(ent.Features, []string{"inference"}) || ent.ExpiresUnix != 1900000000 {
		t.Fatalf("entitlement read as %+v", ent)
	}
	if ent, err := planeCommerce().CheckEntitlement(context.Background(), "maxco", "broken"); err == nil {
		t.Fatalf("a commerce that could not read the entitlement answered %+v, want an error", ent)
	}
	if _, err := planeCommerce().CheckEntitlement(context.Background(), "", "engine"); err == nil {
		t.Fatal("an empty org was asked about")
	}
}
