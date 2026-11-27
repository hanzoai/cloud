package agents

// steer.go — a person speaking into a turn that is still running.
//
// A message said in a conversation while the asker's turn was working started a
// SECOND turn beside it, and the steering API recorded a `message` command that
// nothing in the run ever read. So "no, the other repo" became a race between two
// runs, one of them working from the old instruction. Now what is said into a
// running turn is the turn's next input: the tool loop reads it between rounds
// and hands it to the model as the person's own words (completeWithTools).
//
// THE CURSOR IS ON THE ROW. Each message is a durable control event, and the
// session keeps the seq of the last one its run was handed (steered), moved in
// the same transaction that reads them. So none is handed over twice, and one
// said while the turn was paused is handed to the run that resumes it — in this
// process or the next.
//
// THE LAST LOOK SEALS THE TURN. A model that has answered has one more question
// to ask before its answer is final: did anyone speak while it was writing? The
// run asks, and when the answer is no it closes the turn to further messages in
// the SAME transaction. A message arriving after that is refused — the surface
// takes it as the start of a new turn — and one that arrived before it is owed a
// round and gets one. Steer's check and the seal are each one transaction under the
// store's write lock (Dialect.BeginWrite), so exactly one of them is first, and no
// message falls between an answer and the turn closing.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

// errAnswered refuses a message a turn can no longer take: its run has answered,
// or the turn has ended.
var errAnswered = errors.New("agents: the turn has already answered")

// Steer records one message said into a running turn, unless the turn has
// already answered. See the file comment for why the check and the append are one
// transaction.
func (s *Store) Steer(ctx context.Context, e Event) (Event, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	var sealed bool
	err = tx.QueryRowContext(ctx, `SELECT status, sealed FROM agent_sessions WHERE org=$1 AND id=$2`,
		e.Org, e.SessionID).Scan(&status, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, errSessionNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("steer: %w", err)
	}
	if sealed || isTerminalStatus(status) {
		return Event{}, errAnswered
	}
	if e, err = appendTx(ctx, tx, e); err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit: %w", err)
	}
	return e, nil
}

// Steers is what has been said into a turn since its run last looked, oldest
// first, and moves the cursor past it. seal also closes the turn to more when
// nothing is waiting: the run is about to answer.
func (s *Store) Steers(ctx context.Context, org, id string, seal bool) ([]string, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var cursor int64
	if err := tx.QueryRowContext(ctx, `SELECT steered FROM agent_sessions WHERE org=$1 AND id=$2`,
		org, id).Scan(&cursor); err != nil {
		return nil, fmt.Errorf("steers: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, payload FROM agent_session_events WHERE org=$1 AND session_id=$2 AND kind=$3 AND seq>$4
		 ORDER BY seq ASC`, org, id, KindControl, cursor)
	if err != nil {
		return nil, fmt.Errorf("steers: %w", err)
	}
	var said []string
	last := cursor
	for rows.Next() {
		var seq int64
		var payload string
		if err := rows.Scan(&seq, &payload); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("steers: %w", err)
		}
		last = seq
		var cp controlPayload
		if json.Unmarshal([]byte(payload), &cp) != nil || cp.Command != CmdMessage {
			continue
		}
		if text := strings.TrimSpace(cp.Message); text != "" {
			said = append(said, text)
		} else if len(cp.Payload) > 0 {
			said = append(said, string(cp.Payload))
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("steers: %w", err)
	}
	if last > cursor {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET steered=$1 WHERE org=$2 AND id=$3`,
			last, org, id); err != nil {
			return nil, fmt.Errorf("steers: %w", err)
		}
	}
	if seal && len(said) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET sealed=1 WHERE org=$1 AND id=$2`,
			org, id); err != nil {
			return nil, fmt.Errorf("seal: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return said, nil
}

// heard is what the person has said into this turn since the run last looked, as
// their turns of the conversation; see Steers for seal. A run with no turn, and
// an agent a turn called as a tool, hear nothing: what was said is the turn's
// own run's to read.
func (l *live) heard(ctx context.Context, seal bool) []infer.ChatMessage {
	if l == nil || agentDepth(ctx) > 0 {
		return nil
	}
	sto, err := l.store()
	if err != nil {
		return nil
	}
	said, err := sto.Steers(context.WithoutCancel(ctx), l.x.Org, l.x.ID, seal)
	if err != nil {
		l.s.Log.Warn("agents: what was said into a turn could not be read", "org", l.x.Org, "session", l.x.ID, "err", err)
		return nil
	}
	out := make([]infer.ChatMessage, 0, len(said))
	for _, text := range said {
		out = append(out, infer.ChatMessage{Role: infer.RoleUser, Content: text})
	}
	return out
}

// planeSessionControl steers one session for the person a surface names — the
// same four commands the HTTP API takes, applied the same way (apply).
//
// The org is the caller's plane identity and never an argument, and the session
// has to be the named person's own: a chat surface resolves who is speaking from
// the platform's signed identity, and this is where that answer meets the row.
// Anyone else's session answers 403, so a surface can say whose it is.
func planeSessionControl(ctx context.Context, in *client.SessionControlIn) (*client.SessionControlled, error) {
	if mounted == nil {
		return nil, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agents: steering a session needs the org on the call")
	}
	if strings.TrimSpace(in.Subject) == "" {
		return nil, zip.ErrForbidden("agents: steering a session needs the person it is for")
	}
	switch in.Command {
	case CmdStop, CmdPause, CmdResume, CmdMessage:
	default:
		return nil, zip.ErrBadRequest("command must be stop|pause|resume|message")
	}
	sto, err := mounted.State.storeFor(org)
	if err != nil {
		return nil, err
	}
	x, err := sto.GetSession(ctx, org, in.SessionID)
	if errors.Is(err, errSessionNotFound) {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if x.Actor != billingActor(org, in.Subject) {
		return nil, zip.ErrForbidden("only the person whose run this is can steer it")
	}
	r, err := apply(mounted, ctx, sto, x, x.Actor, &controlIn{ID: x.ID, Message: in.Message}, in.Command, in.Reply, in.Origin)
	if err != nil {
		return nil, err
	}
	return &client.SessionControlled{Was: x.Status, Run: r.Run}, nil
}
