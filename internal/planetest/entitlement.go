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

// Roled publishes the IAM role read on the plane, answering from roles.
//
// A framework lane enforces DocType permissions against the roles IAM gives the
// caller (apps/framework), so a lane test that defines a DocType or installs a
// module needs this peer or reads "System Manager role required".
func Roled(t *testing.T, roles func(org, user string) []string) {
	t.Helper()
	runtimeDir(t)

	app := zip.New(zip.Config{AppName: "iam"})
	zip.Post[struct{}, plane.Roles](app, "/iam/roles",
		func(ctx context.Context, _ *struct{}) (*plane.Roles, error) {
			who := zip.CallerOf(ctx)
			if who.Org == "" {
				return nil, zip.ErrUnauthorized("roles: no caller on the call")
			}
			return &plane.Roles{Roles: roles(who.Org, who.User)}, nil
		}, zip.WithOperationID(plane.IAMRoles))

	listen(t, app, "iam")
}

// Manager serves an IAM peer that makes every caller a System Manager — the
// shape a lane test wants when the lane, not the permission model, is under test.
func Manager(t *testing.T) {
	t.Helper()
	Roled(t, func(string, string) []string { return []string{"System Manager"} })
}
