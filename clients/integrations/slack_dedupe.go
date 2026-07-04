package integrations

import (
	"context"
	"fmt"
	"time"
)

// Durable Slack event de-duplication, on the existing integrations Store (same
// {DataDir}/integrations.db file). The table is created in the store's migrate()
// (store.go) — fail-loud at Mount, one place. It exists because an agent turn is
// BILLED: a Slack retry (Slack re-delivers an event_id when it does NOT get a 2xx
// within ~3s) must never trigger a SECOND billed run, and because it is DB-backed
// the guarantee survives a process restart.
//
// REPLICA SCOPE (Red M2): this table lives in the per-PROCESS embedded SQLite file
// (Base/SQLite is single-writer per HIP-0302). It therefore dedupes WITHIN one
// process only — the guarantee does NOT span replicas. The unified cloud binary
// runs the integrations store as a single embedded-SQLite writer, so the billed
// Slack webhook path MUST run single-replica; that is the shipping invariant.
// Multi-replica is a follow-up (back this with a shared SETNX store — Valkey /
// Postgres — keyed on event_id). Note the residual is already small: we always
// 2xx-ack, and Slack only retries on a NON-2xx / timeout, so a duplicate delivery
// to a second replica requires our ack to first exceed Slack's ~3s budget.

// slackEventTTL bounds how long a dedupe row is retained. Slack's event retry
// horizon is minutes; a day is a wide margin, after which a row can no longer
// correspond to a live retry and is safe to reap.
const slackEventTTL = 24 * time.Hour

// MarkSlackEvent is the atomic durable dedupe test-and-set: it inserts event_key
// and returns fresh=true only on the FIRST sighting. A duplicate (a Slack retry of
// the same event_id / slash trigger_id) hits the PRIMARY KEY, the insert affects
// zero rows, and fresh=false. An empty key is non-dedupable (fresh) — callers only
// dedupe non-empty keys onto the billed path. The single INSERT ... ON CONFLICT DO
// NOTHING + RowsAffected IS the single-use proof (no read-then-write race),
// mirroring the store's ConsumeNonce. A genuine DB error is surfaced so the caller
// fails CLOSED (skips the run) rather than risk a double charge.
func (s *Store) MarkSlackEvent(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return true, nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO slack_events (event_key, created_at) VALUES (?, ?)
		 ON CONFLICT(event_key) DO NOTHING`, key, time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("mark slack event: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GCSlackEvents reaps dedupe rows created before `before` (unix seconds), bounding
// the table's growth. Returns how many rows were removed. Called opportunistically
// from the webhook path so the table cannot accrete without bound.
func (s *Store) GCSlackEvents(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM slack_events WHERE created_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("gc slack events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// staleSlackEventCutoff is the GC horizon: an event row older than the retry
// horizon can never match a live Slack retry, so it is safe to reap.
func staleSlackEventCutoff() int64 { return time.Now().Add(-slackEventTTL).Unix() }
