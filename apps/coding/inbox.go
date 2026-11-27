package coding

// inbox.go — what is said to a run, in the order it was said.
//
// A steer lands on a pod, which asks the others who flies the run (fleet.go). Between
// a rolling restart's two halves, or while a lost run is being resumed, nobody does,
// and the person's words used to come back as "nothing of yours is running there" or
// not at all. A run in that state is still running as far as its session says, and
// will have an owner as soon as one resumes it. So the words are kept: appended to
// the run's inbox in the ledger's database, ordered, and taken by the owner.
//
// One way in. Where a ledger is kept, EVERY instruction for a run is a row, whether
// the run is flown or not: the row is the durable record that it was said, unique by
// the request id it carried, and the flight hands rows to the run by seq. So order is
// the inbox's, a direct steer cannot overtake a backlog, and a steer replayed after
// its claimer died finds its row and hands nothing a second time.
//
//	said     a row, queued. For a running session no pod flies it is appended where
//	         it is asked; a run that has ended refuses it with the reason; a person who
//	         is not the run's is told what they are told of a run that is not there.
//	handed   the owner reads its inbox before the run begins, when what was said joins
//	         the run's task (startSession, after the session has recorded it), and
//	         whenever it is told there is more (give). Told means pushed: the pod that
//	         wrote the row tells its own flight directly and every other pod on the bus.
//	marked   a row is delivered once the run has it: the turn took it, or the follow-up
//	         that carries it has started, or the session recorded it for a resume. A
//	         mark that fails leaves the row queued, and the flight remembers by seq what
//	         it already handed its run, so it is not handed twice.
//	order    rows are read by seq. The turn takes them one at a time; the first it
//	         cannot take stops the run, and everything still queued goes to its
//	         follow-up together, in order, and is marked when that run exists.
//
// What an old pod does with a row (a rolling update runs two versions at once): it does
// not know the tables. It writes none and reads none, and its flights hand nothing from
// the inbox. A row for a run that an old pod resumes stays queued, untouched, and shows
// as queued on the run's changes; the first pod of this version to fly that run, or to
// follow it up, hands it over. A queued instruction is never deleted by anything but
// its expiry, so an old pod cannot strand it into a loss, only into a delay.
//
// A row outlives the run only as long as a request does (ttl), and shows on the run's
// changes as queued until it is taken.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

const (
	steerQueued    = "queued"
	steerDelivered = "delivered"
	// steerCarrying is a row a start after the run has claimed for its new run's task:
	// not delivered until that run is spawned, and queued again if it never is (delivered
	// holds when it was claimed, and the claim is abandoned after claimCeiling). A start
	// that has a request id writes it into the state (heldBy), so the same request,
	// retried, takes its claim back at once and any other waits for the ceiling.
	steerCarrying = "carrying"
	// steerDropped is a row no task could ever hold, which a start after the run found
	// and set aside, saying so.
	steerDropped = "dropped"
)

// heldBy is the state of a row claimed by the start that has this request id: carrying,
// and whose. A start with no request id claims plainly, and nobody takes that back early.
func heldBy(request string) string {
	if request == "" {
		return steerCarrying
	}
	sum := sha256.Sum256([]byte(request))
	return steerCarrying + "/" + hex.EncodeToString(sum[:8])
}

// claimed says whether a state is a row held by a start after its run.
func claimed(state string) bool {
	return state == steerCarrying || strings.HasPrefix(state, steerCarrying+"/")
}

// steer is one instruction in a run's inbox. Times are unix milliseconds.
type steer struct {
	seq                    int64
	subject, request, body string
	state                  string
	said, delivered        int64
}

// minted reports an id this process made for an instruction that came with none.
func (s steer) minted() bool { return strings.HasPrefix(s.request, "steer_") }

// enqueue appends an instruction to the run's inbox. The same request id says it
// once. An instruction is as long as a task prompt may be, and no longer.
func (l *ledger) enqueue(ctx context.Context, org, session, subject, request, body string) error {
	if err := dialect.Text(org, session, subject, request, body); err != nil {
		return zip.ErrBadRequest("coding: an instruction must be text")
	}
	if len(body) > maxPromptLen {
		return zip.ErrBadRequest(fmt.Sprintf("coding: an instruction is at most %d bytes", maxPromptLen))
	}
	now := l.now()
	_, err := l.db.ExecContext(ctx, `INSERT INTO steers (org, session, subject, request, body, state, created, delivered, expires)
VALUES ($1, $2, $3, $4, $5, 'queued', $6, 0, $7)
ON CONFLICT (org, session, request) DO NOTHING`,
		org, session, subject, request, body, now.UnixMilli(), now.Add(l.p.Expire).UnixMilli())
	return err
}

