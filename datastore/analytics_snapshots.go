// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// analytics_snapshots.go — L3 MATERIALIZED SERVING SNAPSHOTS & CONTINUOUS REFRESHER.
//
// Maintains compact ReplacingMergeTree(refreshed_at) serving tables so interactive
// reads answer in 1–3ms via O(1) point lookups regardless of fleet size (100K–1M orgs):
//   - analytics.serving_global_overview    (1 row, refreshed every 5s from analytics.global_overview)
//   - analytics.serving_org_overview       (incremental per dirty org within 1–2s of mutation)
//   - analytics.serving_model_performance  (refreshed every 15s from analytics.model_performance)
//   - analytics.serving_provider_economics (refreshed every 15s from analytics.provider_economics)
//
// INVARIANTS:
//   - All refreshes compute ONLY from L1/L2 analytics rollups and state tables — NEVER
//     raw event.* tables and NEVER per-org RPCs.
//   - Single-instance guarded background loop with bounded concurrency (1 goroutine).
//   - Dirty-org notifications are non-blocking, deduplicated, bounded, and debounced.

const (
	globalSnapshotInterval        = 5 * time.Second
	modelProviderSnapshotInterval = 15 * time.Second
	dirtyOrgDebounce              = 500 * time.Millisecond
	snapshotRefreshTimeout        = 10 * time.Second
	dirtyOrgBatchSize             = 100
	maxDirtyOrgs                  = 4096
)

// buildGlobalOverviewSnapshotInsert builds the server-side INSERT ... SELECT that
// materializes one fresh row into `analytics.serving_global_overview` from L1/L2 rollups.
func buildGlobalOverviewSnapshotInsert(now time.Time) (string, []any) {
	ts := now.UTC()
	return `INSERT INTO ` + AnalyticsServingGlobalOverviewTable + `
		(scope, orgs_total, customer_orgs, members_total, orgs_with_books,
		 cash_minor, credit_minor, owed_minor, active_subscriptions, mrr_minor,
		 ai_requests_30d, ai_tokens_30d, ai_cost_nano_30d, ai_billed_nano_30d,
		 ai_margin_nano_30d, ai_cache_saved_nano_30d, spans_24h, errors_24h, refreshed_at)
	SELECT
		'global' AS scope,
		orgs_total,
		customer_orgs,
		members_total,
		orgs_with_books,
		cash_minor,
		credit_minor,
		owed_minor,
		active_subscriptions,
		mrr_minor,
		ai_requests_30d,
		ai_tokens_30d,
		ai_cost_nano_30d,
		ai_billed_nano_30d,
		ai_margin_nano_30d,
		ai_cache_saved_nano_30d,
		spans_24h,
		errors_24h,
		? AS refreshed_at
	FROM analytics.global_overview
	LIMIT 1`, []any{ts}
}

// buildModelPerformanceSnapshotInsert builds the server-side INSERT ... SELECT for
// `analytics.serving_model_performance`.
func buildModelPerformanceSnapshotInsert(now time.Time) (string, []any) {
	ts := now.UTC()
	return `INSERT INTO ` + AnalyticsServingModelPerformanceTable + `
		(model, provider, family, requests, tokens, prompt_tokens, completion_tokens,
		 cache_read_tokens, cost_nano, billed_nano, margin_nano, cache_saved_nano,
		 errors, error_rate, active_users, active_orgs, refreshed_at)
	SELECT
		model,
		provider,
		family,
		requests,
		tokens,
		prompt_tokens,
		completion_tokens,
		cache_read_tokens,
		cost_nano,
		billed_nano,
		margin_nano,
		cache_saved_nano,
		errors,
		error_rate,
		active_users,
		active_orgs,
		? AS refreshed_at
	FROM analytics.model_performance`, []any{ts}
}

