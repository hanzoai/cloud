package finance

import (
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	ledger "github.com/hanzoai/cloud/clients/finance"
	"github.com/zap-proto/zip"
)

// Backfill answers POST /v1/admin/finance/backfill?org=<org> — the ONE-TIME cutover that
// carries an org's CURRENT commerce prepaid balance into the native finance wallet. It
// reads the pre-migration source of truth (the org's commerce balance for the org-pool
// subject == the org slug) and deposits it into finance under the FIXED ref
// "backfill:<org>", so re-running the cutover credits the wallet AT MOST ONCE. SuperAdmin
// only (core.Guard). Returns { org, migratedCents, entryId }; entryId is "" when the
// balance was non-positive (nothing to carry).
func Backfill(s *cloud.Service[core.State], c *zip.Ctx) error {
	org := strings.TrimSpace(c.Query("org"))
	if org == "" {
		return core.Fail(c, "org is required")
	}
	ctx := c.Context()

	// Pre-migration source of truth: the org's current commerce prepaid balance — the exact
	// figure the native wallet must start from.
	balance, err := s.State.Commerce.Credits(ctx, org)
	if err != nil {
		return core.Fail(c, "read commerce balance: "+err.Error())
	}

	entryID, err := ledger.MigrateOrg(ctx, org, int64(balance))
	if err != nil {
		return core.Fail(c, "finance backfill: "+err.Error())
	}
	return core.OK(c, map[string]any{
		"org":           org,
		"migratedCents": int64(balance),
		"entryId":       entryID,
	})
}
