package integrations

import (
	"context"
	"fmt"
	"time"
)

// Durable, provider-keyed event de-duplication on the existing integrations Store
// (same {DataDir}/integrations.db). The table is created in the store's migrate()
// (store.go) — fail-loud at Mount, one place. It exists because an agent turn is
// BILLED: a platform retry (Slack re-delivers on a non-2xx; Telegram re-delivers on
// a non-2xx; Discord retries interactions; Teams retries activities) must never
// trigger a SECOND billed run. Because it is DB-backed the guarantee survives a
// process restart. Keyed by (provider, event_key) so id spaces never collide across
// platforms.
//
// REPLICA SCOPE: this table lives in the per-PROCESS embedded SQLite file
// (Base/SQLite is single-writer per HIP-0302). It dedupes WITHIN one process; the
// billed webhook path MUST run single-replica (the shipping invariant). We always
// 2xx-ack, and platforms retry only on a non-2xx / timeout, so a duplicate to a
// second replica requires our ack to first exceed the platform's retry budget.
// Multi-replica is a follow-up (back this with a shared SETNX store keyed on
// (provider,event_key)).

// eventTTL bounds how long a dedupe row is retained. Every platform's retry horizon
// is minutes; a day is a wide margin, after which a row can no longer correspond to
// a live retry and is safe to reap.
const eventTTL = 24 * time.Hour

// MarkEvent is the atomic durable dedupe test-and-set: it inserts (provider,key) and
// returns fresh=true only on the FIRST sighting. A duplicate (a platform retry of
// the same event/update/interaction id) hits the PRIMARY KEY, the insert affects
// zero rows, and fresh=false. An empty key is non-dedupable (fresh) — callers only
// dedupe non-empty keys onto the billed path. The single INSERT ... ON CONFLICT DO
// NOTHING + RowsAffected IS the single-use proof (no read-then-write race). A
// genuine DB error is surfaced so the caller fails CLOSED (skips the run) rather
// than risk a double charge.
func (s *Store) MarkEvent(ctx context.Context, provider, key string) (bool, error) {
	if key == "" {
		return true, nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO bridge_events (provider, event_key, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(provider, event_key) DO NOTHING`, provider, key, time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("mark bridge event: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GCEvents reaps dedupe rows created before `before` (unix seconds), bounding the
// table's growth. Returns how many rows were removed. Called opportunistically from
// the webhook path so the table cannot accrete without bound.
func (s *Store) GCEvents(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bridge_events WHERE created_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("gc bridge events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// staleEventCutoff is the GC horizon: an event row older than the retry horizon can
// never match a live platform retry, so it is safe to reap.
func staleEventCutoff() int64 { return time.Now().Add(-eventTTL).Unix() }
