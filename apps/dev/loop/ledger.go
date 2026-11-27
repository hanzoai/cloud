package loop

// ledger.go is THE LEDGER (HIP-1330 §"Actions carry identity"), one per-org
// physically-isolated SQLite file — {DataDir}/orgs/{slug}/dev.db — opened through
// cloud.NewOrgStore, so one org can never read another org's sessions or another
// org's record of what ran.
//
// Two tables, and they are the two facts this plane owns:
//
//	session  id, owner, lease, turn, held, seq, state, pending, turns, status, updated
//	action   session, thread, id, seq, kind, body, dispatched, done, result
//
// The session row holds what the session IS: `state` is the core's own snapshot,
// saved by every turn before it dispatches anything, and the journal is the
// action table. A lease is NOT one of them — it is a cell this session happens to
// hold right now, and losing it costs nothing that cannot be rebuilt. `turn` and
// `held` are the exclusion, below.
//
// AN ACTION IS NAMED BY ITS THREAD AND ITS ID. A loop driven here has one core per
// session, and its actions have the empty thread. A session the Dev frame adapter
// drives has a core per chat thread, each numbering its own actions, so the thread
// is part of the name and two threads' action 3 are two rows.
//
// `seq` IS THE JOURNAL'S OWN ORDER, not an action id. Ids are the core's and only
// unique, so the highest one says nothing about what came after it. Every journal
// row inserted, and every row answered, takes the session's next number — two
// triggers do it, in the statement that makes the change, so no write can move the
// journal without moving the number. A row carries the number of its last change,
// which is what lets a reader ask for everything after a number and miss nothing.
//
// AN ACTION ROW THAT IS DONE IS THE ANSWER, and THE ROW IS HOW THE ID IS CLAIMED.
// claim INSERTs the row ON CONFLICT DO NOTHING, so exactly one caller can ever
// mint an id and exactly that caller performs the effect; every other caller reads
// the row it lost to and answers from it. The insert is the mutual exclusion
// because it is one statement: a SELECT followed by an INSERT is two, and two
// callers who both read an unfinished row would both perform — which is the one
// thing this table exists to prevent. So a replay answers every id whose result
// was RECORDED from its row, and at-least-once delivery of an event does not mean
// at-least-once execution of an effect. An id that was dispatched and never
// answered — the process died while the cell ran it, or before the result was
// written — is refused for good: nothing here can tell whether it ran.
//
// ONE OP HOLDS A SESSION AT A TIME, and that is `turn` and `held`. A turn, a
// suspend and a resume each take the session with a compare-and-set on `turn`,
// renew `held` while they run, and give it back when they answer; one arriving
// while another holds it is refused. An action being performed renews it too, so a
// hold lasts as long as its effect. A holder that stops renewing — its process
// died — lets `held` lapse, and the next op takes the session over, so a crash
// wedges nothing and a holder that is still running is never cleared. Stop is the
// one op that takes a session from a live holder. Every write a holder makes to
// the session row names its token, and so does every claim, so what stop wrote
// stays written and a turn whose hold is gone mints nothing. An action's answer is
// written under the id its claim minted, whoever holds the session by then: the
// effect happened, and its record is not the holder's to refuse.
//
// A WRITE IS ACKNOWLEDGED ONLY ONCE IT IS SHIPPED. On a durable deployment the org
// file is hydrated from its last shipped copy when it opens, so a row that was
// committed and never shipped does not survive an unclean restart or a change of
// owner. Each op ships (dev.go, ship) before it answers success, and an action's
// claim is shipped before its effect runs.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// Action is one effect the core asked for, as the ledger records it: its id, its
// kind, and the body that decides whether a replay of the id is the same action.
type Action struct {
	ID   int64           `json:"id"`
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body,omitempty"`
	// Thread is the chat thread whose core asked, empty for the session's own core.
	Thread string `json:"thread,omitempty"`
}

// Observation is what an effect answered, under the id that asked. Out carries the
// effect's own bytes; Err is set when it never reached an exit.
type Observation struct {
	ID   int64  `json:"id"`
	Exit int    `json:"exit"`
	Out  string `json:"out,omitempty"`
	Err  string `json:"err,omitempty"`
}

