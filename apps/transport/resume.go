package transport

// resume.go picks up the rendering of turns a process that is gone was showing.
//
// live.go says it plainly: "If this process goes away mid-turn the run still
// finishes and its session records how; only the rendering is lost." That is the
// right ownership — the run is agents' — and "only the rendering" is the whole of
// what a person in the channel can see. A process that dies mid-turn leaves the
// message frozen on "Working…" or a progress line, above an answer the run went
// on to produce and nobody was left to deliver. That is not rare here: a rollout
// ends every rendering turn of the pod it replaces, on every deploy.
//
// So where a turn's message lives is written down, and another process reads it
// back. It costs the channel a pause instead of the answer, it holds for a SIGKILL
// as well as a clean exit because the row is in the database rather than in the
// process, and "one message per turn" stays true across the handover — the
// inherited turn edits the message its predecessor posted.
//
// A ROW IS A LEASE, because processes overlap. In a rolling update the new pod
// starts while the old one is still showing turns, and a pod that took every row on
// record at start would show each of them twice. A process renews its rows' renewed_at
// while it renders (keep), and takes only rows nobody has renewed for staleAfter
// (claimTurns, one statement, so two processes cannot take one row): its predecessor's
// when the predecessor is gone, and none of a live neighbour's. A process that is
// leaving gives its rows up (leave), so they are taken at the next beat and not
// after a lease has run out.
//
// There is deliberately nothing to say ABOUT the handover. The run did not stop,
// so a sentence announcing that it had would be false, and one announcing that
// the renderer blinked is noise. The channel sees the turn carry on.

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// rendering is one turn's message, as much of it as another process needs in
// order to carry on showing it.
type rendering struct {
	org, session, run, subject string
	owner                      string // the platform's id for the asker, whom the buttons name
	reply                      Message
	message                    string // the message, once posted; "" until then
	started                    int64  // Unix seconds
}

// resumeWithin is how old a rendering may be and still be picked up. A turn this
// old has long since concluded in agents, and delivering its answer now would
// put a reply under a conversation that moved on days ago — worse than the
// frozen line it replaces. Past this the row is dropped unshown.
const resumeWithin = 24 * time.Hour

// turnBeat is how often a process renews the rows of the turns it is rendering and
// looks for rows no process renews. staleAfter is how long a row may go unrenewed
// before it is taken: four beats, so a beat that is late or lost does not hand a live
// turn to another process. Values rather than constants for the reason editEvery is.
var turnBeat = 15 * time.Second

// staleAfter is in milliseconds, the grain renewed_at is kept in.
func staleAfter() int64 { return (4 * turnBeat).Milliseconds() }

// The writes below never fail a turn. Losing the row costs only the chance
// to resume, and the turn in hand is still being rendered by the process that
// holds it; failing it for that would trade a real answer for a contingency. A
// nil store is a deployment (or a test) without one, as elsewhere in this file's
// package. Each reports what it could not do, because a row that silently stopped
// being written is a resume that silently stopped working.

// openTurn records that r is being rendered, by this process.
func (st *store) openTurn(ctx context.Context, s *cloud.Service[state], r rendering) {
	if st == nil {
		return
	}
	_, err := st.db.ExecContext(ctx, `
INSERT INTO channel_turn (org, session, run, subject, owner, channel, account, room_id, room_kind, reply_to, message_id, started_at, holder, renewed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (org, session) DO UPDATE SET run = excluded.run, message_id = excluded.message_id, holder = excluded.holder, renewed_at = excluded.renewed_at`,
		r.org, r.session, r.run, r.subject, r.owner, r.reply.Transport, r.reply.Account,
		r.reply.Channel.ID, string(r.reply.Channel.Kind), r.reply.ReplyTo, r.message, r.started, st.holder, time.Now().UnixMilli())
	if err != nil {
		s.Log.Warn("transport: turn not recorded, so it cannot be resumed", "org", r.org, "session", r.session, "err", err)
	}
}

