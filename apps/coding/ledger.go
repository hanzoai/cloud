package coding

// ledger.go — the durable record of every request a run is asked for once.
//
// A person's retry reaches whichever pod took it, and a deploy forgets what a pod
// knew. A table in a map of one process therefore ran a start twice (real money) or
// said a steer twice, whenever the retry landed beside the first or after a restart.
// The answer is the database every pod already shares: a request is a row, keyed by
// who asked, what they asked and the id they gave it, and the row is the only memory.
//
//	claim    INSERT ... ON CONFLICT DO NOTHING. Of any number of pods and any number of
//	         restarts handed the same request, one inserts the row and runs the work.
//	done     the claimer stores the outcome. A duplicate answers it and runs nothing.
//	waits    a duplicate of a claimed request subscribes to the request's channel on
//	         the coord bus, looks, and waits for the message or for the bound, then
//	         looks once more. Nothing is polled: the claimer tells everyone who may be
//	         waiting the moment it has an outcome, and the bound is only how long a
//	         lost message can delay one.
//	retake   a claim whose claimer is gone is taken over by exactly one duplicate: past
//	         the bound, a duplicate asks the claimer's own process whether it is still
//	         doing the work (a process that restarted answers no, a pod that is gone
//	         answers nothing), and the one whose UPDATE of the owner matches wins.
//	fence    the claimer settles with the same match, so a claimer that was retaken
//	         learns it before it does what cannot be undone (Settle).
//
// A failure is not remembered: the claim is released and the next attempt runs. What
// is remembered is an outcome, for ttl.
//
// The store is the deployment's own "requests" (a schema on hanzo-sql, or the one file
// without it), in the system namespace like the other singletons: org is the first
// column of every key and of every statement.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// requestsSubsystem names the store in the system namespace; manifest.SQLStores lists it.
const requestsSubsystem = "requests"

const (
	stateClaimed = "claimed"
	stateDone    = "done"
)

const (
	// claimBound is how long a claim stands before a duplicate asks whether its
	// claimer is still doing the work, and how long a duplicate then waits before it
	// asks again. A start is admitted in seconds; this is the slowest it is expected
	// to be.
	claimBound = 30 * time.Second
	// claimCeiling is how long a claim is waited on whatever its claimer says: a
	// process that answers yes and does not finish is stuck, and the request must not
	// stand on it for the rest of its ttl.
	claimCeiling = 15 * time.Minute
	// sweepEvery is how often expired rows are pruned.
	sweepEvery = 10 * time.Minute
	// askWait bounds the question to a claimer, so a pod that does not answer delays
	// a judgement by this and no more.
	askWait = 3 * time.Second
	// writeWait bounds a write made after its request is gone.
	writeWait = 10 * time.Second
)

// periods are how long the ledger waits. A zero is production's: a test states its own.
type periods struct {
	Bound   time.Duration
	Ceiling time.Duration
	Expire  time.Duration
	Sweep   time.Duration
}

func (p periods) set() periods {
	if p.Bound <= 0 {
		p.Bound = claimBound
	}
	if p.Ceiling <= 0 {
		p.Ceiling = claimCeiling
	}
	if p.Expire <= 0 {
		p.Expire = idempotencyTTL
	}
	if p.Sweep <= 0 {
		p.Sweep = sweepEvery
	}
	return p
}

// errClaim is a claim that is no longer the caller's: another attempt retook it.
var errClaim = errors.New("coding: another attempt at this request now owns it")

// errGone is a row that was released between the insert that lost and the read.
var errGone = errors.New("coding: the claim was released")

// The questions and messages of the ledger on the bus.
const (
	askAlive  = "coding.alive"
	sweepLoop = "coding:requests"
)

// ledger is the requests of the deployment, as one pod holds them.
type ledger struct {
	db  *sql.DB
	d   dialect.Dialect
	p   periods
	log func(msg string, kv ...any)
	// trail is where a parked run's try is written before it is made (park.go); nil where
	// the process has none, and then no parked run goes on.
	trail Trail

	// id names a ledger that has no coord to be named by.
	id string

	mu    sync.Mutex
	bus   *coord.Coord
	leave func()
	// busy is the claims this process is executing right now, by key: what its answer
	// to "are you still on it" is read from. It holds a claim from the insert to the
	// settle and no longer; it remembers no outcome.
	busy map[string]int
	// waits is the duplicates of this process that wait on a key, to wake.
	waits map[string]map[chan struct{}]struct{}
}

