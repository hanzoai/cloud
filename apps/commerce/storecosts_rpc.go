// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.

package commerce

// storecosts_rpc.go — the vendor COGS god-view and the org's storefront, over
// the internal plane.
//
// Both were asked by re-entering commerce's own HTTP door: a request that leaves
// the process and comes back through the public edge, because commerce is a
// plugin in this binary rather than a deployment of its own. That re-entry is
// what apps/commerce/transport's maxDepth counter exists to survive. A call by
// name cannot express it.
//
// Each op asks commerce's OWN exported core — costs.Report, store.Current,
// store.SetListing — so the plane and the HTTP door answer the same function
// rather than two implementations that have to be kept in step.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	costsapi "github.com/hanzoai/commerce/api/costs"
	storeapi "github.com/hanzoai/commerce/api/store"
	"github.com/zap-proto/zip"
)

// planeCosts is what we paid every vendor in a period, and the total.
//
// It is a PLATFORM god-view, not a tenant read: the figures are the fleet's own
// COGS. It still resolves through payingOrg, because the books it walks are
// namespaced and the reserved admin org is where the platform's own live —
// asking without a validated caller would read an empty namespace and report
// that we pay our vendors nothing, which is the worst shape a cost report can
// take.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCosts(ctx context.Context, in *plane.CostsIn) (*plane.Costs, error) {
	org, err := payingOrg(ctx, "costs")
	if err != nil {
		return nil, err
	}
	var period string
	test := org.TestMode()
	if in != nil {
		period = in.Period
		if in.Test {
			test = true
		}
	}
	report := costsapi.Report(org.Namespaced(ctx), test, period)
	vendors := make([]plane.VendorCost, 0, len(report.Vendors))
	for _, v := range report.Vendors {
		vendors = append(vendors, plane.VendorCost{
			Vendor: v.Vendor, Service: v.Service, AmountCents: v.AmountCents,
			Period: v.Period, Source: string(v.Source), Note: v.Note, Currency: v.Currency,
		})
	}
	return &plane.Costs{
		Period: report.Period, Vendors: vendors,
		TotalCents: report.TotalCents, Currency: report.Currency,
	}, nil
}

// planeStore is the caller org's own storefront, provisioned on first ask.
//
// Resolved inside that org's namespace and nowhere else, so a store id can never
// cross a tenant boundary — StoreIn carries no fields at all, which is the same
// guarantee stated in the type.
func planeStore(ctx context.Context, _ *plane.StoreIn) (*plane.Store, error) {
	org, err := payingOrg(ctx, "store")
	if err != nil {
		return nil, err
	}
	s, err := storeapi.Current(org.Namespaced(ctx), org)
	if err != nil {
		return nil, zip.Errorf(502, "store: %v", err)
	}
	if s == nil {
		// A store we could not resolve is not a store named "default". Reporting
		// one would send a caller's listing into a store that does not exist.
		return nil, zip.Errorf(502, "store: commerce resolved no store for this org")
	}
	return &plane.Store{ID: s.Id(), Name: s.Name, Currency: string(s.Currency)}, nil
}

// planeListing upserts one product listing on the caller org's store.
//
// The patch is decoded ONTO the existing listing by the core, never replacing
// it, so a caller setting a header image preserves the curated name, price and
// copy it says nothing about.
func planeListing(ctx context.Context, in *plane.ListingIn) (*plane.Listed, error) {
	org, err := payingOrg(ctx, "listing")
	if err != nil {
		return nil, err
	}
	if in == nil || in.StoreID == "" || in.Key == "" {
		return nil, zip.ErrBadRequest("listing: storeId and key are required")
	}
	_, existed, err := storeapi.SetListing(org.Namespaced(ctx), org, in.StoreID, in.Key, in.Patch)
	if err != nil {
		return nil, zip.Errorf(502, "listing: %v", err)
	}
	return &plane.Listed{Existed: existed}, nil
}

// exposeStoreCosts publishes the three. Mount calls it.
func exposeStoreCosts() {
	zip.Post[plane.CostsIn, plane.Costs](cloud.Plane(), "/finance/costs", planeCosts,
		zip.WithOperationID(plane.FinanceCosts),
		zip.WithSummary("What we paid every vendor in a period"))
	zip.Post[plane.StoreIn, plane.Store](cloud.Plane(), "/store/current", planeStore,
		zip.WithOperationID(plane.StoreCurrent),
		zip.WithSummary("This org's storefront"))
	zip.Post[plane.ListingIn, plane.Listed](cloud.Plane(), "/store/listing", planeListing,
		zip.WithOperationID(plane.StoreListing),
		zip.WithSummary("Upsert one product listing"))
}
