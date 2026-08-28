// Copyright © 2026 Hanzo AI. MIT License.

package planetest

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// Entitled publishes the enablement read on the plane, answering from holds.
//
// A framework module refuses every document op whose module the caller's org has
// not enabled (apps/framework/elective.go), so a lane test that writes documents
// needs this peer or reads 404 on every write.
//
// holds is a predicate over (org, product) rather than a map so one helper covers
// both callers: a lane test says `p == "erp"` for every org, and the gate's own
// test discriminates by org.
func Entitled(t *testing.T, holds func(org, product string) bool) {
	t.Helper()
	// JOIN the runtime dir in effect, never claim a new one: repointing it strands
	// every peer already listening at the old address.
	runtimeDir(t)

	app := zip.New(zip.Config{AppName: "entitlement"})
	zip.Post[plane.ProductIn, plane.Held](app, "/entitlement/holds",
		func(ctx context.Context, in *plane.ProductIn) (*plane.Held, error) {
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("holds: no org on the call")
			}
			return &plane.Held{On: holds(org, in.Product)}, nil
		}, zip.WithOperationID(plane.EntitlementHolds))

	listen(t, app, "entitlement")
}