// openLedger is the store's own open: its signature IS locus.Use's open func. It
// migrates the tables under the store's lock, in one transaction.
func openLedger(db *sql.DB) (*ledger, error) {
	l := &ledger{
		db: db, d: dialect.Of(db), p: periods{}.set(),
		id:    rand.Text(),
		busy:  map[string]int{},
		waits: map[string]map[chan struct{}]struct{}{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := l.d.Migrate(ctx, db, func(tx *sql.Tx) error { return l.migrate(ctx, tx) }); err != nil {
		return nil, fmt.Errorf("coding: requests: migrate: %w", err)
	}
	return l, nil
}

func (l *ledger) migrate(ctx context.Context, tx *sql.Tx) error {
	// Times are unix milliseconds. A request is one row; a steer said to a run no pod
	// held is another, ordered by seq and unique by the request id it carried.
	ddl := `
CREATE TABLE IF NOT EXISTS requests (
  org     TEXT NOT NULL,
  subject TEXT NOT NULL,
  scope   TEXT NOT NULL,
  id      TEXT NOT NULL,
  state   TEXT NOT NULL,
  owner   TEXT NOT NULL,
  session TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT '',
  print   TEXT NOT NULL DEFAULT '',
  created BIGINT NOT NULL,
  claimed BIGINT NOT NULL,
  expires BIGINT NOT NULL,
  PRIMARY KEY (org, subject, scope, id)
);
CREATE INDEX IF NOT EXISTS ix_requests_expires ON requests(expires);

CREATE TABLE IF NOT EXISTS steers (
  seq       ` + l.d.Identity() + `,
  org       TEXT NOT NULL,
  session   TEXT NOT NULL,
  subject   TEXT NOT NULL,
  request   TEXT NOT NULL,
  body      TEXT NOT NULL,
  state     TEXT NOT NULL,
  created   BIGINT NOT NULL,
  delivered BIGINT NOT NULL DEFAULT 0,
  expires   BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_steers_request ON steers(org, session, request);
CREATE INDEX IF NOT EXISTS ix_steers_session ON steers(org, session, state, seq);
CREATE INDEX IF NOT EXISTS ix_steers_expires ON steers(expires);

CREATE TABLE IF NOT EXISTS parks (
  org       TEXT NOT NULL,
  session   TEXT NOT NULL,
  subject   TEXT NOT NULL,
  reason    TEXT NOT NULL,
  resume_at BIGINT NOT NULL,
  attempt   BIGINT NOT NULL,
  spec      TEXT NOT NULL,
  created   BIGINT NOT NULL,
  tries     BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, session)
);
CREATE INDEX IF NOT EXISTS ix_parks_resume ON parks(resume_at);

CREATE TABLE IF NOT EXISTS chains (
  org     TEXT NOT NULL,
  session TEXT NOT NULL,
  since   BIGINT NOT NULL,
  PRIMARY KEY (org, session)
);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the store.
func (l *ledger) Close() error { return l.db.Close() }

func (l *ledger) now() time.Time { return time.Now() }

func (l *ledger) warn(msg string, kv ...any) {
	if l.log != nil {
		l.log(msg, kv...)
	}
}

// attach makes this ledger answer the other pods ("are you still on that request")
// and tell them what it has settled, or none: a coord that answers for this process
// alone leaves it as it was. The function it answers ends it.
func (l *ledger) attach(c *coord.Coord) func() {
	l.mu.Lock()
	leave := l.leave
	l.bus, l.leave = nil, nil
	l.mu.Unlock()
	if leave != nil {
		leave()
	}
	if c == nil {
		return func() {}
	}
	var stop func()
	if c.Shared() {
		stop = c.Serve(askAlive, l.serveAlive)
	} else {
		stop = func() {}
	}
	l.mu.Lock()
	l.bus, l.leave = c, stop
	l.mu.Unlock()
	return func() { l.attach(nil) }
}

func (l *ledger) coord() *coord.Coord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bus
}

// owner names this process among the claimers: its coord's, which carries a nonce
// no other process of the pod shares.
func (l *ledger) owner() string {
	if c := l.coord(); c != nil {
		return c.Owner()
	}
	return l.id
}

// request is one request: who asked, what, and the id they gave it. print is a hash of
// the body it was asked with, which is not part of who it is: a duplicate that carries
// another is refused.
type request struct{ org, subject, scope, id, print string }

func (k request) text() string { return k.org + "\x00" + k.subject + "\x00" + k.scope + "\x00" + k.id }

// topic is the channel a request's outcome is told on: a hash, so the channel names
// neither the tenant nor the person.
func (k request) topic() string {
	sum := sha256.Sum256([]byte(k.text()))
	return "requests/" + hex.EncodeToString(sum[:16])
}

// session is the run a session-scoped request is about, "" for a start.
func (k request) session() string {
	_, s, _ := strings.Cut(k.scope, "/")
	return s
}

// entry is a request's row, as a duplicate reads it.
type entry struct {
	state, owner, print string
	outcome             []byte
	claimed             int64
}

// claim is a request this process holds.
type claim struct {
	l       *ledger
	k       request
	owner   string
	at      int64
	settled bool
}

// ctxClaim keys the claim a start runs under.
type ctxClaim struct{}

// withClaim carries the claim a start was made under to the point the run is committed.
func withClaim(ctx context.Context, c *claim) context.Context {
	return context.WithValue(ctx, ctxClaim{}, c)
}

// claimOf is the claim the request in ctx runs under, nil for a run nobody asked twice.
func claimOf(ctx context.Context) *claim {
	c, _ := ctx.Value(ctxClaim{}).(*claim)
	return c
}

// Settle records out as the request's outcome, if the claim is still this one's, and
// otherwise answers errClaim. A start settles BEFORE it spawns the run: that is the
// fence, since a claimer that was retaken (it was slow, not dead) finds out here and
// spawns nothing, so the request is never two runs.
func (c *claim) Settle(ctx context.Context, out any) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return c.settle(ctx, raw, sessionOf(c.k, out))
}

// Fence answers errClaim unless the claim is still this one's. An op whose effect cannot
// be undone asks it just before making it, so a claimer that was retaken (it was slow, not
// dead) makes nothing the retaker will make again.
func (c *claim) Fence(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	var n int
	if err := c.l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests
WHERE org = $1 AND subject = $2 AND scope = $3 AND id = $4 AND state = 'claimed' AND owner = $5 AND claimed = $6`,
		c.k.org, c.k.subject, c.k.scope, c.k.id, c.owner, c.at).Scan(&n); err != nil {
		return fmt.Errorf("coding: could not check a request's claim: %w", err)
	}
	if n != 1 {
		return errClaim
	}
	return nil
}

func (c *claim) settle(ctx context.Context, raw []byte, session string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	res, err := c.l.db.ExecContext(ctx, `UPDATE requests SET state = 'done', outcome = $1, session = $2
WHERE org = $3 AND subject = $4 AND scope = $5 AND id = $6 AND state = 'claimed' AND owner = $7 AND claimed = $8`,
		string(raw), session, c.k.org, c.k.subject, c.k.scope, c.k.id, c.owner, c.at)
	if err != nil {
		return fmt.Errorf("coding: could not record the outcome of a request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errClaim
	}
	c.settled = true
	return nil
}

// sessionOf is the run a request is about: the one its scope names, else the one its
// outcome does.
func sessionOf(k request, out any) string {
	if s := k.session(); s != "" {
		return s
	}
	if a, ok := out.(Accepted); ok {
		return a.SessionID
	}
	return ""
}

// take claims the request, or reads who has it. An expired row is taken over as an
// absent one is, whether or not the sweep has pruned it yet.
func (l *ledger) take(ctx context.Context, k request) (mine bool, at int64, e entry, err error) {
	now := l.now()
	at = now.UnixMilli()
	res, err := l.db.ExecContext(ctx, `INSERT INTO requests (org, subject, scope, id, state, owner, session, outcome, created, claimed, expires, print)
VALUES ($1, $2, $3, $4, 'claimed', $5, '', '', $6, $6, $7, $8)
ON CONFLICT (org, subject, scope, id) DO UPDATE SET state = 'claimed', owner = $5, session = '', outcome = '', created = $6, claimed = $6, expires = $7, print = $8
WHERE requests.expires <= $6`,
		k.org, k.subject, k.scope, k.id, l.owner(), at, now.Add(l.p.Expire).UnixMilli(), k.print)
	if err != nil {
		return false, 0, entry{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, at, entry{}, nil
	}
	e, err = l.read(ctx, k)
	return false, 0, e, err
}

func (l *ledger) read(ctx context.Context, k request) (entry, error) {
	var e entry
	var outcome string
	err := l.db.QueryRowContext(ctx, `SELECT state, owner, outcome, claimed, print FROM requests
WHERE org = $1 AND subject = $2 AND scope = $3 AND id = $4`, k.org, k.subject, k.scope, k.id).Scan(&e.state, &e.owner, &outcome, &e.claimed, &e.print)
	if errors.Is(err, sql.ErrNoRows) {
		return entry{}, errGone
	}
	e.outcome = []byte(outcome)
	return e, err
}

// retake moves a claim whose claimer is gone to this process. Of the duplicates that
// try, the one whose owner and claim time still match the row it read wins.
func (l *ledger) retake(ctx context.Context, k request, e entry) (bool, int64, error) {
	at := l.now().UnixMilli()
	res, err := l.db.ExecContext(ctx, `UPDATE requests SET owner = $1, claimed = $2
WHERE org = $3 AND subject = $4 AND scope = $5 AND id = $6 AND state = 'claimed' AND owner = $7 AND claimed = $8`,
		l.owner(), at, k.org, k.subject, k.scope, k.id, e.owner, e.claimed)
	if err != nil {
		return false, 0, err
	}
	n, _ := res.RowsAffected()
	return n == 1, at, nil
}

// release gives a claim up without an outcome, so the next attempt runs.
func (l *ledger) release(ctx context.Context, c *claim) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	if _, err := l.db.ExecContext(ctx, `DELETE FROM requests
WHERE org = $1 AND subject = $2 AND scope = $3 AND id = $4 AND state = 'claimed' AND owner = $5 AND claimed = $6`,
		c.k.org, c.k.subject, c.k.scope, c.k.id, c.owner, c.at); err != nil {
		l.warn("coding: a failed request's claim was not released; it is retaken once its claimer is gone", "err", err)
	}
}

// prune deletes what has expired: the requests, the steers said so long ago that nothing can
// still be waiting on them, and the chains of parks that have run their course (a chain is
// kept twice as long as a run goes on by itself, for the run that is flying).
func (l *ledger) prune(ctx context.Context) (int64, error) {
	now := l.now().UnixMilli()
	var total int64
	for _, q := range []string{`DELETE FROM requests WHERE expires <= $1`, `DELETE FROM steers WHERE expires <= $1`} {
		res, err := l.db.ExecContext(ctx, q, now)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	// The chain of a session that has no park and began so long ago that no run of it can still be
	// flying is spent: a park of it now would begin another (park.go). A run that is flying has no
	// park, and the sessions store is not this one's to ask, so the bound is the run's own: a run
	// is gone on with at most MaxLife into its chain, and a session lives at most MaxLife from its
	// unpark (the reaper), so past twice MaxLife none of the chain can be flying.
	res, err := l.db.ExecContext(ctx, `DELETE FROM chains WHERE since <= $1
 AND NOT EXISTS (SELECT 1 FROM parks WHERE parks.org = chains.org AND parks.session = chains.session)`, l.now().Add(-2*agents.MaxLife()).UnixMilli())
	if err != nil {
		return total, err
	}
	n, _ := res.RowsAffected()
	return total + n, nil
}

// sweep prunes, on the pod that leads the loop. Another pod answers coord.ErrNotHeld.
func (l *ledger) sweep(ctx context.Context) error {
	var err error
	lead := l.coord().Lead(sweepLoop)
	if derr := lead.Do(ctx, func(ctx context.Context) { _, err = l.prune(ctx) }); derr != nil {
		return derr
	}
	return err
}

// ---- the bus --------------------------------------------------------------

// listen subscribes to the request's channel, on this pod and the others, and answers
// a channel that is signalled when its outcome is told and the function that ends it.
// A message missed (the bus said so) signals too: the waiter looks at the row.
func (l *ledger) listen(k request) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	topic := k.topic()
	l.mu.Lock()
	if l.waits[topic] == nil {
		l.waits[topic] = map[chan struct{}]struct{}{}
	}
	l.waits[topic][ch] = struct{}{}
	c := l.bus
	l.mu.Unlock()
	poke := func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	var off func()
	if c != nil {
		off = c.Subscribe(topic, func(string, []byte) { poke() }, poke)
	}
	return ch, func() {
		if off != nil {
			off()
		}
		l.mu.Lock()
		delete(l.waits[topic], ch)
		if len(l.waits[topic]) == 0 {
			delete(l.waits, topic)
		}
		l.mu.Unlock()
	}
}

// tell wakes every duplicate waiting on k, on this pod and the others. The bus does not
// deliver a pod's own message to it, so this pod's waiters are woken directly.
func (l *ledger) tell(k request) {
	topic := k.topic()
	l.mu.Lock()
	for ch := range l.waits[topic] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	c := l.bus
	l.mu.Unlock()
	if c != nil {
		if err := c.Publish(topic, nil); err != nil {
			l.warn("coding: could not tell the other pods a request was settled; a duplicate waiting there looks again at the bound", "err", err)
		}
	}
}

// serveAlive says whether this process is executing the request in body.
func (l *ledger) serveAlive(_ context.Context, _ string, body []byte) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return nil, l.busy[string(body)] > 0
}

func (l *ledger) executing(k request) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.busy[k.text()] > 0
}

