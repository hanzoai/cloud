// Copyright © 2026 Hanzo AI. MIT License.

package ai

// pool_rpc.go — the free pool's standing, answered by the process that spends it.
//
// The Free plan is limited usage from ONE pool every free user shares: the
// platform's vendor accounts for free models, spent in turn by the model API in
// this process (hanzoai/ai controllers/family_keys.go). What each account last said
// — at its per-minute limit, spent for the day, back at a reset — lives here and
// nowhere else, so the Free plan's usage page asks this process for it rather than
// keeping a second count that could disagree with the accounts.

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	aictl "github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// exposePool publishes the pool's standing on the internal plane. Use calls it.
func exposePool() {
	cloud.Plane().Post("/ai/pool", planePool,
		zip.WithOperationID(client.AIPool),
		zip.WithSummary("The free pool's standing across the platform's vendor accounts"))
}

// Answers the standing of the pool the Free plan is served from: whether the
// platform's vendor accounts for free models are available, busy or exhausted,
// and when the pool refills.
//
// It is what the accounts' own answers said and nothing else: an account is out
// only after the vendor refused it for its free quota, and back when the vendor
// said it would be. It names no key, no key id and no count of accounts, and it
// reads no tenant's data — the pool is one, shared by every free caller — so it
// needs no org.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planePool(_ context.Context, _ *client.PoolIn) (*client.Pool, error) {
	return poolOf(aictl.FreePool(), time.Now()), nil
}

// poolOf moves the model API's standing onto the wire. A refill is stated for an
// exhausted pool, and for a busy one only within the minute that "busy" promises:
// one account spent until midnight while the others serve is not a pool that
// comes back at midnight.
func poolOf(p aictl.Pool, now time.Time) *client.Pool {
	out := &client.Pool{State: p.State}
	if !p.Resets.IsZero() && (p.State == aictl.PoolExhausted || p.Resets.Sub(now) <= time.Minute) {
		out.Resets = p.Resets.Unix()
	}
	return out
}
