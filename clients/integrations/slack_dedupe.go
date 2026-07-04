package integrations

import (
	"context"
	"fmt"
	"time"
)

// Durable Slack event de-duplication, added as methods on the existing
// integrations Store (same package, same {DataDir}/integrations.db file — no new
// store, no edit to store.go's migrate()). The table is created lazily on first
// bridge use (ensureSlackEvents). It exists because an agent turn is BILLED: a
// Slack retry (Slack re-delivers an event_id on a non-2xx or timeout) must never
// trigger a SECOND billed run, and because the guarantee is DB-backed it survives
// a process restart (and holds across replicas sharing the store) — an in-process
// set would not.

// slackEventTTL bounds how long a dedupe row is retained. Slack's event retry
// horizon is minutes; a day is a wide margin, after which a row can no longer
// correspond to a live retry and is safe to reap.
const slackEventTTL = 24 * time.Hour

// ensureSlackEvents creates the durable dedupe table. Idempotent (IF NOT EXISTS);
// called once per mounted store when the bridge is first used.
func (s *Store) ensureSlackEvents() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS slack_events (
  event_key  TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_slack_events_created ON slack_events(created_at);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("ensure slack_events: %w", err)
	}
	return nil
}

// MarkSlackEvent is the atomic durable dedupe test-and-set: it inserts event_key
// and returns fresh=true only on the FIRST sighting. A duplicate (Slack retry of
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