// buildProviderEconomicsSnapshotInsert builds the server-side INSERT ... SELECT for
// `analytics.serving_provider_economics`.
func buildProviderEconomicsSnapshotInsert(now time.Time) (string, []any) {
	ts := now.UTC()
	return `INSERT INTO ` + AnalyticsServingProviderEconomicsTable + `
		(provider, requests, tokens, cost_nano, billed_nano, margin_nano, margin_bps,
		 cache_saved_nano, errors, error_rate, models, active_orgs, refreshed_at)
	SELECT
		provider,
		requests,
		tokens,
		cost_nano,
		billed_nano,
		margin_nano,
		margin_bps,
		cache_saved_nano,
		errors,
		error_rate,
		models,
		active_orgs,
		? AS refreshed_at
	FROM analytics.provider_economics`, []any{ts}
}

// buildOrgOverviewSnapshotInsert builds a predicate-pushed-down INSERT ... SELECT into
// `analytics.serving_org_overview` restricted strictly to the specified dirty orgs.
// Pushing `WHERE org IN (?,...)` into every L1/L2 branch guarantees zero full-fleet scan.
func buildOrgOverviewSnapshotInsert(orgs []string, now time.Time) (string, []any) {
	ts := now.UTC()
	ph := strings.Repeat("?,", len(orgs))
	ph = ph[:len(ph)-1]

	// 1 timestamp + 4 pushed-down IN clauses across dim_orgs, state_org_balances,
	// state_subscriptions, and rollup_ai_daily.
	args := make([]any, 0, 1+len(orgs)*4)
	args = append(args, ts)
	for range 4 {
		for _, o := range orgs {
			args = append(args, o)
		}
	}

	stmt := `INSERT INTO ` + AnalyticsServingOrgOverviewTable + `
		(org, display_name, is_internal, members, tier, status, created_at,
		 cash_minor, credit_minor, owed_minor, mrr_minor, seats,
		 ai_requests_30d, ai_tokens_30d, ai_cost_nano_30d, ai_billed_nano_30d, refreshed_at)
	SELECT
		o.org AS org,
		o.display_name,
		o.is_internal,
		o.members,
		o.tier,
		o.status,
		o.created_at,
		coalesce(b.cash_minor, 0) AS cash_minor,
		coalesce(b.credit_minor, 0) AS credit_minor,
		coalesce(b.owed_minor, 0) AS owed_minor,
		coalesce(s.mrr_minor, 0) AS mrr_minor,
		coalesce(s.seats, 0) AS seats,
		coalesce(a.ai_requests_30d, 0) AS ai_requests_30d,
		coalesce(a.ai_tokens_30d, 0) AS ai_tokens_30d,
		coalesce(a.ai_cost_nano_30d, 0) AS ai_cost_nano_30d,
		coalesce(a.ai_billed_nano_30d, 0) AS ai_billed_nano_30d,
		? AS refreshed_at
	FROM (
		SELECT
			org,
			argMax(display_name, updated_at) AS display_name,
			argMax(is_internal, updated_at) AS is_internal,
			argMax(members, updated_at) AS members,
			argMax(tier, updated_at) AS tier,
			argMax(status, updated_at) AS status,
			argMax(created_at, updated_at) AS created_at
		FROM analytics.dim_orgs
		WHERE org IN (` + ph + `)
		GROUP BY org
	) AS o
	LEFT JOIN (
		SELECT
			org,
			sum(cash_minor) AS cash_minor,
			sum(credit_minor) AS credit_minor,
			sum(owed_minor) AS owed_minor
		FROM (
			SELECT
				org,
				currency,
				argMax(cash_minor, version) AS cash_minor,
				argMax(credit_minor, version) AS credit_minor,
				argMax(owed_minor, version) AS owed_minor
			FROM analytics.state_org_balances
			WHERE org IN (` + ph + `)
			GROUP BY org, currency
		)
		GROUP BY org
	) AS b USING (org)
	LEFT JOIN (
		SELECT
			org,
			sumIf(mrr_minor, deleted = 0 AND test = 0 AND status = 'active') AS mrr_minor,
			sumIf(seats, deleted = 0 AND test = 0 AND status IN ('active', 'trialing')) AS seats
		FROM (
			SELECT
				org,
				id,
				argMax(deleted, version) AS deleted,
				argMax(test, version) AS test,
				argMax(status, version) AS status,
				argMax(mrr_minor, version) AS mrr_minor,
				argMax(seats, version) AS seats
			FROM analytics.state_subscriptions
			WHERE org IN (` + ph + `)
			GROUP BY org, id
		)
		GROUP BY org
	) AS s USING (org)
	LEFT JOIN (
		SELECT
			org,
			countMerge(requests) AS ai_requests_30d,
			sumMerge(tokens) AS ai_tokens_30d,
			sumMerge(cost_nano) AS ai_cost_nano_30d,
			sumMerge(billed_nano) AS ai_billed_nano_30d
		FROM analytics.rollup_ai_daily
		WHERE day >= today() - 30 AND org IN (` + ph + `)
		GROUP BY org
	) AS a USING (org)`

	return stmt, args
}

