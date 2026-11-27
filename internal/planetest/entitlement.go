// Copyright © 2026 Hanzo AI. MIT License.

package planetest

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
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
	app.Post("/entitlement/holds",
		func(ctx context.Context, in *client.ProductIn) (*client.Held, error) {
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("holds: no org on the call")
			}
			return &client.Held{On: holds(org, in.Product)}, nil
		}, zip.WithOperationID(client.EntitlementHolds))

	listen(t, app, "entitlement")
}

// Roled publishes the IAM role read on the plane, answering from roles.
//
// A framework lane enforces DocType permissions against the roles IAM gives the
// caller (apps/framework), so a lane test that defines a DocType or installs a
// module needs this peer or reads "System Manager role required".
func Roled(t *testing.T, roles func(org, user string) []string) {
	t.Helper()
	Ruled(t, func(org, user string) client.Roles { return client.Roles{Roles: roles(org, user)} })
}

// Ruled publishes the IAM role read answering the whole of it — the names AND
// whether the caller administers the org, which a test may set apart, the way a
// tenant-named "System Manager" role is apart from the grant.
func Ruled(t *testing.T, answer func(org, user string) client.Roles) {
	t.Helper()
	Asked(t, func(who zip.Caller) client.Roles { return answer(who.Org, who.User) })
}

// Asked publishes the IAM role read answering from the whole caller the plane
// delivered — the admin bits the identity boundary rendered included — so a test
// can hold a surface to what reaches IAM across the socket, not only to the org
// and the user.
func Asked(t *testing.T, answer func(who zip.Caller) client.Roles) {
	t.Helper()
	runtimeDir(t)

	app := zip.New(zip.Config{AppName: "iam"})
	app.Post("/iam/roles",
		func(ctx context.Context, _ *struct{}) (*client.Roles, error) {
			who := zip.CallerOf(ctx)
			if who.Org == "" {
				return nil, zip.ErrUnauthorized("roles: no caller on the call")
			}
			out := answer(who)
			return &out, nil
		}, zip.WithOperationID(client.IAMRoles))

	listen(t, app, "iam")
}

// Manager serves an IAM peer that makes every caller a System Manager — the
// shape a lane test wants when the lane, not the permission model, is under test.
func Manager(t *testing.T) {
	t.Helper()
	Roled(t, func(string, string) []string { return []string{"System Manager"} })
}
