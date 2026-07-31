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
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// BackfillReq bounds one seed run.
type BackfillReq struct {
	// Before bounds the seed to rows older than this RFC3339 instant; empty means now.
	// Use the materialized view's creation instant so the seed and the live view never overlap.
	Before string `json:"before"`
	// Force must be the exact string "true" to re-run against a non-empty rollup, which
	// WILL double-count. Anything else refuses with 409.
	Force string `json:"force"`
}

// BackfillResult reports one seed run.
type BackfillResult struct {
	// Status is ok when the seed ran.
	Status string `json:"status"`
	// SeededBefore is the RFC3339 bound the seed actually used.
	SeededBefore string `json:"seededBefore"`
	// Forced is whether the non-empty-rollup guard was overridden.
	Forced bool `json:"forced"`
}

// backfill seeds the derived usage rollup from pre-materialized-view ledger history.
// SuperAdmin only, and meant to run ONCE: because the
// rollup accumulates, a second run would double a day, so it refuses with 409 against
// a non-empty rollup unless force is set.
//
// Example: {"before": "2026-01-01T00:00:00Z"}
// Response: {"status": "ok", "seededBefore": "2026-01-01T00:00:00Z", "forced": false}
func (o ops) backfill(ctx context.Context, in *BackfillReq) (*BackfillResult, error) {
	c, hasReq := o.request(ctx)
	if !hasReq {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if !principal.IsSuperAdmin(c) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if !datastoreEnabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "datastore not connected")
	}
	if err := EnsureUsageRollup(ctx); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "ensure rollup: %v", err)
	}

	before := time.Now().UTC()
	if raw := in.Before; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, zip.ErrBadRequest("before must be RFC3339")
		}
		before = t.UTC()
	}

	force := in.Force == "true"
	if !force {
		n, err := rollupRowCount(ctx)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "rollup count: %v", err)
		}
		if n > 0 {
			return nil, zip.Errorf(http.StatusConflict,
				"rollup already has %d rows; pass ?force=true to re-run (WILL double-count)", n)
		}
	}

	if err := BackfillUsageRollup(ctx, before); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "backfill: %v", err)
	}
	return &BackfillResult{
		Status:       "ok",
		SeededBefore: before.Format(time.RFC3339),
		Forced:       force,
	}, nil
}