// RefreshGlobalOverviewSnapshot computes all 17 global overview KPIs from
// `analytics.global_overview` (L1/L2 rollups and state, never raw `event.*` or
// per-org RPCs), inserts 1 fresh row into `analytics.serving_global_overview`,
// and updates the `serving_global_overview` watermark.
func RefreshGlobalOverviewSnapshot(ctx context.Context) error {
	if !Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	start := time.Now().UTC()
	stmt, args := buildGlobalOverviewSnapshotInsert(start)
	if err := Exec(ctx, stmt, args...); err != nil {
		return fmt.Errorf("refresh serving_global_overview: %w", err)
	}
	done := time.Now().UTC()
	return UpsertWatermark(ctx, Watermark{
		Pipeline:      "serving_global_overview",
		Source:        "analytics.global_overview",
		WatermarkTS:   start,
		LagMS:         max(done.Sub(start).Milliseconds(), 0),
		RowsProcessed: 1,
		Status:        "fresh",
		Detail:        "l3 global overview snapshot refreshed",
		UpdatedAt:     done,
	})
}

// RefreshModelAndProviderSnapshots refreshes `analytics.serving_model_performance`
// and `analytics.serving_provider_economics` from their L1/L2 views and updates
// their respective watermarks.
func RefreshModelAndProviderSnapshots(ctx context.Context) error {
	if !Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}

	mStart := time.Now().UTC()
	mStmt, mArgs := buildModelPerformanceSnapshotInsert(mStart)
	if err := Exec(ctx, mStmt, mArgs...); err != nil {
		return fmt.Errorf("refresh serving_model_performance: %w", err)
	}
	mDone := time.Now().UTC()
	if err := UpsertWatermark(ctx, Watermark{
		Pipeline:    "serving_model_performance",
		Source:      "analytics.model_performance",
		WatermarkTS: mStart,
		LagMS:       max(mDone.Sub(mStart).Milliseconds(), 0),
		Status:      "fresh",
		Detail:      "l3 model performance snapshot refreshed",
		UpdatedAt:   mDone,
	}); err != nil {
		return err
	}

	pStart := time.Now().UTC()
	pStmt, pArgs := buildProviderEconomicsSnapshotInsert(pStart)
	if err := Exec(ctx, pStmt, pArgs...); err != nil {
		return fmt.Errorf("refresh serving_provider_economics: %w", err)
	}
	pDone := time.Now().UTC()
	return UpsertWatermark(ctx, Watermark{
		Pipeline:    "serving_provider_economics",
		Source:      "analytics.provider_economics",
		WatermarkTS: pStart,
		LagMS:       max(pDone.Sub(pStart).Milliseconds(), 0),
		Status:      "fresh",
		Detail:      "l3 provider economics snapshot refreshed",
		UpdatedAt:   pDone,
	})
}

// normalizeDirtyOrgs trims, deduplicates, and sorts a list of org slugs.
func normalizeDirtyOrgs(orgs []string) []string {
	if len(orgs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(orgs))
	out := make([]string, 0, len(orgs))
	for _, raw := range orgs {
		org := strings.TrimSpace(raw)
		if org == "" {
			continue
		}
		if _, ok := seen[org]; ok {
			continue
		}
		seen[org] = struct{}{}
		out = append(out, org)
	}
	slices.Sort(out)
	return out
}

