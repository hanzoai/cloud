package agents

// sessions_once.go — an event append that names itself is stored once.
//
// A client retries a POST after a 5xx only when the request names itself with a
// `requestId` (hanzo-client's one retry policy), on the platform's word that a
// named request is answered once. The audit trail refuses an append it could not
// record before anything is applied (503 audit_unavailable), so a turn that named
// itself is made again; and a 5xx or a timeout that DID apply must not put the
// turn in the transcript twice, whichever replica the repeat reaches.
//
// So the "once" is the store's: the read of an id already kept and the insert are
// one step under the org's write lock (Dialect.BeginWrite), and the unique index
// below is the backstop. A turn that names nothing is appended as it always was.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/zap-proto/zip"
)

// maxRequestID bounds a turn's own name, as apps/coding bounds a run's.
const maxRequestID = 256

// requestID is a turn's name as it is kept: trimmed, at most maxRequestID bytes,
// and no control character (it is compared, indexed and logged). Empty is a turn
// that names nothing.
func requestID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if len(id) > maxRequestID {
		return "", zip.ErrBadRequest("requestId too long")
	}
	if strings.ContainsFunc(id, unicode.IsControl) {
		return "", zip.ErrBadRequest("requestId must not contain control characters")
	}
	return id, nil
}

// migrateOnce gives the event table the column a turn's name is kept in and the
// index that holds one row per (session, name). Both are made only when the column
// is missing: a migration does not ask an existing table for an index it already
// has, because finding it takes the lock the table's writers hold.
func (s *Store) migrateOnce(ctx context.Context, tx *sql.Tx) error {
	have, err := s.d.HasColumn(ctx, tx, "agent_session_events", "request_id")
	if err != nil {
		return fmt.Errorf("migrate: look for agent_session_events.request_id: %w", err)
	}
	if have {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE agent_session_events ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate: add agent_session_events.request_id: %w", err)
	}
	// Partial, so the rows that name nothing (every row written before this, and
	// every unnamed turn after) are outside it and never collide on ''.
	if _, err := tx.ExecContext(ctx,
		`CREATE UNIQUE INDEX ux_events_session_request ON agent_session_events(session_id, request_id) WHERE request_id <> ''`); err != nil {
		return fmt.Errorf("migrate: index agent_session_events.request_id: %w", err)
	}
	return nil
}

// errRequestReused is a named append whose id this session already kept for another
// turn: another request under a reused id, refused 422 as apps/coding refuses one.
var errRequestReused = errors.New("the requestId was already used for a different turn; send a new requestId for a new turn")

// requested is the event a named request already stored on this session, if it
// did. The caller holds the org's write lock, so "not yet" stays true until its
// own insert. A kept turn that is not e (another kind, actor or payload) is
// errRequestReused: e's payload is already in its kept form (plainPayload).
func requested(ctx context.Context, tx *sql.Tx, e Event) (Event, bool, error) {
	var kept Event
	err := tx.QueryRowContext(ctx,
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=$1 AND session_id=$2 AND request_id=$3`,
		e.Org, e.SessionID, e.RequestID).
		Scan(&kept.ID, &kept.SessionID, &kept.Org, &kept.Seq, &kept.Kind, &kept.Actor, &kept.Payload, &kept.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, fmt.Errorf("find request: %w", err)
	}
	if kept.Kind != e.Kind || kept.Actor != e.Actor || kept.Payload != e.Payload {
		return Event{}, false, errRequestReused
	}
	kept.RequestID, kept.Replay = e.RequestID, true
	return kept, true, nil
}