func (l *ledger) hold(k request, by int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.busy[k.text()] += by
	if l.busy[k.text()] <= 0 {
		delete(l.busy, k.text())
	}
}

// abandoned judges a claim that has stood past the bound: its claimer is gone when its
// own process says it is not on the request, or when the process it was is not there to
// ask. A claimer that cannot be asked is not judged gone before the ceiling.
func (l *ledger) abandoned(ctx context.Context, k request, e entry) bool {
	if e.owner == l.owner() {
		return !l.executing(k)
	}
	c := l.coord()
	if c == nil || !c.Shared() {
		return true // a process of an earlier life; this deployment has no other
	}
	ctx, cancel := context.WithTimeout(ctx, askWait)
	defer cancel()
	_, yes, err := c.CallPod(ctx, e.owner, askAlive, []byte(k.text()))
	if err != nil {
		return false
	}
	return !yes
}

// ---- the one entry --------------------------------------------------------

// once runs fn for the request k at most once across every pod and restart. A
// duplicate answers what the first stored; one whose body is not the first's is refused
// 422, the id having been used for another request. ran says whether fn ran here.
//
// A duplicate of a claimed request waits on the bus, not on a timer: it listens, looks
// again, and sleeps until it is told. It looks at the claimer twice at most, and only
// because a claimer can die without telling anyone: once when the claim has stood the
// bound, and once at the ceiling. Told, it looks again.
func (l *ledger) once(ctx context.Context, k request, fn func(*claim) ([]byte, error)) (raw []byte, ran bool, err error) {
	if err := dialect.Text(k.org, k.subject, k.scope, k.id); err != nil {
		return nil, false, zip.ErrBadRequest("coding: requestId must be text")
	}
	var (
		wake   <-chan struct{}
		stop   func()
		judged int64 // the claim, by its time, whose claimer has been asked after
	)
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	for {
		mine, at, e, err := l.take(ctx, k)
		switch {
		case errors.Is(err, errGone):
			continue
		case err != nil:
			return nil, false, unavailable(err)
		case mine:
			raw, err := l.run(ctx, &claim{l: l, k: k, owner: l.owner(), at: at}, fn)
			return raw, true, err
		case !samePrint(e.print, k.print):
			return nil, false, zip.Errorf(http.StatusUnprocessableEntity,
				"coding: the request id %q was already used for a different request; send a new requestId (or Idempotency-Key) for a new request", k.id)
		case e.state == stateDone:
			return e.outcome, false, nil
		}
		// Somebody else's claim. Listen first and look again, so an outcome told in
		// between is not missed, then wait for it.
		if wake == nil {
			wake, stop = l.listen(k)
			continue
		}
		now := l.now()
		claimed := time.UnixMilli(e.claimed)
		age := now.Sub(claimed)
		gone := age >= l.p.Ceiling
		if !gone && age >= l.p.Bound && judged != e.claimed {
			judged = e.claimed
			gone = l.abandoned(ctx, k, e)
		}
		if gone {
			won, at, err := l.retake(ctx, k, e)
			switch {
			case err != nil:
				return nil, false, unavailable(err)
			case won:
				raw, err := l.run(ctx, &claim{l: l, k: k, owner: l.owner(), at: at}, fn)
				return raw, true, err
			}
			continue // another duplicate won it: wait for that one
		}
		due := claimed.Add(l.p.Bound)
		if judged == e.claimed {
			due = claimed.Add(l.p.Ceiling)
		}
		t := time.NewTimer(due.Sub(now))
		select {
		case <-wake:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, false, ctx.Err()
		}
		t.Stop()
	}
}

