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

	"github.com/zap-proto/zip"
)

// backfillQuery bounds and guards one seed of the rollup. Both fields ride the query
// string and both are optional.
type backfillQuery struct {
	// Before bounds the seed to ledger rows written before this RFC3339 instant.
	// Defaults to now; pass the incremental view's creation instant so the seed and
	// the live view never overlap and double a day.
	Before string `json:"before"`
	// Force must be exactly "true" to seed a rollup that already holds rows. It is
	// spelled as a string, not a flag, because the guard has always compared this
	// value literally — "1" and "yes" do NOT force.
	Force string `json:"force"`
}

// backfillResult reports what the seed did.
type backfillResult struct {
	// Status is "ok" — a seed that did not run answered an error instead.
	Status string `json:"status"`
	// SeededBefore is the RFC3339 upper bound the seed actually used.
	SeededBefore string `json:"seededBefore"`
	// Forced is true when the caller overrode the already-populated guard.
	Forced bool `json:"forced"`
}

// Backfill seeds the derived usage rollup from ledger history — the rows written
// before the incremental view existed, which that view can never capture. SuperAdmin
// only. Because the rollup accumulates, a second unguarded run would double every
// day it re-reads, so it refuses with 409 when the rollup already holds rows unless
// force=true is passed; forcing WILL double-count.
//
// Example: {"before": "2026-01-01T00:00:00Z"}
func (o boardOps) backfill(ctx context.Context, in *backfillQuery) (*backfillResult, error) {
	if !superOf(ctx) {
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
	return &backfillResult{
		Status:       "ok",
		SeededBefore: before.Format(time.RFC3339),
		Forced:       force,
	}, nil
}
