// Copyright © 2026 Hanzo AI. MIT License.

package ai

// spend_rpc.go — what each API key has spent, answered by the process that counts
// it. The count is kept where the model calls are recorded (record), so the key
// list asks this process rather than keeping a second count that could disagree.

import (
	"context"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// spendOf is the limiter planeSpend reads. Set once by exposeSpend.
var spendOf *limits.Limiter

// exposeSpend publishes the per-key spend on the internal plane. Use calls it.
func exposeSpend(lim *limits.Limiter) {
	spendOf = lim
	cloud.Plane().Post("/ai/spend", planeSpend,
		zip.WithOperationID(client.AISpend),
		zip.WithSummary("What each of an org's API keys has spent, this month and in all"))
}

// Answers what each named API key of the caller's org has spent on models, this
// calendar month (UTC) and in all, in cents rounded up. A key that has spent
// nothing answers zeros. The org is the caller's, from the plane, so one org can
// never read another's keys.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSpend(ctx context.Context, in *client.KeysIn) (*client.KeySpends, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("an org is required")
	}
	if spendOf == nil {
		return nil, zip.Errorf(503, "key spend is not counted in this process")
	}
	var keys []string
	if in != nil {
		keys = in.Keys
	}
	out := &client.KeySpends{Keys: make([]client.KeySpend, 0, len(keys))}
	for _, key := range keys {
		month, total, err := spendOf.Spent(ctx, org, key)
		if err != nil {
			return nil, zip.Errorf(503, "key spend is unavailable right now")
		}
		out.Keys = append(out.Keys, client.KeySpend{Key: key, Month: centsUp(month), Total: centsUp(total)})
	}
	return out, nil
}

// centsUp turns nano-dollars into whole cents, rounded up, so a sub-cent spend is
// never shown as nothing.
func centsUp(nanos int64) int64 { return (nanos + 9_999_999) / 10_000_000 }

// nanosUp reads a usage event's exact decimal USD as nano-dollars, rounded up.
func nanosUp(usd string) (int64, error) {
	if usd == "" {
		return 0, nil
	}
	a, err := money.ParseUSD(usd)
	if err != nil {
		return 0, err
	}
	return limits.NanosUp(a), nil
}