// run executes fn under a claim this process holds, and settles it.
func (l *ledger) run(ctx context.Context, c *claim, fn func(*claim) ([]byte, error)) (raw []byte, err error) {
	l.hold(c.k, 1)
	defer l.hold(c.k, -1)
	defer func() {
		if r := recover(); r != nil {
			l.release(ctx, c)
			l.tell(c.k)
			panic(r)
		}
	}()
	raw, err = fn(c)
	if err != nil {
		l.release(ctx, c)
		l.tell(c.k)
		return nil, err
	}
	if !c.settled {
		if err := c.settle(ctx, raw, sessionOf(c.k, nil)); err != nil {
			// The work is done and is answered. What could not be recorded is a duplicate
			// that will run it again, once this claim is judged abandoned.
			l.warn("coding: a request's outcome was not recorded", "scope", c.k.scope, "err", err)
		}
	}
	l.tell(c.k)
	return raw, nil
}

// unavailable is a ledger that cannot be read or written: a request that may not be
// the first is not run on a guess.
func unavailable(err error) error {
	return zip.Errorf(http.StatusServiceUnavailable, "coding: the request ledger is unavailable; try again: %v", err)
}

// once runs fn for the request at most once across every pod and restart, and answers
// every duplicate what the first answered. A failure is not remembered.
func once[T any](ctx context.Context, l *ledger, k request, fn func(*claim) (T, error)) (T, error) {
	var out, zero T
	raw, ran, err := l.once(ctx, k, func(c *claim) ([]byte, error) {
		v, err := fn(c)
		if err != nil {
			return nil, err
		}
		out = v
		return json.Marshal(v)
	})
	switch {
	case err != nil:
		return zero, err
	case ran:
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, zip.Errorf(http.StatusInternalServerError, "coding: a stored outcome is unreadable: %v", err)
	}
	return out, nil
}

// ---- the process's ledger -------------------------------------------------

// Keep opens the request ledger, makes this process answer the other pods about the
// requests it is executing, and prunes what has expired on the pod that leads the
// sweep. It is the composition root's, once, beside Serve; the function it answers
// ends it. Until it has run, a request that carries an id is refused.
func Keep(c *coord.Coord, log func(msg string, kv ...any), trail Trail) (func(), error) {
	l, err := locus.Use(context.Background(), nil, namespace.System(), requestsSubsystem, cloud.DataDir(), openLedger)
	if err != nil {
		return nil, fmt.Errorf("coding: open the request ledger: %w", err)
	}
	l.log = log
	l.trail = trail
	l.attach(c)
	flights.keep(l)
	// The runs parked for a limit are woken, and tried when funds change (park.go).
	unwatch := flights.watch(l.coord())
	ctx, halt := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(l.p.Sweep)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := l.sweep(ctx); err != nil && !errors.Is(err, coord.ErrNotHeld) && ctx.Err() == nil {
					l.warn("coding: could not prune expired requests", "err", err)
				}
			}
		}
	}()
	return func() {
		halt()
		<-done
		unwatch()
		flights.keep(nil)
		l.attach(nil)
		_ = l.Close()
	}, nil
}