// markTurn records the message a turn posted, which is what makes it editable by
// a process that did not post it.
func (st *store) markTurn(ctx context.Context, s *cloud.Service[state], org, session, message string) {
	if st == nil {
		return
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE channel_turn SET message_id = $1 WHERE org = $2 AND session = $3 AND holder = $4`, message, org, session, st.holder); err != nil {
		s.Log.Warn("transport: turn's message not recorded, a resume would post a second one", "org", org, "session", session, "err", err)
	}
}

// closeTurn forgets a turn whose conclusion has been shown, if it is this process's
// to forget: a turn another process has since taken is that process's to conclude.
func (st *store) closeTurn(ctx context.Context, s *cloud.Service[state], org, session string) {
	if st == nil {
		return
	}
	if _, err := st.db.ExecContext(ctx,
		`DELETE FROM channel_turn WHERE org = $1 AND session = $2 AND holder = $3`, org, session, st.holder); err != nil {
		// Harmless if it is the store closing under a turn that outlived Shutdown:
		// the next process finds the row, reads a concluded session, and edits the
		// same message to the same answer.
		s.Log.Warn("transport: turn not forgotten, it will be shown again on the next start", "org", org, "session", session, "err", err)
	}
}

// turnCols are a rendering's columns, in the order scanTurns reads them.
const turnCols = "org, session, run, subject, owner, channel, account, room_id, room_kind, reply_to, message_id, started_at"

func scanTurns(rows *sql.Rows) ([]rendering, error) {
	defer rows.Close()
	var out []rendering
	for rows.Next() {
		var r rendering
		var kind string
		if err := rows.Scan(&r.org, &r.session, &r.run, &r.subject, &r.owner, &r.reply.Transport, &r.reply.Account,
			&r.reply.Channel.ID, &kind, &r.reply.ReplyTo, &r.message, &r.started); err != nil {
			return nil, err
		}
		r.reply.Channel.Kind = ChannelKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}

// claimTurns takes the turns no process has renewed since before now (milliseconds) less
// staleAfter,
// in one statement, and answers them, oldest first. A row is taken by exactly one
// caller, however many ask at once: the statement re-reads its condition once it has
// the row, and a row another caller took has been renewed.
func (st *store) claimTurns(ctx context.Context, now int64) ([]rendering, error) {
	if st == nil {
		return nil, nil
	}
	rows, err := st.db.QueryContext(ctx,
		`UPDATE channel_turn SET holder = $1, renewed_at = $2 WHERE renewed_at < $3 RETURNING `+turnCols,
		st.holder, now, now-staleAfter())
	if err != nil {
		return nil, err
	}
	out, err := scanTurns(rows)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b rendering) int {
		if a.started != b.started {
			return int(a.started - b.started)
		}
		return strings.Compare(a.session, b.session)
	})
	return out, nil
}

// leave gives up the rows this process holds, so another takes them at its next beat
// and not when their leases have run out. It is the first thing a process leaving does.
func (st *store) leave(ctx context.Context) error {
	if st == nil {
		return nil
	}
	_, err := st.db.ExecContext(ctx, `UPDATE channel_turn SET renewed_at = 0 WHERE holder = $1`, st.holder)
	return err
}

// lease is one rendering's hold on its row, kept for as long as the rendering lasts.
type lease struct {
	gone atomic.Bool // another process took the row: the turn is its to show now
	stop func()
}

// lost reports whether another process has taken the turn.
func (l *lease) lost() bool { return l != nil && l.gone.Load() }

// keep renews the row of the turn org/session every beat until the returned lease is
// stopped, and notes it if another process took the row instead. Renewing a turn that
// has no row (one that could not be recorded) is not losing it: nobody can take what
// is not on record.
func (st *store) keep(s *cloud.Service[state], org, session string) *lease {
	l := &lease{stop: func() {}}
	if st == nil {
		return l
	}
	done := make(chan struct{})
	var once sync.Once
	l.stop = func() { once.Do(func() { close(done) }) }
	go func() {
		t := time.NewTicker(turnBeat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				taken, err := st.renew(context.Background(), org, session)
				if err != nil {
					s.Log.Warn("transport: turn's lease not renewed, it may be taken", "org", org, "session", session, "err", err)
					continue
				}
				if taken {
					l.gone.Store(true)
					return
				}
			}
		}
	}()
	return l
}

// renew stamps the row of org/session as this process's and live now, and reports
// whether another process holds it instead.
func (st *store) renew(ctx context.Context, org, session string) (taken bool, err error) {
	res, err := st.db.ExecContext(ctx,
		`UPDATE channel_turn SET renewed_at = $1 WHERE org = $2 AND session = $3 AND holder = $4`,
		time.Now().UnixMilli(), org, session, st.holder)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	var holder string
	err = st.db.QueryRowContext(ctx, `SELECT holder FROM channel_turn WHERE org = $1 AND session = $2`, org, session).Scan(&holder)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return holder != st.holder, nil
}

// resume carries on every rendering no process is showing: the turns a process that
// is gone left behind, and none that a live one holds. Called at once from Use, once
// the service is published, and then every beat for as long as it is mounted, because
// a process can go at any time and the turns it held are found when its leases lapse.
//
// Each goes through spawn, the same pool a new turn takes a slot from: an
// inherited turn is a turn, and a deploy that landed on a busy hour must not be
// the one moment the cap does not apply. One that finds the pool full is not shown
// by this process, whose row it took, and its lease lapses for the next beat of any
// process rather than being dropped.
func resume(s *cloud.Service[state]) {
	ctx, done := context.WithTimeout(context.Background(), 30*time.Second)
	defer done()
	left, err := s.State.store.claimTurns(ctx, time.Now().UnixMilli())
	if err != nil {
		if mounted.Load() != s {
			// Unmounted while this was starting: Shutdown closed the store under the
			// read. "database is closed" is then a fact about the exit, not a fault
			// worth an error line on every quick start-and-stop. Only a FAILED read
			// is excused this way — a read that succeeded is acted on whoever holds
			// the service, which is what lets this be called on one directly.
			return
		}
		s.Log.Error("transport: cannot read the turns left rendering, none will be resumed", "err", err)
		return
	}
	horizon := time.Now().Add(-resumeWithin).Unix()
	for _, r := range left {
		r := r
		tr, ok := transportFor(r.reply.Transport)
		if r.started < horizon || !ok {
			// Too old to deliver, or a transport this build no longer carries: there
			// is no channel to show it in, so there is nothing to keep the row for.
			s.State.store.closeTurn(ctx, s, r.org, r.session)
			continue
		}
		spawn(s, r.org, func() {
			ctx, cancel := turnContext(r.org)
			defer cancel()
			ctx, span := turnTracer.Start(ctx, "agent.turn "+r.reply.Transport+" resumed", trace.WithSpanKind(trace.SpanKindInternal))
			defer span.End()
			span.SetAttributes(
				attribute.String("hanzo.org", r.org),
				attribute.String("hanzo.chat.provider", r.reply.Transport),
				attribute.String("hanzo.agent.run_id", r.run),
				attribute.String("hanzo.agent.session_id", r.session),
				// The trace is how anyone later tells a turn that ran straight
				// through from one that was carried across a restart.
				attribute.Bool("hanzo.chat.resumed", true),
			)
			render(ctx, s, tr, r, span)
		})
	}
	if n := len(left); n > 0 {
		s.Log.Info("transport: resumed turns no process was showing", "turns", n)
	}
}

// resuming runs resume at once and every beat until ctx ends.
func resuming(ctx context.Context, s *cloud.Service[state]) {
	resume(s)
	t := time.NewTicker(turnBeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			resume(s)
		}
	}
}
