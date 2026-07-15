// POST /v1/usage/rollup/backfill — the DEPLOY-GATED, run-ONCE seed of the derived
// rollup from pre-MV ledger history. SuperAdmin only.
//
// The incremental MV captures rows inserted AFTER its creation; this seeds everything
// before. Because SummingMergeTree accumulates, a second unguarded run would double a
// day — so it refuses when the rollup is already non-empty unless ?force=true. Pass
// ?before=<RFC3339> to bound the seed (default now); use the MV-creation instant so
// the seed and the live MV never overlap.
package leaderboard

import (
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

func backfillHandler(s *cloud.Service[state], c *zip.Ctx) error {
	if !principal.IsSuperAdmin(c) {
		return zip.ErrForbidden("SuperAdmin required")
	}
	if !datastoreEnabled() {
		return zip.Errorf(http.StatusServiceUnavailable, "datastore not connected")
	}
	ctx := c.Context()
	if err := EnsureUsageRollup(ctx); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "ensure rollup: %v", err)
	}

	before := time.Now().UTC()
	if raw := c.Query("before"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return zip.ErrBadRequest("before must be RFC3339")
		}
		before = t.UTC()
	}

	force := c.Query("force") == "true"
	if !force {
		n, err := rollupRowCount(ctx)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "rollup count: %v", err)
		}
		if n > 0 {
			return zip.Errorf(http.StatusConflict,
				"rollup already has %d rows; pass ?force=true to re-run (WILL double-count)", n)
		}
	}

	if err := BackfillUsageRollup(ctx, before); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "backfill: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"status":       "ok",
		"seededBefore": before.Format(time.RFC3339),
		"forced":       force,
	})
}