// errNoSession is the store's one "no such row" sentinel; the ops map it to 404.
var errNoSession = errors.New("dev: no such session")

// errAnswered is a caller's answer the journal refuses; the op maps it to 409.
var errAnswered = errors.New("dev: the journal refuses this answer")

// holdFor is how long an op's hold on a session lasts unless it is renewed. The op
// renews it at a quarter of this while it runs, so only a holder that stopped
// running lets it lapse.
const holdFor = time.Minute

// kindAnswer is the journal's kind for an observation a caller made and answered
// from outside. It is never dispatched: a row of this kind records what the core
// was told, under the id of the action it answers.
const kindAnswer = "answer"

// store wraps one org's dev store (SQLite file or hanzo-sql schema). cloud.OrgStore
// opens and migrates it once per org and closes it on shutdown.
type Store struct {
	db *sql.DB
	d  dialect.Dialect
}

// Open is the open func cloud.OrgStore calls once per org store.
func Open(db *sql.DB) (*Store, error) {
	s := &Store{db: db, d: dialect.Of(db)}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	blob := s.d.Blob()
	bw := s.d.Bytewise()
	err := s.d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.threaded(ctx, tx); err != nil {
			return err
		}
		for _, ddl := range []string{`
CREATE TABLE IF NOT EXISTS session (
  id      TEXT PRIMARY KEY,
  owner   TEXT NOT NULL,
  lease   TEXT NOT NULL DEFAULT '',
  turn    TEXT NOT NULL DEFAULT '',
  held    BIGINT NOT NULL DEFAULT 0,
  seq     BIGINT NOT NULL DEFAULT 0,
  state   ` + blob + `,
  pending ` + blob + `,
  frames  ` + blob + `,
  turns   BIGINT NOT NULL DEFAULT 0,
  status  TEXT NOT NULL,
  updated TEXT ` + bw + ` NOT NULL
)`, `
CREATE INDEX IF NOT EXISTS session_updated ON session(updated DESC)`, `
CREATE INDEX IF NOT EXISTS session_owner ON session(owner, status)`, `
CREATE TABLE IF NOT EXISTS action (
  session    TEXT   NOT NULL,
  thread     TEXT   NOT NULL DEFAULT '',
  id         BIGINT NOT NULL,
  seq        BIGINT NOT NULL DEFAULT 0,
  kind       TEXT   NOT NULL,
  body       ` + blob + ` NOT NULL,
  dispatched TEXT   NOT NULL,
  done       TEXT   NOT NULL DEFAULT '',
  result     ` + blob + `,
  PRIMARY KEY (session, thread, id)
)`, `
CREATE INDEX IF NOT EXISTS action_seq ON action(session, seq)`, `
CREATE TABLE IF NOT EXISTS emit (
  session TEXT   NOT NULL,
  n       BIGINT NOT NULL,
  text    TEXT   NOT NULL,
  PRIMARY KEY (session, n)
)`} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		// Sequence advancement moved into Go under BeginWrite so both SQLite and
		// PostgreSQL advance seq identically and COPY during locus.Move does not
		// fire triggers. Drop the legacy SQLite triggers if an existing file has them.
		if !s.d.Postgres() {
			for _, drop := range []string{
				`DROP TRIGGER IF EXISTS journal_insert`,
				`DROP TRIGGER IF EXISTS journal_answer`,
			} {
				if _, err := tx.ExecContext(ctx, drop); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("dev migrate: %w", err)
	}
	return nil
}

// threaded moves a journal from before threads to one that names them: the action
// table is rebuilt with the thread in its key, every row it held keeping the empty
// thread, in the migration transaction. A journal that has the column, or none yet,
// is left as it is.
func (s *Store) threaded(ctx context.Context, tx *sql.Tx) error {
	hasTable, err := s.d.HasTable(ctx, tx, "action")
	if err != nil || !hasTable {
		return err
	}
	has, err := s.d.HasColumn(ctx, tx, "action", "thread")
	if err != nil || has {
		return err
	}
	blob := s.d.Blob()
	for _, q := range []string{
		`CREATE TABLE action_threaded (
		  session    TEXT   NOT NULL,
		  thread     TEXT   NOT NULL DEFAULT '',
		  id         BIGINT NOT NULL,
		  seq        BIGINT NOT NULL DEFAULT 0,
		  kind       TEXT   NOT NULL,
		  body       ` + blob + ` NOT NULL,
		  dispatched TEXT   NOT NULL,
		  done       TEXT   NOT NULL DEFAULT '',
		  result     ` + blob + `,
		  PRIMARY KEY (session, thread, id))`,
		`INSERT INTO action_threaded (session, thread, id, seq, kind, body, dispatched, done, result)
		 SELECT session, '', id, seq, kind, body, dispatched, done, result FROM action`,
		`DROP TABLE action`,
		`ALTER TABLE action_threaded RENAME TO action`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// row is one session as the store holds it. The shape a caller reads (devSession)
// is derived from it, so a column this plane keeps to itself — the core's state
// blob, and the op holding the session — has one place to stay behind in.
type row struct {
	ID      string
	Owner   string
	Pending []byte
	Frames  []byte
	Turns   int64
	Lease   string
	Turn    string
	Held    int64 // UnixNano the hold of Turn lasts until, unless it is renewed
	Seq     int64
	State   []byte
	Status  string
	Updated string
}

// lapsed reports whether an op holds the session and stopped renewing its hold.
func (r row) lapsed() bool { return r.Turn != "" && r.Held < time.Now().UnixNano() }

// The statuses a session moves through. `live` holds a lease, `idle` has none and
// can take one, `suspended` gave its cell back deliberately, `stopped` is over.
const (
	statusLive      = "live"
	statusIdle      = "idle"
	statusSuspended = "suspended"
	statusStopped   = "stopped"
)

// stamp is a time as every column here stores it: UTC, nine fractional digits.
// The width is fixed because the list sorts on the text, and RFC3339Nano trims
// trailing zeros — "…:39.9Z" sorts after "…:39.91Z" as a string.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func now() string { return stamp(time.Now()) }

// create records a new session for owner, the IAM subject it belongs to.
func (s *Store) create(r row) error {
	_, err := s.db.Exec(
		`INSERT INTO session (id, owner, lease, turn, held, seq, state, status, updated)
		 VALUES ($1, $2, '', '', 0, 0, NULL, $3, $4)`, r.ID, r.Owner, r.Status, now())
	return err
}

// get reads one session, or errNoSession.
func (s *Store) get(id string) (row, error) {
	var r row
	var state []byte
	err := s.db.QueryRow(
		`SELECT id, owner, lease, turn, held, seq, state, pending, frames, turns, status, updated FROM session WHERE id = $1`, id).
		Scan(&r.ID, &r.Owner, &r.Lease, &r.Turn, &r.Held, &r.Seq, &state, &r.Pending, &r.Frames, &r.Turns, &r.Status, &r.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, errNoSession
	}
	if err != nil {
		return row{}, err
	}
	r.State = state
	return r, nil
}

// list reads the org's sessions, most recently touched first.
func (s *Store) list(owner string) ([]row, error) {
	rs, err := s.db.Query(
		`SELECT id, owner, lease, turn, seq, status, updated FROM session WHERE owner = $1 ORDER BY updated DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.ID, &r.Owner, &r.Lease, &r.Turn, &r.Seq, &r.Status, &r.Updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// take holds the session for one op, when its status is one of `from` and nobody
// holds it — or the op that did let its hold lapse. It is a COMPARE-AND-SET on the
// row and not a lock in this process: the row is the only thing two ops arriving
// at once can both see. `was` is the holder the caller read, empty for none, so a
// take succeeds only over the holder the caller already dealt with.
func (s *Store) take(id, token, was string, from ...string) (bool, error) {
	at := time.Now()
	args := []any{token, at.Add(holdFor).UnixNano(), stamp(at), id, was, at.UnixNano()}
	for _, st := range from {
		args = append(args, st)
	}
	res, err := s.db.Exec(
		`UPDATE session SET turn = $1, held = $2, updated = $3
		 WHERE id = $4 AND turn = $5 AND (turn = '' OR held < $6)
		   AND status IN (`+dialect.Marks(7, len(from))+`)`, args...)
	return affected(res, err)
}

// renew extends the hold of the op that took the session, and reports false once
// that op no longer holds it.
func (s *Store) renew(id, token string) (bool, error) {
	res, err := s.db.Exec(`UPDATE session SET held = $1 WHERE id = $2 AND turn = $3`,
		time.Now().Add(holdFor).UnixNano(), id, token)
	return affected(res, err)
}

// leave gives the session back, and reports whether this op still held it: false
// means stop took the session while the op ran, or the op's hold lapsed and another
// took it over. The token in the WHERE is what stops a late op from clearing its
// successor's hold.
func (s *Store) leave(id, token string) (bool, error) {
	res, err := s.db.Exec(`UPDATE session SET turn = '', held = 0, updated = $1 WHERE id = $2 AND turn = $3`,
		now(), id, token)
	return affected(res, err)
}

// hold records the cell a turn leased and moves the session live — only while that
// turn still holds the session. A turn stop took the session from gets false, and
// the row stays what stop wrote.
func (s *Store) hold(id, token, lease string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE session SET lease = $1, status = $2, updated = $3
		 WHERE id = $4 AND turn = $5 AND status IN ($6, $7)`,
		lease, statusLive, now(), id, token, statusIdle, statusLive)
	return affected(res, err)
}

// save stores the core's own snapshot and the actions it has asked for that have not
// yet been answered, for the op holding the session. They are written together so
// a resume restores the one and replays the other.
func (s *Store) save(id, token string, state, pending []byte) (bool, error) {
	res, err := s.db.Exec(`UPDATE session SET state = $1, pending = $2, updated = $3 WHERE id = $4 AND turn = $5`,
		state, pending, now(), id, token)
	return affected(res, err)
}

// nextTurn takes the session's next turn number for the op holding it. The core
// refuses a turn id at or below the highest it has accepted, so ids rise for the
// life of the session, not of the process.
func (s *Store) nextTurn(id, token string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`UPDATE session SET turns = turns + 1 WHERE id = $1 AND turn = $2 RETURNING turns`, id, token).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errNoSession
	}
	return n, err
}

// settle is a holder's last write: the session moves to status, names lease, and
// is given back, in one statement.
func (s *Store) settle(id, token, status, lease string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE session SET status = $1, lease = $2, turn = '', held = 0, updated = $3 WHERE id = $4 AND turn = $5`,
		status, lease, now(), id, token)
	return affected(res, err)
}

// end stops the session whoever holds it, when its lease is still `had`, and names
// `keep` in its place: empty when the cell was ended, `had` when it could not be,
// so the next stop retries. The lease is compared rather than overwritten because a
// turn may record a cell after stop read the row. Its hold moves the lease, this
// matches nothing, and stop reads the row again — so stop never forgets a cell it
// did not end.
func (s *Store) end(id, had, keep string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE session SET status = $1, lease = $2, turn = '', held = 0, updated = $3 WHERE id = $4 AND lease = $5`,
		statusStopped, keep, now(), id, had)
	return affected(res, err)
}

