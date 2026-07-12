// Copyright © 2026 Hanzo AI. MIT License.

package finance

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/types"
)

// MigrateOrg carries an org's existing prepaid balance into its native finance wallet,
// EXACTLY ONCE — the cutover primitive that moves each tenant's money from the legacy
// commerce ledger onto the ONE finance ledger. It deposits balanceCents into org's pooled
// wallet (subject == the org slug) under the FIXED idempotency ref "backfill:<org>", so a
// re-run (a retried operator call, a replayed job) finds the existing entry and credits
// the wallet at most once — returning the original entry id, moving no money. A
// non-positive balance is skipped ("" , nil): there is nothing to carry. It resolves the
// process-wide finance client published at boot; a nil client (finance not co-resident on
// this deployment) is an error, since there is no wallet to migrate into.
func MigrateOrg(ctx context.Context, org string, balanceCents int64) (string, error) {
	if balanceCents <= 0 {
		return "", nil
	}
	fin := Current()
	if fin == nil {
		return "", fmt.Errorf("finance: not published; cannot migrate %q", org)
	}
	return fin.Deposit(ctx, types.DepositInput{
		Org:      org,
		Subject:  org, // the org-pool wallet (subject == slug)
		Cents:    balanceCents,
		Currency: "usd",
		Notes:    "commerce balance backfill",
		Tags:     "backfill",
		Ref:      "backfill:" + org, // fixed ref → exactly-once cutover
	})
}
