// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// rate_rpc.go — what one unit of metered work costs, over the internal plane.
//
// The platform meters work in several apps — a GB-month of block storage, a
// thousand characters translated, a risk screen — and each of them priced it
// with a compiled constant plus an env override, with the same resolver copied
// beside it. An env var keeps no history, so nothing could answer what we
// charged in March.
//
// The authority is commerce's, because commerce owns the ledger those charges
// land in. The apps that meter are separate processes and cannot open it, so
// they ask the process that does — the same shape resource_billing_peer.go uses
// for the spend gate, and for the same reason.
//
// IT TAKES NO SUBJECT. What a GB-month costs does not depend on whose bytes they
// are; a plan may include some of it and a wallet pays for the rest, and both of
// those are asked elsewhere.

import (
	"context"
	"net/http"
	"strings"

	commercerate "github.com/hanzoai/commerce/models/rate"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// exposeRate publishes the meter-price op. Mount calls it.
func exposeRate() {
	zip.Post[client.RateIn, client.Rate](cloud.Plane(), "/billing/rate", planeRate,
		zip.WithOperationID(client.BillingRate),
		zip.WithSummary("What one unit of a metered product costs"))
}

// planeRate reads one row of the meter authority.
//
// AN ABSENT RATE IS NOT AN ERROR. It answers Found=false and the caller falls
// back to the floor it compiled in, because a meter nobody has published a price
// for is the ordinary state of a new one — and failing the work would stop a
// customer's storage from provisioning over a missing price row. A STORE failure
// is a different thing and is returned as one: that is the authority being
// unwell, not the rate being absent, and a caller that cannot tell them apart
// would quietly charge its floor while the real price sat unreadable.
func planeRate(ctx context.Context, in *client.RateIn) (*client.Rate, error) {
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"rate: commerce is not co-resident in this process")
	}
	product, meter := strings.TrimSpace(in.Product), strings.TrimSpace(in.Meter)
	if product == "" || meter == "" {
		return nil, zip.ErrBadRequest("rate: product and meter are required")
	}

	// Bound through the MODEL's own rule rather than joined here, so the address
	// this reads by and the identity a write derives cannot disagree.
	key := &commercerate.Rate{Product: product, Meter: meter}
	key.Bind()

	row := commercerate.New(commercerate.AuthorityDB(ctx))
	found, err := row.Query().Filter("Slug=", key.Slug).Get()
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "rate: read %s: %v", key.Slug, err)
	}
	if !found {
		return &client.Rate{Product: product, Meter: meter}, nil
	}
	// An archived or draft rate is not what the platform charges today, so it
	// reads as absent and the caller takes its floor — the same answer the public
	// plan catalog gives for a row that is stored but not sold.
	if !row.Listed() {
		return &client.Rate{Product: product, Meter: meter}, nil
	}
	return &client.Rate{
		Product: row.Product,
		Meter:   row.Meter,
		Unit:    row.Unit,
		Nano:    row.Rate,
		Found:   true,
	}, nil
}