// RefreshOrgOverviewSnapshot incrementally refreshes ONLY the specified dirty/mutated
// orgs into `analytics.serving_org_overview` in bounded chunks with zero full-fleet scan.
func RefreshOrgOverviewSnapshot(ctx context.Context, orgs []string) error {
	clean := normalizeDirtyOrgs(orgs)
	if len(clean) == 0 {
		return nil
	}
	if !Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	start := time.Now().UTC()
	for i := 0; i < len(clean); i += dirtyOrgBatchSize {
		end := min(i+dirtyOrgBatchSize, len(clean))
		batch := clean[i:end]
		stmt, args := buildOrgOverviewSnapshotInsert(batch, start)
		if err := Exec(ctx, stmt, args...); err != nil {
			return fmt.Errorf("refresh serving_org_overview batch [%d:%d]: %w", i, end, err)
		}
	}
	done := time.Now().UTC()
	return UpsertWatermark(ctx, Watermark{
		Pipeline:      "serving_org_overview",
		Source:        "analytics.org_overview",
		WatermarkTS:   start,
		LagMS:         max(done.Sub(start).Milliseconds(), 0),
		RowsProcessed: uint64(len(clean)),
		Status:        "fresh",
		Detail:        fmt.Sprintf("incrementally refreshed %d dirty orgs", len(clean)),
		UpdatedAt:     done,
	})
}

// dirtyOrgQueue coalesces rapid org mutation notifications without blocking callers.
type dirtyOrgQueue struct {
	mu     sync.Mutex
	set    map[string]struct{}
	cap    int
	wakeup chan struct{}
}

func newDirtyOrgQueue(capacity int) *dirtyOrgQueue {
	if capacity <= 0 {
		capacity = maxDirtyOrgs
	}
	return &dirtyOrgQueue{
		set:    make(map[string]struct{}, 64),
		cap:    capacity,
		wakeup: make(chan struct{}, 1),
	}
}

func (q *dirtyOrgQueue) mark(orgs ...string) {
	added := false
	q.mu.Lock()
	for _, raw := range orgs {
		org := strings.TrimSpace(raw)
		if org == "" {
			continue
		}
		if _, exists := q.set[org]; exists {
			continue
		}
		if len(q.set) >= q.cap {
			break
		}
		q.set[org] = struct{}{}
		added = true
	}
	q.mu.Unlock()
	if added {
		select {
		case q.wakeup <- struct{}{}:
		default:
		}
	}
}

func (q *dirtyOrgQueue) drain(maxBatch int) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.set) == 0 {
		return nil
	}
	n := len(q.set)
	if maxBatch > 0 && n > maxBatch {
		n = maxBatch
	}
	out := make([]string, 0, n)
	for org := range q.set {
		out = append(out, org)
		delete(q.set, org)
		if maxBatch > 0 && len(out) >= maxBatch {
			break
		}
	}
	if len(q.set) > 0 {
		select {
		case q.wakeup <- struct{}{}:
		default:
		}
	}
	slices.Sort(out)
	return out
}

func (q *dirtyOrgQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.set)
}

var globalDirtyOrgs = newDirtyOrgQueue(maxDirtyOrgs)

// MarkOrgSnapshotDirty enqueues org for incremental `analytics.serving_org_overview`
// refresh within 1–2 seconds. Safe for hot mutation paths: O(1), non-blocking,
// and coalesces repeated mutations for the same org.
func MarkOrgSnapshotDirty(org string) {
	globalDirtyOrgs.mark(org)
}

// MarkOrgsSnapshotDirty enqueues multiple orgs for incremental L3 snapshot refresh.
func MarkOrgsSnapshotDirty(orgs ...string) {
	globalDirtyOrgs.mark(orgs...)
}

