package agents

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/zap-proto/zip"
)

// sessions_control_drain.go is the CLI-facing half of the control channel. The
// dashboard POSTs steering commands (pause/resume/stop/message) which `control`
// records as durable KindControl events; a locally-started `hanzo code` session
// is NOT task-backed, so those events are never forwarded to a tasks engine —
// the running surface consumes them itself by polling here.
//
// GET /v1/agents/sessions/:id/control?after=<seq> returns the control commands
// newer than the caller's cursor, oldest first, with a cursor to poll from next.
// It is READ-ONLY and org-scoped (org is the ONLY tenant key): the same validated
// principal + same-org ownership that guards the session guards this, so a poller
// only ever drains its OWN session's commands and a foreign id is a clean 404.

// controlCommandView is one steering command as the CLI consumes it.
type controlCommandView struct {
	Seq     int64           `json:"seq"`
	Command string          `json:"command"`
	Message string          `json:"message,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// controlDrainIn is the poller's cursor over one session's steering queue.
type controlDrainIn struct {
	// ID is the session whose commands are being drained, from the path.
	ID string `json:"id"`
	// After is the last seq this poller applied; only commands newer than it come
	// back. Absent or negative reads as 0, which drains from the beginning.
	After int64 `json:"after"`
}

// controlDrain is a page of steering commands plus the cursor to poll from next.
type controlDrain struct {
	// Commands is the session's control commands newer than the cursor, oldest first.
	Commands []controlCommandView `json:"commands"`
	// Cursor is the seq to send as `after` on the next poll — the highest seq in
	// this page, or the cursor sent in when the page is empty.
	Cursor int64 `json:"cursor"`
}

// DrainSessionControl returns the steering commands (pause/resume/stop/message)
// recorded against the caller's own session that are newer than the cursor,
// oldest first, with the cursor to poll from next. It is how a locally started
// `hanzo code` session — which is not task-backed, so nothing forwards its
// commands to an execution engine — consumes what the dashboard posted. Read-only
// and bounded at 200 per poll, so a steady poll is cheap and an applied command is
// never redelivered.
//
// Example: {"id": "sess_1", "after": 12}
func (o sessionOps) drain(ctx context.Context, in *controlDrainIn) (*controlDrain, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if _, err := sto.GetSession(ctx, org, id); err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}

	// A negative cursor is not a position: it reads as 0, the same answer an
	// unparseable ?after= has always produced.
	after := max(in.After, 0)

	evs, err := sto.ListControlAfter(ctx, org, id, after, 200)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "drain control: %v", err)
	}

	cursor := after
	cmds := make([]controlCommandView, 0, len(evs))
	for _, e := range evs {
		var cp controlPayload
		_ = json.Unmarshal([]byte(e.Payload), &cp)
		cmds = append(cmds, controlCommandView{
			Seq:     e.Seq,
			Command: cp.Command,
			Message: cp.Message,
			Payload: cp.Payload,
		})
		if e.Seq > cursor {
			cursor = e.Seq
		}
	}
	return &controlDrain{Commands: cmds, Cursor: cursor}, nil
}

// ListControlAfter returns a session's KindControl events with seq > since,
// oldest first — the durable steering queue a running surface drains. Mirrors
// ListEvents but filters to control so a chatty session's message/log/tool-call
// events never dilute a poll.
func (s *Store) ListControlAfter(ctx context.Context, org, sessionID string, since int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=? AND session_id=? AND kind=? AND seq>?
		 ORDER BY seq ASC LIMIT ?`, org, sessionID, KindControl, since, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Org, &e.Seq, &e.Kind, &e.Actor,
			&e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