// keep names a cell on the row that a turn leased and could not end — stop took
// the session while the turn ran, or the lease could not be made durable — so the
// next stop retries its end. It writes over no name but the one the turn leased
// by (`had`): the sandbox answered that lease with another cell, so `had` was
// gone. Any other name on the row is a cell a stop could not end, and it stays.
func (s *Store) keep(id, cell, had string) (bool, error) {
	return affected(s.db.Exec(`UPDATE session SET lease = $1, updated = $2 WHERE id = $3 AND lease IN ('', $4)`,
		cell, now(), id, had))
}

// affected reports whether a statement changed a row.
func affected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// claim is the deduplication, and it is the reason this table exists.
//
// THE INSERT IS THE CLAIM. One statement mints the row or loses to the row that is
// already there, so exactly one caller is ever told to perform an id and every
// other one reads what it lost to. A SELECT followed by an INSERT would be two
// statements and no exclusion at all: two callers would both read an unfinished
// row, both be told to perform, and one `git commit` would become two. The insert
// also names the turn: it mints nothing unless that turn still holds the session
// live and its hold has not lapsed, so a turn stop took the session from, or one
// whose process died, dispatches nothing after it.
//
// The states of an id, and the answers:
//
//	no row, turn holds  minted here, (Observation{}, false) — perform it
//	no row, turn lost   refused, (that, true) — do not perform it
//	another action      refused, (that, true) — do not perform it
//	dispatched          refused, (that, true) — do not perform it
//	done                answered from the row, (result, true) — do not perform it
//
// A row recording a different action is not this action's answer: answering from
// it would report the other command's exit for one that never ran. A dispatched
// row is either in flight in another caller or orphaned by a process that died
// between the dispatch and the result, and NOTHING here can tell those apart — so
// it refuses rather than guessing, because guessing wrong runs arbitrary native
// code a second time. None of the refusals is written to the journal. The op
// reports a different action and an unanswered dispatch from the rows it reads,
// and a turn that lost its session answers 409.
func (s *Store) claim(session, turn string, a Action) (Observation, bool, error) {
	body, err := json.Marshal(a)
	if err != nil {
		return Observation{}, false, err
	}
	ctx := context.Background()
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Observation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	minted, err := affected(tx.ExecContext(ctx,
		`INSERT INTO action (session, thread, id, kind, body, dispatched)
		 SELECT $1, $2, $3, $4, $5, $6 WHERE EXISTS (
		   SELECT 1 FROM session WHERE id = $7 AND turn = $8 AND status IN ($9, $10) AND held >= $11)
		 ON CONFLICT(session, thread, id) DO NOTHING`,
		session, a.Thread, a.ID, a.Kind, body, now(), session, turn, statusIdle, statusLive, time.Now().UnixNano()))
	if err != nil {
		return Observation{}, false, err
	}
	if minted {
		var seq int64
		if err := tx.QueryRowContext(ctx, `UPDATE session SET seq = seq + 1 WHERE id = $1 RETURNING seq`, session).Scan(&seq); err != nil {
			return Observation{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE action SET seq = $1 WHERE session = $2 AND thread = $3 AND id = $4`, seq, session, a.Thread, a.ID); err != nil {
			return Observation{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Observation{}, false, err
		}
		return Observation{}, false, nil
	}

	var had, result []byte
	var done string
	switch err := tx.QueryRowContext(ctx, `SELECT body, done, result FROM action WHERE session = $1 AND thread = $2 AND id = $3`,
		session, a.Thread, a.ID).Scan(&had, &done, &result); {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing was there to lose to, so the insert found the session not held by
		// this turn.
		return Observation{ID: a.ID, Err: fmt.Sprintf(
			"action %d was not dispatched: this turn no longer holds the session", a.ID)}, true, nil
	case err != nil:
		return Observation{}, false, err
	case !bytes.Equal(had, body):
		return differs(a.ID), true, nil
	case done == "":
		return unanswered(a.ID), true, nil
	}
	var obs Observation
	if err := json.Unmarshal(result, &obs); err != nil {
		return Observation{}, false, fmt.Errorf("dev: read action %d of %s: %w", a.ID, session, err)
	}
	return obs, true, nil
}

// same reports whether a journal row's body records action a.
func same(body []byte, a Action) bool {
	b, err := json.Marshal(a)
	return err == nil && bytes.Equal(body, b)
}

// differs is the answer to an id the journal holds for a different action.
func differs(id int64) Observation {
	return Observation{ID: id, Err: fmt.Sprintf(
		"action %d is recorded as a different action — it is neither answered from that row nor dispatched", id)}
}

// unanswered is the answer to an id that was dispatched and has no result.
func unanswered(id int64) Observation {
	return Observation{ID: id, Err: fmt.Sprintf(
		"action %d was already dispatched and has not answered — it is not dispatched twice", id)}
}

// answer records an observation a caller made for one of the core's actions, and
// returns the one the core is to be given, and whether this call wrote the row.
//
// The journal is the record either way. An id it has never seen is written as
// answered from outside, BEFORE the core hears of it, so the stream and a replay
// see what the core was told — and the turn takes the row back with unmint when
// the core does not take the answer. An id it has already answered is answered
// from the row, and a caller's answer that disagrees with the row is refused: the
// row is what the cell said. An id this plane dispatched and never heard back
// about is refused too — the cell may still be running it.
func (s *Store) answer(session, turn string, obs Observation) (Observation, bool, error) {
	body, err := json.Marshal(Action{ID: obs.ID, Kind: kindAnswer})
	if err != nil {
		return Observation{}, false, err
	}
	result, err := json.Marshal(obs)
	if err != nil {
		return Observation{}, false, err
	}
	at := now()
	ctx := context.Background()
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Observation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	minted, err := affected(tx.ExecContext(ctx,
		`INSERT INTO action (session, id, kind, body, dispatched, done, result)
		 SELECT $1, $2, $3, $4, $5, $6, $7 WHERE EXISTS (SELECT 1 FROM session WHERE id = $8 AND turn = $9)
		 ON CONFLICT(session, thread, id) DO NOTHING`,
		session, obs.ID, kindAnswer, body, at, at, result, session, turn))
	if err != nil {
		return Observation{}, false, err
	}
	if minted {
		var seq int64
		if err := tx.QueryRowContext(ctx, `UPDATE session SET seq = seq + 1 WHERE id = $1 RETURNING seq`, session).Scan(&seq); err != nil {
			return Observation{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE action SET seq = $1 WHERE session = $2 AND thread = '' AND id = $3`, seq, session, obs.ID); err != nil {
			return Observation{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Observation{}, false, err
		}
		return obs, true, nil
	}
	var done string
	var had []byte
	switch err := tx.QueryRowContext(ctx, `SELECT done, result FROM action WHERE session = $1 AND thread = '' AND id = $2`,
		session, obs.ID).Scan(&done, &had); {
	case errors.Is(err, sql.ErrNoRows):
		return Observation{}, false, fmt.Errorf("%w: this turn no longer holds the session", errAnswered)
	case err != nil:
		return Observation{}, false, err
	case done == "":
		return Observation{}, false, fmt.Errorf("%w: action %d was dispatched here and has not answered", errAnswered, obs.ID)
	}
	var recorded Observation
	if err := json.Unmarshal(had, &recorded); err != nil {
		return Observation{}, false, fmt.Errorf("dev: read action %d of %s: %w", obs.ID, session, err)
	}
	if recorded != obs {
		return Observation{}, false, fmt.Errorf("%w: action %d is recorded as exit %d (err %q); the answer says exit %d (err %q)",
			errAnswered, obs.ID, recorded.Exit, recorded.Err, obs.Exit, obs.Err)
	}
	return recorded, false, nil
}

// unmint takes back a row its own minter could not keep: a claim whose ship failed
// before the effect ran, or an answer the core never took. Neither records
// anything that happened, and left behind each would refuse the id for good. A row
// holding an action's answer is never removed — that is an effect's record.
func (s *Store) unmint(session, thread string, id int64) error {
	_, err := s.db.Exec(`DELETE FROM action WHERE session = $1 AND thread = $2 AND id = $3 AND (done = '' OR kind = $4)`,
		session, thread, id, kindAnswer)
	return err
}

// complete records what the cell answered, under the thread and id that asked. A
// row is answered once.
func (s *Store) complete(session, thread string, id int64, obs Observation) error {
	result, err := json.Marshal(obs)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := affected(tx.ExecContext(ctx, `UPDATE action SET done = $1, result = $2 WHERE session = $3 AND thread = $4 AND id = $5 AND done = ''`,
		now(), result, session, thread, id))
	if err != nil {
		return err
	}
	if changed {
		var seq int64
		if err := tx.QueryRowContext(ctx, `UPDATE session SET seq = seq + 1 WHERE id = $1 RETURNING seq`, session).Scan(&seq); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE action SET seq = $1 WHERE session = $2 AND thread = $3 AND id = $4`, seq, session, thread, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// journal reads what changed in a session's journal after sequence number `after`,
// in the order it changed — what the stream tails and what an audit reads. A row
// answered since it was dispatched comes back once, answered.
func (s *Store) journal(session string, after int64) ([]entry, error) {
	return s.entries(`SELECT thread, id, seq, kind, body, dispatched, done, result FROM action
		 WHERE session = $1 AND seq > $2 ORDER BY seq`, session, after)
}

// actions reads the journal's rows of the session's own core for the given ids, in
// the order they changed.
func (s *Store) actions(session string, ids []int64) ([]entry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{session}
	for _, id := range ids {
		args = append(args, id)
	}
	return s.entries(`SELECT thread, id, seq, kind, body, dispatched, done, result FROM action
		 WHERE session = $1 AND thread = '' AND id IN (`+dialect.Marks(2, len(ids))+`) ORDER BY seq`, args...)
}

func (s *Store) entries(query string, args ...any) ([]entry, error) {
	rs, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []entry
	for rs.Next() {
		var e entry
		var result []byte
		if err := rs.Scan(&e.Thread, &e.ID, &e.Seq, &e.Kind, &e.body, &e.Dispatched, &e.Done, &result); err != nil {
			return nil, err
		}
		if e.Done != "" && len(result) > 0 {
			var obs Observation
			if err := json.Unmarshal(result, &obs); err != nil {
				return nil, err
			}
			e.Answer = &obs
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

// entry is one journal line: an action, when it was dispatched, what came back if
// anything has, and the sequence number of its last change. The body stays behind
// — it is what an id is checked against, not what a watcher reads.
type entry struct {
	Thread     string       `json:"thread,omitempty"`
	ID         int64        `json:"id"`
	Seq        int64        `json:"seq"`
	Kind       string       `json:"kind"`
	Dispatched string       `json:"dispatched"`
	Done       string       `json:"done,omitempty"`
	Answer     *Observation `json:"answer,omitempty"`
	body       []byte
}

// appendEmit records a line shown to whoever is watching, so a watcher that connects
// late, or again, reads what it missed. n is the line's number in the session.
func (s *Store) appendEmit(session, text string) (int64, error) {
	ctx := context.Background()
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO emit (session, n, text)
		 SELECT $1, COALESCE(MAX(n), 0) + 1, $2 FROM emit WHERE session = $3 RETURNING n`,
		session, text, session).Scan(&n)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// Line is one thing shown to a watcher.
type Line struct {
	N    int64
	Text string
}

// emitsAfter reads the lines after n, in order.
func (s *Store) emitsAfter(session string, n int64) ([]Line, error) {
	rs, err := s.db.Query(`SELECT n, text FROM emit WHERE session = $1 AND n > $2 ORDER BY n`, session, n)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Line
	for rs.Next() {
		var l Line
		if err := rs.Scan(&l.N, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rs.Err()
}

// unlease clears the cell a suspended session named, once the cell is given back.
func (s *Store) unlease(id, had string) (bool, error) {
	return affected(s.db.Exec(`UPDATE session SET lease = '', updated = $1 WHERE id = $2 AND lease = $3 AND status = $4`,
		now(), id, had, statusSuspended))
}

// saveFrames stores the Dev frame adapter's own state — its threads and each one's
// loop snapshot — for the op holding the session. It is a different fact from the
// core's `state`: the adapter drives its loops itself, and a session is driven by one
// of the two, never both.
func (s *Store) saveFrames(id, token string, frames []byte) (bool, error) {
	res, err := s.db.Exec(`UPDATE session SET frames = $1, updated = $2 WHERE id = $3 AND turn = $4`,
		frames, now(), id, token)
	return affected(res, err)
}