type snapshotRefresherConfig struct {
	globalInterval        time.Duration
	modelProviderInterval time.Duration
	dirtyDebounce         time.Duration
	dirtyBatchSize        int
	refreshTimeout        time.Duration
	dirtyQueue            *dirtyOrgQueue
	isReady               func() bool
	refreshGlobal         func(context.Context) error
	refreshModelProvider  func(context.Context) error
	refreshOrgs           func(context.Context, []string) error
}

func runSnapshotRefresherLoop(ctx context.Context, cfg snapshotRefresherConfig, onExit func()) {
	defer func() {
		if onExit != nil {
			onExit()
		}
	}()

	runStep := func(fn func(context.Context) error) {
		if !cfg.isReady() {
			return
		}
		stepCtx, cancel := context.WithTimeout(ctx, cfg.refreshTimeout)
		defer cancel()
		_ = fn(stepCtx)
	}

	// Immediate initial materialization on boot so serving tables are warm right away.
	if cfg.isReady() {
		runStep(cfg.refreshGlobal)
		runStep(cfg.refreshModelProvider)
	}

	globalTick := time.NewTicker(cfg.globalInterval)
	defer globalTick.Stop()
	modelTick := time.NewTicker(cfg.modelProviderInterval)
	defer modelTick.Stop()

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time
	stopDebounce := func() {
		if debounceTimer != nil {
			if !debounceTimer.Stop() {
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer = nil
			debounceCh = nil
		}
	}
	defer stopDebounce()

	// If dirty orgs were already queued before StartAnalyticsSnapshotRefresher ran,
	// arm the debounce timer immediately.
	if cfg.dirtyQueue.pending() > 0 {
		debounceTimer = time.NewTimer(cfg.dirtyDebounce)
		debounceCh = debounceTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-globalTick.C:
			runStep(cfg.refreshGlobal)
		case <-modelTick.C:
			runStep(cfg.refreshModelProvider)
		case <-cfg.dirtyQueue.wakeup:
			if debounceTimer == nil {
				debounceTimer = time.NewTimer(cfg.dirtyDebounce)
				debounceCh = debounceTimer.C
			}
		case <-debounceCh:
			debounceTimer = nil
			debounceCh = nil
			batch := cfg.dirtyQueue.drain(cfg.dirtyBatchSize)
			if len(batch) > 0 {
				runStep(func(sctx context.Context) error {
					return cfg.refreshOrgs(sctx, batch)
				})
			}
			// If more dirty orgs remain queued beyond batch size, schedule next batch.
			if cfg.dirtyQueue.pending() > 0 {
				debounceTimer = time.NewTimer(cfg.dirtyDebounce)
				debounceCh = debounceTimer.C
			}
		}
	}
}

var snapshotRefresherRunning atomic.Bool

// StartAnalyticsSnapshotRefresher launches the single-instance guarded continuous
// L3 snapshot refresh loop with bounded concurrency (1 goroutine):
//   - 5s tick for `analytics.serving_global_overview`
//   - 15s tick for `analytics.serving_model_performance` and `analytics.serving_provider_economics`
//   - 500ms debounced dirty-org batching for `analytics.serving_org_overview`
//
// Calling StartAnalyticsSnapshotRefresher more than once while running is a safe no-op.
func StartAnalyticsSnapshotRefresher(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshotRefresherRunning.CompareAndSwap(false, true) {
		return
	}
	go runSnapshotRefresherLoop(ctx, snapshotRefresherConfig{
		globalInterval:        globalSnapshotInterval,
		modelProviderInterval: modelProviderSnapshotInterval,
		dirtyDebounce:         dirtyOrgDebounce,
		dirtyBatchSize:        dirtyOrgBatchSize,
		refreshTimeout:        snapshotRefreshTimeout,
		dirtyQueue:            globalDirtyOrgs,
		isReady:               Ready,
		refreshGlobal:         RefreshGlobalOverviewSnapshot,
		refreshModelProvider:  RefreshModelAndProviderSnapshots,
		refreshOrgs:           RefreshOrgOverviewSnapshot,
	}, func() {
		snapshotRefresherRunning.Store(false)
	})
}
