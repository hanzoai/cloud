package reward

// usage.go — the feeders that read what members have used: the token milestones and
// the first use of a model.
//
// BOTH READ THE USAGE ROLLUP (hanzo.usage_rollup_daily, the one derived lens over the
// metering ledger), never a request. A milestone is a crossing and not a sum: a
// member whose lifetime total rose past one or more thresholds since the last pass
// states one event for each threshold crossed, with their lifetime total, however
// many requests it took.
//
// FORWARD ONLY. The first pass starts at the day the feeders were switched on: what
// members used before is counted by the legacy backfill (backfill.go), grandfathered
// as the program says, and is not stated again as a milestone.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/reward/catalog"

	"github.com/hanzoai/cloud/datastore"
)

// milestones are the lifetime token totals that earn, in order: 100K, 1M, 5M, 10M,
// 100M, 1B, 10B, 100B, 1T. They are the program's seed rules' thresholds.
var milestones = []int64{
	100_000, 1_000_000, 5_000_000, 10_000_000, 100_000_000,
	1_000_000_000, 10_000_000_000, 100_000_000_000, 1_000_000_000_000,
}

// crossed are the milestones a total passed going from before to now: before < m <= now.
func crossed(before, now int64) []int64 {
	var out []int64
	for _, m := range milestones {
		if before < m && m <= now {
			out = append(out, m)
		}
	}
	return out
}

// The warehouse, variables so a test answers them.
var (
	queryFn = datastore.Query
	readyFn = datastore.Ready
)

const rollup = "hanzo.usage_rollup_daily"

// num and txt read a warehouse cell.
func num(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		x, _ := strconv.ParseInt(n, 10, 64)
		return x
	}
	return 0
}

func txt(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

// sweepUsage states the milestones crossed since the last pass.
func (s *state) sweepUsage(ctx context.Context) error {
	if !readyFn() {
		return nil
	}
	since, err := s.cursorOf(ctx, "usage")
	if err != nil {
		return err
	}
	now := today()
	if since == "" {
		since = now
	}
	rows, err := queryFn(ctx, `SELECT user_id, sum(total_tokens) AS total, sumIf(total_tokens, day >= toDate(?)) AS recent
		FROM `+rollup+`
		WHERE user_id IN (SELECT DISTINCT user_id FROM `+rollup+` WHERE day >= toDate(?) AND total_tokens > 0)
		GROUP BY user_id`, since, since)
	if err != nil {
		return fmt.Errorf("usage rollup: %w", err)
	}
	who, err := roster(ctx)
	if err != nil {
		return err
	}
	var stated int
	for _, r := range rows {
		sub, ok := who.bySlug[txt(r["user_id"])]
		if !ok {
			continue
		}
		total := num(r["total"])
		for _, m := range crossed(total-num(r["recent"]), total) {
			ok, err := s.once(ctx, catalog.Event{
				ID: "usage.tokens:" + sub + ":" + strconv.FormatInt(m, 10), Type: "usage.tokens", Subject: sub, At: time.Now().UTC(),
				Fields: map[string]any{"tokens": total, "model": ""},
			})
			if err != nil {
				return err
			}
			if ok {
				stated++
			}
		}
	}
	if stated > 0 {
		s.base.Log.Info("reward: token milestones stated", "events", stated)
	}
	return s.moveCursor(ctx, "usage", now)
}

// sweepModels states each model a member used for the first time since the last pass.
// A model counts after a task that took tokens, by the id the ledger records, which
// is the model the member asked for; an id that names a vendor's path
// (provider/model) is the model served and is not a Hanzo model, so it never counts.
func (s *state) sweepModels(ctx context.Context) error {
	if !readyFn() {
		return nil
	}
	since, err := s.cursorOf(ctx, "models")
	if err != nil {
		return err
	}
	now := today()
	if since == "" {
		since = now
	}
	rows, err := queryFn(ctx, `SELECT user_id, model, min(day) AS first
		FROM `+rollup+` WHERE total_tokens > 0 AND model != '' GROUP BY user_id, model HAVING first >= toDate(?)`, since)
	if err != nil {
		return fmt.Errorf("usage rollup: %w", err)
	}
	who, err := roster(ctx)
	if err != nil {
		return err
	}
	var stated int
	for _, r := range rows {
		sub, ok := who.bySlug[txt(r["user_id"])]
		model := strings.ToLower(strings.TrimSpace(txt(r["model"])))
		if !ok || model == "" || strings.Contains(model, "/") {
			continue
		}
		ok, err := s.once(ctx, catalog.Event{
			ID: "model.used:" + sub + ":" + model, Type: "model.used", Subject: sub, At: time.Now().UTC(),
			Fields: map[string]any{"model": model},
		})
		if err != nil {
			return err
		}
		if ok {
			stated++
		}
	}
	if stated > 0 {
		s.base.Log.Info("reward: first uses of a model stated", "events", stated)
	}
	return s.moveCursor(ctx, "models", now)
}
