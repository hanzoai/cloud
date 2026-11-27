package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// Durable, provider-keyed event de-duplication on the existing integrations Store
// (the same "integrations" store). The table is created in the store's migrate()
// (store.go) — fail-loud at Mount, one place. It exists because an agent turn is
// BILLED: a platform retry (Slack re-delivers on a non-2xx; Telegram re-delivers on
// a non-2xx; Discord retries interactions; Teams retries activities) must never
// trigger a SECOND billed run. Because it is DB-backed the guarantee survives a
// process restart. Keyed by (provider, event_key) so id spaces never collide across
// platforms.
//
// REPLICA SCOPE: the table is in the integrations store, which is the deployment's own
// (a schema on hanzo-sql, or the one file when there is no DSN), so every replica
// marks into the same table. The INSERT is the claim: of any number of replicas that
// are handed the same event, exactly one inserts the row and the rest see it taken,
// so a platform's retry that lands on another replica is a duplicate there too and
// never a second billed run.

// eventTTL bounds how long a dedupe row is retained. Every platform's retry horizon
// is minutes; a day is a wide margin, after which a row can no longer correspond to
// a live retry and is safe to reap.
const eventTTL = 24 * time.Hour

// MarkEvent is the atomic durable dedupe test-and-set: it inserts (provider,key) and
// returns fresh=true only on the FIRST sighting. A duplicate (a platform retry of
// the same event/update/interaction id) hits the PRIMARY KEY, the insert affects
// zero rows, and fresh=false, whichever replica made the first. An empty key is non-dedupable (fresh) — callers only
// dedupe non-empty keys onto the billed path. The single INSERT ... ON CONFLICT DO
// NOTHING + RowsAffected IS the single-use proof (no read-then-write race). A
// genuine DB error is surfaced so the caller fails CLOSED (skips the run) rather
// than risk a double charge.
func (s *Store) MarkEvent(ctx context.Context, provider, key string) (bool, error) {
	if key == "" {
		return true, nil
	}
	if err := dialect.Text(provider, key); err != nil {
		return false, fmt.Errorf("mark bridge event: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO bridge_events (provider, event_key, created_at) VALUES ($1, $2, $3)
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
	res, err := s.db.ExecContext(ctx, `DELETE FROM bridge_events WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("gc bridge events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// staleEventCutoff is the GC horizon: an event row older than the retry horizon can
// never match a live platform retry, so it is safe to reap.
func staleEventCutoff() int64 { return time.Now().Add(-eventTTL).Unix() }
