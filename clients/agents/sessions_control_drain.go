package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
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

// drainControl returns KindControl commands with seq > ?after for the caller's
// own session. Bounded (200/poll) and cursor-driven, so a steady poll is cheap
// and never redelivers an applied command.
func drainControl(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	if _, err := s.State.store.GetSession(c.Context(), org, id); err == errSessionNotFound {
		return zip.ErrNotFound("session not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}

	after := int64(0)
	if q := strings.TrimSpace(c.Query("after")); q != "" {
		if n, perr := strconv.ParseInt(q, 10, 64); perr == nil && n >= 0 {
			after = n
		}
	}

	evs, err := s.State.store.ListControlAfter(c.Context(), org, id, after, 200)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "drain control: %v", err)
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
	return c.JSON(http.StatusOK, map[string]any{"commands": cmds, "cursor": cursor})
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
		`SELECT id,session_id,org,seq,kind,actor,payload,created_at
		 FROM agent_session_events WHERE org=? AND session_id=? AND kind=? AND seq>?
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