// queued is the instructions waiting for the run, oldest first.
func (l *ledger) queued(ctx context.Context, org, session string) ([]steer, error) {
	return l.steers(ctx, org, session, `AND state = 'queued'`)
}

// inbox is every instruction said to the run, oldest first.
func (l *ledger) inbox(ctx context.Context, org, session string) ([]steer, error) {
	return l.steers(ctx, org, session, "")
}

func (l *ledger) steers(ctx context.Context, org, session, more string, args ...any) ([]steer, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT seq, subject, request, body, state, created, delivered FROM steers
WHERE org = $1 AND session = $2 `+more+` ORDER BY seq`, append([]any{org, session}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []steer
	for rows.Next() {
		var s steer
		if err := rows.Scan(&s.seq, &s.subject, &s.request, &s.body, &s.state, &s.said, &s.delivered); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// waiting says whether the instruction the request id names is still queued.
func (l *ledger) waiting(ctx context.Context, org, session, request string) (bool, error) {
	var state string
	err := l.db.QueryRowContext(ctx, `SELECT state FROM steers WHERE org = $1 AND session = $2 AND request = $3`,
		org, session, request).Scan(&state)
	if err != nil {
		return false, err
	}
	return state == steerQueued, nil
}

// receive marks an instruction delivered, and says whether this call was the one that
// did: of two readers, one.
func (l *ledger) receive(ctx context.Context, s steer) (bool, error) {
	res, err := l.db.ExecContext(ctx, `UPDATE steers SET state = 'delivered', delivered = $1 WHERE seq = $2 AND state = 'queued'`,
		l.now().UnixMilli(), s.seq)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// unfinished is what a start after the run can claim: the rows still queued; the ones a
// start claimed and never finished, which are queued again once the claim is older than
// claimCeiling; and the ones the request with this id claimed, which are its own to take
// back (it is the same request, retried, and only one attempt at it runs). "" holds none.
func (l *ledger) unfinished(ctx context.Context, org, session, request string) ([]steer, error) {
	cutoff := l.now().Add(-l.p.Ceiling).UnixMilli()
	own := ""
	if request != "" {
		own = heldBy(request)
	}
	return l.steers(ctx, org, session, `AND (state = 'queued' OR (state LIKE 'carrying%' AND delivered <= $3) OR state = $4)`, cutoff, own)
}

// move changes a row's state from the one the caller read it in, and says whether this
// call was the one that did: of two starts after one run, one. at is when, and a row
// read as carrying is moved only if it is still the claim that was read.
func (l *ledger) move(ctx context.Context, s steer, to string, at int64) (bool, error) {
	res, err := l.db.ExecContext(ctx, `UPDATE steers SET state = $1, delivered = $2 WHERE seq = $3 AND state = $4 AND delivered = $5`,
		to, at, s.seq, s.state, s.delivered)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ---- saying it ------------------------------------------------------------

// inboxTopic is the channel an inbox is told on: a hash, so it names neither the
// tenant nor the run.
func inboxTopic(org, session string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + session))
	return "coding/inbox/" + hex.EncodeToString(sum[:16])
}

// ident is the id an instruction is kept under: the one it came with, else one made
// here, so every row is unique and a caller that sent none is not deduplicated.
func ident(raw string) (string, error) {
	id, err := requestID(raw)
	if err != nil {
		return "", err
	}
	if id == "" {
		id = "steer_" + rand.Text()
	}
	return id, nil
}

// room refuses an instruction that cannot reach a run with no live turn: one that does not
// fit, beside everything already waiting for the run, in the one task a follow-up or a
// resume can be given (base is the task it would be given, "" when it is not known). A run
// with a live turn takes an instruction of any size, so it is not asked. The refusal names
// the prompt limit, the one an oversized instruction is refused at. The instruction itself,
// if it was queued already (a retry), is not counted twice.
func (l *ledger) room(ctx context.Context, org, session, request, base, body string) error {
	waiting, err := l.queued(ctx, org, session)
	if err != nil {
		return unavailable(err)
	}
	rows := make([]steer, 0, len(waiting)+1)
	for _, s := range waiting {
		if s.request != request {
			rows = append(rows, s)
		}
	}
	rows = append(rows, steer{body: body})
	took, _ := fit(base, rows)
	if len(took) > 0 && took[len(took)-1].seq == 0 {
		return nil
	}
	if len(waiting) == 0 {
		return zip.ErrBadRequest(fmt.Sprintf("coding: this instruction is more than a follow-up's task holds beside the run's own (%d bytes in all), and that run "+
			"cannot take it while it works; stop the run or wait for it to end, then start a run with `after` set to its session and say it in the prompt", maxPromptLen))
	}
	return zip.ErrBadRequest(fmt.Sprintf("coding: this instruction, with the %d waiting for that run, is more than the task they are carried in holds (%d bytes), "+
		"and that run cannot take it while it works; wait for the run to take what is queued, then say it again", len(waiting), maxPromptLen))
}

// queue keeps an instruction for a run no pod flies: the session says it is running,
// so an owner is on its way. A run that has ended, or is paused with no pod to
// continue it, is refused 409 with the reason (it was 404, "nothing of yours is running
// there", before; no client tells the two apart), and one that is not the person's, or
// is not a coding run, is not there.
func (fl *fleet) queue(ctx context.Context, org, subject string, in client.CodingSteerIn) error {
	gone := zip.ErrNotFound("coding: nothing of yours is running there")
	session, text := strings.TrimSpace(in.SessionID), strings.TrimSpace(in.Text)
	l := fl.ledger()
	if l == nil || session == "" || text == "" {
		return gone
	}
	// The run is read as its person: only they may steer it, and a session they cannot
	// read is answered as one that is not there.
	as := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: subject})
	x, err := visibleRun(as, session)
	var he *zip.HTTPError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		return gone
	case err != nil:
		return zip.Errorf(http.StatusServiceUnavailable, "coding: could not read that run to keep your instruction for it: %v; try again", err)
	case x.Kind != kindCoding:
		return gone
	case x.Target != "" || x.Box == "":
		// A run on a machine of the person's own is never flown or resumed by a pod, and
		// a run with no sandbox cannot be resumed (resumeCoding): an instruction kept for
		// either would be acked and never taken. They answer as they did before.
		return gone
	}
	switch x.Status {
	case agents.StatusRunning:
	case agents.StatusPaused:
		return zip.Errorf(http.StatusConflict, "coding: that run is paused and no pod holds it; start a run with `after` set to this session to carry it on")
	default:
		return zip.Errorf(http.StatusConflict, "coding: that run has %s; start a run with `after` set to this session to carry it on", x.Status)
	}
	rid, err := ident(in.RequestID)
	if err != nil {
		return err
	}
	// Nothing flies the run, so what is known of its harness is not: it is asked as one
	// with no live turn, whose task is the least a resume is given.
	if err := l.room(ctx, org, session, rid, "", text); err != nil {
		return err
	}
	if err := l.enqueue(ctx, org, session, subject, rid, text); err != nil {
		if errors.As(err, &he) {
			return err
		}
		return unavailable(err)
	}
	fl.signal(org, session)
	return nil
}

// signal tells the owner of the run there is more in its inbox: this pod's flight
// directly, since the bus does not deliver a pod its own messages, and the other
// pods on the bus.
func (fl *fleet) signal(org, session string) {
	fl.Lock()
	f, bus := fl.m[session], fl.bus
	fl.Unlock()
	if f != nil && f.org == org {
		go f.drain()
	}
	if bus != nil {
		if err := bus.Publish(inboxTopic(org, session), nil); err != nil {
			fl.say("coding: could not tell the other pods there is an instruction for a run; its owner reads it when it next looks", "session", session, "err", err)
		}
	}
}

// say logs on this pod's log.
func (fl *fleet) say(msg string, kv ...any) {
	_, log := fl.coord()
	if log != nil {
		log(msg, kv...)
	}
}

// ---- handing it ------------------------------------------------------------

// listen makes the flight take what is said to its run: told on the bus, it gives, and
// it gives once now for what was said before it flew. It listens BEFORE it looks, so an
// instruction written between the two is not missed.
func (f *flight) listen() {
	fl := f.fleet
	if fl.ledger() == nil {
		return
	}
	if bus, _ := fl.coord(); bus != nil {
		off := bus.Subscribe(inboxTopic(f.org, f.session), func(string, []byte) { go f.drain() }, func() { go f.drain() })
		f.mu.Lock()
		f.unlisten = off
		f.mu.Unlock()
	}
	go f.drain()
}

// drain hands the run what was said to it, oldest first, once each.
func (f *flight) drain() {
	f.draining.Lock()
	defer f.draining.Unlock()
	f.give()
}

// tryDrain is drain for a caller that would rather not wait for one in progress: it
// answers false and gives nothing when another is running, whose read will see what the
// caller wrote.
func (f *flight) tryDrain() bool {
	if !f.draining.TryLock() {
		return false
	}
	defer f.draining.Unlock()
	f.give()
	return true
}

// following says whether a follow-up has taken the run, which carries what is queued.
func (f *flight) following() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimed
}

// give is the read of the inbox, under the drain's lock. The turn takes the
// instructions one at a time; the first it cannot take stops the run, and the
// follow-up carries every one still queued, in order, marking them when it has started
// (follow).
func (f *flight) give() {
	fl := f.fleet
	if fl == nil {
		return
	}
	l := fl.ledger()
	if l == nil {
		return
	}
	defer f.drains.Add(1)
	fl.Lock()
	here := fl.m[f.session] == f
	fl.Unlock()
	if !here {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	rows, err := l.queued(ctx, f.org, f.session)
	if err != nil {
		fl.say("coding: could not read the instructions waiting for a run", "org", f.org, "session", f.session, "err", err)
		return
	}
	_, log := fl.coord()
	for i, s := range rows {
		if !f.handed[s.seq] {
			if !f.turn(s.body, s.request, log) {
				f.redirected(rows[i:], log)
				return
			}
			if f.handed == nil {
				f.handed = map[int64]bool{}
			}
			f.handed[s.seq] = true
		}
		// The run has it. A mark that fails leaves the row queued and the run's own
		// memory of having been handed it: the next read marks it and hands nothing.
		if _, err := l.receive(ctx, s); err != nil {
			fl.say("coding: could not mark an instruction delivered", "session", f.session, "seq", s.seq, "err", err)
		}
	}
}

// redirected stops the run for what its turn would not take. The rows stay queued: the
// follow-up is started with `after` this session and carries them (startSession), marking
// them once it has them. A run already being followed up carries them. A run is not
// stopped for an instruction no follow-up could carry whole, which fits no task beside
// this one's: it stays queued for a live turn, which takes an instruction of any size
// (setSteerer tells the flight when one begins).
func (f *flight) redirected(rows []steer, log func(msg string, kv ...any)) {
	if len(rows) == 0 || f.following() {
		return
	}
	if !f.carryable(rows) {
		f.fleet.say("coding: an instruction waiting for a run fits no follow-up's task; it stays queued for the run's next turn", "session", f.session)
		return
	}
	in := client.CodingSteerIn{SessionID: f.session, Subject: f.subject}
	if err := f.redirect(f.subject, in, log); err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusConflict {
			return
		}
		f.fleet.say("coding: a run could not be redirected for the instructions waiting for it; they stay queued", "session", f.session, "err", err)
	}
}

// base is the task a follow-up of this run would be given before anything is carried: the
// longer of the two ways it is worded.
func (f *flight) base() string {
	a, b := followUp(f.in.Prompt, "", true, false), followUp(f.in.Prompt, "", false, true)
	if len(a) > len(b) {
		return a
	}
	return b
}

// live says whether the run's harness takes an instruction while it works, as dev and
// gen do (sandboxrunner.go onTurnSteer): the others are told things by stopping them.
func (f *flight) live() bool { return runsDev(f.in.Tool) }

// carryable says whether a follow-up of this run could carry its first waiting row whole.
func (f *flight) carryable(rows []steer) bool {
	took, _ := fit(f.base(), rows[:1])
	return len(took) > 0
}

// settle marks delivered what the run's turn was handed and its mark missed, before a
// follow-up carries what is still queued: the follow-up must not repeat it. It is read
// under the drain's lock, so a read in progress has marked what it handed over first.
func (f *flight) settle() {
	fl := f.fleet
	if fl == nil || fl.ledger() == nil {
		return
	}
	l := fl.ledger()
	f.draining.Lock()
	defer f.draining.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	rows, err := l.queued(ctx, f.org, f.session)
	if err != nil {
		fl.say("coding: could not read the instructions waiting for a run", "org", f.org, "session", f.session, "err", err)
		return
	}
	for _, s := range rows {
		if f.handed[s.seq] {
			if _, err := l.receive(ctx, s); err != nil {
				fl.say("coding: could not mark an instruction delivered", "session", f.session, "seq", s.seq, "err", err)
			}
		}
	}
}

// ---- a resumed run --------------------------------------------------------

// backlog is what was said to a run being resumed while no pod held it: the caller joins
// it to the task the run is resumed with. Each instruction is first recorded on the
// run's session as a message the run was owed (resumeCoding folds those back into the
// task, so a second resume of the same run has them), and only then marked delivered. One
// that the session could not record stays queued, and with every one after it: the
// flight hands them over (give). A run that is not resumed has nothing said to it that
// nobody has read.
func backlog(ctx context.Context, existing *agents.Session, org, session, subject, task string, log func(msg string, kv ...any)) []steer {
	l := flights.ledger()
	if l == nil || existing == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*writeWait)
	defer cancel()
	rows, err := l.queued(ctx, org, session)
	if err != nil {
		if log != nil {
			log("coding: could not read the instructions waiting for a resumed run; its flight reads them", "org", org, "session", session, "err", err)
		}
		return nil
	}
	// Whole instructions, in order, as many as the task has room for: one the run is not
	// given in full is not delivered, and is not cut. The rest stay queued.
	rows, _ = fit(task, rows)
	var took []steer
	for _, s := range rows {
		said := map[string]any{"role": "user", "text": s.body, "queued": true}
		if !s.minted() {
			said["requestId"] = s.request
		}
		b, _ := json.Marshal(said)
		if err := Engine(log).Sessions.Log(ctx, org, session, kindMessage, subject, b); err != nil {
			if log != nil {
				log("coding: a resumed run's session did not record an instruction said to it; it stays queued", "org", org, "session", session, "seq", s.seq, "err", err)
			}
			break
		}
		took = append(took, s)
	}
	for _, s := range took {
		if _, err := l.receive(ctx, s); err != nil && log != nil {
			// Recorded and not marked: the task carries it and the next read repeats it,
			// which is a repeat and never a loss.
			log("coding: could not mark an instruction delivered", "org", org, "session", session, "seq", s.seq, "err", err)
		}
	}
	return took
}

// fit is the rows that join task whole under the prompt limit (maxPromptLen), which Run cuts
// a task at, and the rows that do not. It goes through the rows in order with a running total,
// taking each that still fits, so one that does not is left and the ones behind it that do
// are not held up by it. An instruction is never cut to fit.
func fit(task string, rows []steer) (took, left []steer) {
	used := len(strings.TrimSpace(task))
	lead := len(saidLead)
	if used == 0 {
		lead -= 2 // the blank lines before it are trimmed from a task that says nothing else
	}
	for _, s := range rows {
		add := 2 + len(strings.TrimSpace(s.body))
		if len(took) == 0 {
			add += lead
		}
		if used+add <= maxPromptLen {
			used += add
			took = append(took, s)
		} else {
			left = append(left, s)
		}
	}
	return took, left
}

// never says whether a row fits no task at all: not one that says nothing else.
func never(s steer) bool {
	took, _ := fit("", []steer{s})
	return len(took) == 0
}

// carrying is what a start after a run took from its inbox: the rows claimed for its new
// run's task, in order, and how many it left on the run it follows, or set aside.
type carrying struct {
	after string
	rows  []steer
	// drops are the rows that fit no task: counted now, set aside when the run is spawned
	// (finish), so a start that is refused leaves them queued for its retry to report.
	drops []steer
	left  int
}

// note says what became of the queue, for the answer to the start, or "" when it was empty.
func (c carrying) note() string {
	if len(c.rows)+c.left+len(c.drops) == 0 {
		return ""
	}
	n := fmt.Sprintf("%d of the instructions queued on %s are in this run's task", len(c.rows), c.after)
	if c.left > 0 {
		n += fmt.Sprintf("; %d did not fit it and are still queued there", c.left)
	}
	if len(c.drops) > 0 {
		n += fmt.Sprintf("; %d fit no task (more than %d bytes) and were dropped", len(c.drops), widest)
	}
	return n
}

// carryAfter takes what was left queued on the session a run is started after, for the
// new run's task: its person's instructions that no run took, whole, in order, each that
// fits. The run that ended may have been lost (an orphan end) or stopped for them (a
// follow-up that could not start), and a start with `after` is how its work goes on.
//
// A row is claimed before the run is spawned (the new run's task depends on which rows it
// won, and two starts after one run must not both carry one): it is carrying, and delivered
// once the run is spawned (finish), or queued again if the start is refused (release). Rows
// that fit no task are counted, and dropped once the run is spawned; rows that fit this
// one's no more are left. A claim is tagged with the start's request id, so that request,
// retried after the attempt that held the rows died, takes them back at once.
func carryAfter(ctx context.Context, org, subject, after, task string, log func(msg string, kv ...any)) carrying {
	c := carrying{after: after}
	request := ""
	if k := claimOf(ctx); k != nil {
		request = k.k.id
	}
	l := flights.ledger()
	if l == nil || after == "" {
		return c
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	rows, err := l.unfinished(ctx, org, after, request)
	if err != nil {
		if log != nil {
			log("coding: could not read the instructions left queued on the run a start follows; it starts without them", "org", org, "after", after, "err", err)
		}
		return c
	}
	var own []steer
	for _, s := range rows {
		if s.subject != subject {
			continue
		}
		if never(s) {
			c.drops = append(c.drops, s)
			continue
		}
		own = append(own, s)
	}
	took, left := fit(task, own)
	c.left = len(left)
	now := l.now().UnixMilli()
	for _, s := range took {
		// A claim of this request's own is taken over with a time later than the one it
		// held, so the attempt that held it, if it is only slow, can no longer move it.
		at := max(now, s.delivered+1)
		ok, err := l.move(ctx, s, heldBy(request), at)
		switch {
		case err != nil:
			c.left++
			if log != nil {
				log("coding: could not claim an instruction left queued for a new run; it stays queued", "org", org, "after", after, "seq", s.seq, "err", err)
			}
		case ok:
			s.state, s.delivered = heldBy(request), at
			c.rows = append(c.rows, s)
		}
		// A row another start won is in that run's task, not this one's.
	}
	return c
}

// release gives the claimed rows back, for a start that was refused before it spawned.
func (c carrying) release(ctx context.Context, log func(msg string, kv ...any)) {
	l := flights.ledger()
	if l == nil || len(c.rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	for _, s := range c.rows {
		if _, err := l.move(ctx, s, steerQueued, 0); err != nil && log != nil {
			log("coding: could not give back an instruction claimed for a start that was refused; it is queued again once the claim is old", "after", c.after, "seq", s.seq, "err", err)
		}
	}
}

// finish marks the claimed rows delivered, and sets aside the ones that fit no task, once
// the new run has been spawned with the rest.
func (c carrying) finish(ctx context.Context, log func(msg string, kv ...any)) {
	l := flights.ledger()
	if l == nil || len(c.rows)+len(c.drops) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	for _, s := range c.rows {
		if _, err := l.move(ctx, s, steerDelivered, l.now().UnixMilli()); err != nil && log != nil {
			// The run has it and the mark was missed: carrying until the claim is old, then
			// carried again by a start after the same run, which is a repeat and never a loss.
			log("coding: could not mark an instruction delivered", "after", c.after, "seq", s.seq, "err", err)
		}
	}
	for _, s := range c.drops {
		if _, err := l.move(ctx, s, steerDropped, l.now().UnixMilli()); err != nil && log != nil {
			log("coding: could not set aside an instruction that fits no task; the next start after the run reports it again", "after", c.after, "seq", s.seq, "err", err)
		}
	}
}

// saidLead is what stands between a task and the instructions said to its run since.
const saidLead = "\n\nThen the person who asked said:"

// widest is the widest instruction a task can hold, with nothing else in it: the limit
// of a task less what stands before the instructions. A longer one fits no task.
const widest = maxPromptLen - len(saidLead)

// Said is a task with what was said to its run since: what it was asked, then, in order,
// each instruction. It is the task a resumed or followed-up run is given.
func Said(task string, said []string) string {
	if len(said) == 0 {
		return task
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(task))
	b.WriteString(saidLead)
	for _, s := range said {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(s))
	}
	return b.String()
}

// withSaid is Said for a run's inbox.
func withSaid(task string, rows []steer) string {
	said := make([]string, len(rows))
	for i, s := range rows {
		said[i] = s.body
	}
	return Said(task, said)
}

// steersOf is a run's inbox as its changes show it.
func steersOf(rows []steer) []client.CodingSteered {
	out := make([]client.CodingSteered, 0, len(rows))
	for _, s := range rows {
		if claimed(s.state) {
			s.state = steerQueued // claimed for a run that is not spawned yet
		}
		x := client.CodingSteered{Text: s.body, State: s.state, Said: time.UnixMilli(s.said).UTC().Format(time.RFC3339), RequestID: s.request}
		if s.state == steerDelivered {
			x.Delivered = time.UnixMilli(s.delivered).UTC().Format(time.RFC3339)
		}
		if s.minted() {
			x.RequestID = "" // one made here, for a caller that sent none
		}
		out = append(out, x)
	}
	return out
}
