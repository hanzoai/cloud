// Package loop is the Dev agent loop's host (HIP-1330): the reasoning core as wasm,
// the action ledger it is dispatched through, and the three things the core asks
// its host for.
//
// The core thinks and the host acts. A turn steps the core with an event, and what
// comes back is the work: ask the model, touch the workspace, show the person
// something, save, finish. The host does each and steps the core with what
// happened. Between those two halves is the ledger, which is why a crash after
// `cargo test` replays the RESULT and never the command.
//
// Nothing here knows what a sandbox, a model gateway or a browser is. Host is the
// whole of what the loop asks of the world.
package loop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	dev "github.com/hanzoai/dev/go"
)

// Host performs what the core asks for.
type Host interface {
	// Ask is the model: the whole conversation in, a reply out. A reply may carry
	// tool calls; the core maps them to actions.
	Ask(ctx context.Context, ask dev.Ask) (dev.Reply, error)
	// Effect does one thing in the workspace — read, write, patch, exec, git or
	// browse — and answers what it produced. lease is the cell the session holds,
	// empty when it holds none; a host that needs one leases it and answers its id
	// as the lease, which the loop records. A failed effect is a result the core
	// reasons about (failed true); err is only for the host being unable to try.
	Effect(ctx context.Context, lease string, op dev.Op) (text string, failed bool, held string, err error)
	// Emit shows whoever is watching a line of text, as it happens.
	Emit(text string)
}

// Interrupter is a host that can stop what is running in a cell.
type Interrupter interface {
	Interrupt(ctx context.Context, lease string) error
}

// ErrBusy is a session another turn holds.
var ErrBusy = errors.New("dev: another turn holds this session")

// Loop runs turns of sessions through a core and a host.
type Loop struct {
	Core  *dev.Core
	Store func(org string) (*Store, error)
	// Ship makes an org's ledger durable and fails unless that was acknowledged. On a
	// durable deployment an org file reopens from its last shipped copy, so a write that
	// was committed and never shipped is gone after an unclean restart; a claim is
	// shipped before its effect runs and a turn before it is acknowledged. Nil is a
	// deployment with no durable plane.
	Ship func(org string) error
	// Model and Prelude are every new session's Config.
	Model, Prelude *string

	mu sync.Mutex
}

// Session is a session as a caller reads it.
type Session struct {
	ID, Owner, Lease, Status, Updated string
	Seq                               int64
}

// New records a session for owner.
func (l *Loop) New(org, owner, id string) error {
	st, err := l.Store(org)
	if err != nil {
		return err
	}
	return st.create(row{ID: id, Owner: owner, Status: statusIdle})
}

// Get reads one session of owner's. Another person's is the same as none.
func (l *Loop) Get(org, owner, id string) (Session, error) {
	st, err := l.Store(org)
	if err != nil {
		return Session{}, err
	}
	r, err := st.get(id)
	if err != nil || r.Owner != owner {
		return Session{}, errNoSession
	}
	return Session{ID: r.ID, Owner: r.Owner, Lease: r.Lease, Status: r.Status, Updated: r.Updated, Seq: r.Seq}, nil
}

// List is owner's sessions, newest movement first.
func (l *Loop) List(org, owner string) ([]Session, error) {
	st, err := l.Store(org)
	if err != nil {
		return nil, err
	}
	rs, err := st.list(owner)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rs))
	for _, r := range rs {
		out = append(out, Session{ID: r.ID, Owner: r.Owner, Lease: r.Lease, Status: r.Status, Updated: r.Updated, Seq: r.Seq})
	}
	return out, nil
}

// ErrNoSession is the answer for a session that is not owner's.
var ErrNoSession = errNoSession

// ErrSuspended is a session its person has put away; it takes no turn until resumed.
var ErrSuspended = errors.New("dev: the session is suspended")

// Turn is Submit and then Drive: a person's words in, the turn run to its end.
func (l *Loop) Turn(ctx context.Context, org, owner, id, prompt string, host Host) error {
	if strings.TrimSpace(prompt) != "" {
		if err := l.Submit(ctx, org, owner, id, prompt); err != nil {
			return err
		}
	}
	return l.Drive(ctx, org, owner, id, host)
}

// Submit takes a person's words as the session's next turn and records what the
// core asks for in answer, and nothing more. It performs no action: the work is in
// the session's pending list, durable, for Drive to do and to do again if it is cut
// off. That split is what lets the work run in the background.
func (l *Loop) Submit(ctx context.Context, org, owner, id, prompt string) error {
	return l.held(ctx, org, owner, id, nil, func(d *drive, r row) error {
		session, err := d.open(ctx, r)
		if err != nil {
			return err
		}
		defer func() { _ = session.Close(context.WithoutCancel(ctx)) }()
		var work []dev.Action
		if len(r.Pending) > 0 {
			if err := json.Unmarshal(r.Pending, &work); err != nil {
				return fmt.Errorf("dev: read the pending actions of %s: %w", d.id, err)
			}
		}
		n, err := d.st.nextTurn(d.id, d.token)
		if err != nil {
			return err
		}
		if _, err = d.step(ctx, session, dev.Event{Turn: &dev.Turn{ID: uint64(n), Prompt: prompt}}, work); err != nil {
			return err
		}
		return d.ship()
	})
}

// Drive does the session's pending work until none is left, through host. A call
// that is cut off leaves the rest pending, and the next one carries on where the
// ledger says it was. An empty list is done.
func (l *Loop) Drive(ctx context.Context, org, owner, id string, host Host) error {
	return l.held(ctx, org, owner, id, host, func(d *drive, r row) error {
		return d.run(ctx, r)
	})
}

// Answer records an observation made outside this plane for one of the core's pending
// actions — a person's own machine ran the command — and tells the core. The ledger is
// checked first: an id it has already answered is answered from its row, and an answer
// that disagrees with the row is refused, because the row is what the effect said.
// The action is then not done here.
func (l *Loop) Answer(ctx context.Context, org, owner, id string, obs Observation) error {
	return l.held(ctx, org, owner, id, nil, func(d *drive, r row) error {
		var work []dev.Action
		if err := json.Unmarshal(r.Pending, &work); err != nil {
			return fmt.Errorf("dev: read the pending actions of %s: %w", d.id, err)
		}
		at := -1
		for i, a := range work {
			if int64(a.ID) == obs.ID {
				at = i
			}
		}
		if at < 0 {
			return fmt.Errorf("dev: action %d is not one the core is waiting on", obs.ID)
		}
		kind, _, err := describe(work[at].Op)
		if err != nil {
			return err
		}
		rec, wrote, err := d.st.answer(d.id, d.token, obs)
		if err != nil {
			return err
		}
		session, err := d.open(ctx, r)
		if err != nil {
			return err
		}
		defer func() { _ = session.Close(context.WithoutCancel(ctx)) }()
		rest := append(append([]dev.Action(nil), work[:at]...), work[at+1:]...)
		if _, err := d.step(ctx, session, event(work[at].ID, kind, rec), rest); err != nil {
			if wrote {
				_ = d.st.unmint(d.id, "", obs.ID)
			}
			return err
		}
		return d.ship()
	})
}

// Pending reports whether the session has work waiting.
func (l *Loop) Pending(org, owner, id string) (bool, error) {
	st, err := l.Store(org)
	if err != nil {
		return false, err
	}
	r, err := st.get(id)
	if err != nil || r.Owner != owner {
		return false, errNoSession
	}
	var work []dev.Action
	if len(r.Pending) > 0 {
		if err := json.Unmarshal(r.Pending, &work); err != nil {
			return false, err
		}
	}
	return len(work) > 0, nil
}

// held runs one op with the session taken for it.
func (l *Loop) held(ctx context.Context, org, owner, id string, host Host, do func(*drive, row) error) error {
	st, err := l.Store(org)
	if err != nil {
		return err
	}
	r, err := st.get(id)
	if err != nil || r.Owner != owner {
		return errNoSession
	}
	switch r.Status {
	case statusStopped:
		return errors.New("dev: the session is stopped")
	case statusSuspended:
		return ErrSuspended
	}
	if r.Turn != "" && !r.lapsed() {
		return ErrBusy
	}
	token := newToken()
	if ok, err := st.take(id, token, r.Turn, statusIdle, statusLive); err != nil || !ok {
		if err != nil {
			return err
		}
		return ErrBusy
	}
	if r.Turn != "" && r.Lease != "" {
		// A turn that stopped renewing may have died with a command running in the cell,
		// and the sandbox does not stop a command because its caller went away. The turn
		// that takes the session over stops it before anything else runs.
		if i, ok := host.(Interrupter); ok {
			if err := i.Interrupt(ctx, r.Lease); err != nil {
				_, _ = st.leave(id, token)
				return fmt.Errorf("dev: the cell of a dead turn could not be interrupted: %w", err)
			}
		}
	}
	d := &drive{l: l, st: st, org: org, id: id, token: token, lease: r.Lease}
	d.host = &told{Host: host, st: st, id: id}
	defer func() { _, _ = st.leave(id, token) }()
	stop := d.keepHeld(ctx)
	defer stop()
	return do(d, r)
}

// told is a host that also writes what it shows to the session's record.
type told struct {
	Host
	st *Store
	id string
}

func (t *told) Emit(text string) {
	_, _ = t.st.appendEmit(t.id, text)
	if t.Host != nil {
		t.Host.Emit(text)
	}
}

// Lines reads what was shown to watchers of owner's session after line n.
func (l *Loop) Lines(org, owner, id string, n int64) ([]Line, error) {
	st, err := l.Store(org)
	if err != nil {
		return nil, err
	}
	if r, err := st.get(id); err != nil || r.Owner != owner {
		return nil, errNoSession
	}
	return st.emitsAfter(id, n)
}

// Lease is the cell the session holds, empty when it holds none.
func (l *Loop) Lease(org, owner, id string) (string, error) {
	s, err := l.Get(org, owner, id)
	return s.Lease, err
}

// Suspend puts a session away: it takes no turn until resumed, and the cell it
// holds is named so the host can give it back. Nothing else about it changes: the
// core's state, its pending work and its journal stay where they are.
func (l *Loop) Suspend(ctx context.Context, org, owner, id string) (lease string, err error) {
	err = l.held(ctx, org, owner, id, nil, func(d *drive, r row) error {
		lease = r.Lease
		ok, err := d.st.settle(d.id, d.token, statusSuspended, r.Lease)
		if err == nil && !ok {
			err = ErrBusy
		}
		return err
	})
	return lease, err
}

// Unleased records that a suspended session's cell has been given back.
func (l *Loop) Unleased(org, id, lease string) error {
	st, err := l.Store(org)
	if err != nil {
		return err
	}
	_, err = st.unlease(id, lease)
	return err
}

// Resume brings a suspended session back. The next action leases a cell afresh, on
// the same disk. The cell it suspended with must have been given back first.
func (l *Loop) Resume(org, owner, id string) error {
	st, err := l.Store(org)
	if err != nil {
		return err
	}
	r, err := st.get(id)
	if err != nil || r.Owner != owner {
		return errNoSession
	}
	// An ended session is not brought back: it answered 200 and the session it named
	// stayed ended, so a client took a session that would take no turn for a live one.
	if r.Status == statusStopped {
		return errNoSession
	}
	if r.Status != statusSuspended {
		return nil
	}
	if r.Lease != "" {
		return errors.New("dev: the suspended session's cell has not been given back yet")
	}
	token := newToken()
	if ok, err := st.take(id, token, r.Turn, statusSuspended); err != nil || !ok {
		if err != nil {
			return err
		}
		return ErrBusy
	}
	ok, err := st.settle(id, token, statusIdle, "")
	if err == nil && !ok {
		err = ErrBusy
	}
	return err
}

// Stop ends a session. Its cell, if it holds one, is the host's to give back.
func (l *Loop) Stop(org, owner, id string) (lease string, err error) {
	st, err := l.Store(org)
	if err != nil {
		return "", err
	}
	r, err := st.get(id)
	if err != nil || r.Owner != owner {
		return "", errNoSession
	}
	if _, err := st.end(id, r.Lease, r.Lease); err != nil {
		return "", err
	}
	return r.Lease, nil
}

// Ended records that a stopped session's cell is given back.
func (l *Loop) Ended(org, id, lease string) error {
	st, err := l.Store(org)
	if err != nil {
		return err
	}
	_, err = st.end(id, lease, "")
	return err
}

type drive struct {
	l     *Loop
	st    *Store
	org   string
	id    string
	token string
	host  Host
	lease string
}

func (d *drive) ship() error {
	if d.l.Ship == nil {
		return nil
	}
	return d.l.Ship(d.org)
}

// keepHeld renews the hold while the turn runs, so only a turn that died lets it lapse.
func (d *drive) keepHeld(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(holdFor / 4)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = d.st.renew(d.id, d.token)
			}
		}
	}()
	return func() { close(done) }
}

// queued is an action waiting its turn, as the pending list stores it.
type queued struct {
	Action dev.Action `json:"action"`
}

// open restores the session's core from its snapshot, or starts one.
func (d *drive) open(ctx context.Context, r row) (*dev.Session, error) {
	if len(r.State) > 0 {
		return d.l.Core.Restore(ctx, r.State)
	}
	return d.l.Core.New(ctx, dev.Config{Model: d.l.Model, Prelude: d.l.Prelude})
}

func (d *drive) run(ctx context.Context, r row) error {
	session, err := d.open(ctx, r)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close(context.WithoutCancel(ctx)) }()
	var work []dev.Action
	if len(r.Pending) > 0 {
		if err := json.Unmarshal(r.Pending, &work); err != nil {
			return fmt.Errorf("dev: read the pending actions of %s: %w", d.id, err)
		}
	}
	for len(work) > 0 {
		a := work[0]
		ev, done, err := d.perform(ctx, a)
		if err != nil {
			return err
		}
		rest := work[1:]
		if done {
			work = rest
			if _, err := d.save(session, rest); err != nil {
				return err
			}
			continue
		}
		acts, err := d.step(ctx, session, ev, rest)
		if err != nil {
			return err
		}
		work = append(rest, acts...)
	}
	return nil
}

// step feeds the core one event, then keeps its snapshot and the work still to do.
func (d *drive) step(ctx context.Context, s *dev.Session, ev dev.Event, rest []dev.Action) ([]dev.Action, error) {
	acts, err := s.Step(ctx, ev)
	if err != nil {
		return nil, err
	}
	if _, err := d.save(s, append(append([]dev.Action(nil), rest...), acts...)); err != nil {
		return nil, err
	}
	return acts, nil
}

func (d *drive) save(s *dev.Session, work []dev.Action) (bool, error) {
	state, err := s.Snapshot(context.Background())
	if err != nil {
		return false, err
	}
	pending, err := json.Marshal(work)
	if err != nil {
		return false, err
	}
	ok, err := d.st.save(d.id, d.token, state, pending)
	if err == nil && !ok {
		err = errors.New("dev: this turn no longer holds the session")
	}
	return ok, err
}

// perform does one action and answers the event that tells the core. done is true for
// an action the core is not told about (Emit, Save, Done).
func (d *drive) perform(ctx context.Context, a dev.Action) (ev dev.Event, done bool, err error) {
	op := a.Op
	switch {
	case op.Emit != nil:
		d.host.Emit(*op.Emit)
		return dev.Event{}, true, nil
	case op.Save, op.Done != nil:
		return dev.Event{}, true, nil
	}
	kind, body, err := describe(op)
	if err != nil {
		return dev.Event{}, false, err
	}
	act := Action{ID: int64(a.ID), Kind: kind, Body: body}
	if kind == "model" {
		// Asking a model changes nothing, so an ask that was dispatched and never
		// answered is asked again rather than refused for good.
		if err := d.st.unmint(d.id, "", act.ID); err != nil {
			return dev.Event{}, false, err
		}
	}
	obs, err := d.dispatch(ctx, a, act)
	if err != nil {
		return dev.Event{}, false, err
	}
	return event(a.ID, kind, obs), false, nil
}

// dispatch is the ledger's half of an action: claim its id, and only when this call
// minted it, ship the claim, have the host do it, and record and ship the answer. An
// id the journal already holds is answered from its row, or refused, and the host is
// not asked.
func (d *drive) dispatch(ctx context.Context, a dev.Action, act Action) (Observation, error) {
	obs, seen, err := d.st.claim(d.id, d.token, act)
	if err != nil || seen {
		return obs, err
	}
	// The claim is shipped before the effect runs: a claim that was only committed
	// is gone after an unclean restart and the same id would be performed again.
	if err := d.ship(); err != nil {
		_ = d.st.unmint(d.id, act.Thread, act.ID)
		return Observation{}, fmt.Errorf("dev: action %d was not dispatched: %w", act.ID, err)
	}
	obs, err = d.do(ctx, a, act.Kind)
	if err != nil {
		_ = d.st.unmint(d.id, act.Thread, act.ID)
		return Observation{}, err
	}
	if err := d.st.complete(d.id, act.Thread, act.ID, obs); err != nil {
		return Observation{}, err
	}
	if err := d.ship(); err != nil {
		return Observation{}, fmt.Errorf("dev: action %d answered and its answer was not shipped: %w", act.ID, err)
	}
	return obs, nil
}

// do asks the host to do the action and shapes its answer as the ledger records it.
func (d *drive) do(ctx context.Context, a dev.Action, kind string) (Observation, error) {
	obs := Observation{ID: int64(a.ID)}
	if a.Op.Model != nil {
		reply, err := d.host.Ask(ctx, *a.Op.Model)
		if ctx.Err() != nil {
			// The turn was cut off, not answered: nothing is recorded, so the resume asks again.
			return obs, ctx.Err()
		}
		if err != nil {
			obs.Err = err.Error()
			return obs, nil
		}
		b, err := json.Marshal(reply)
		if err != nil {
			return obs, err
		}
		obs.Out = string(b)
		return obs, nil
	}
	text, failed, held, err := d.host.Effect(ctx, d.lease, a.Op)
	if err != nil {
		return obs, err
	}
	if held != "" && held != d.lease {
		if ok, err := d.st.hold(d.id, d.token, held); err != nil || !ok {
			// The session was stopped while the turn ran. The cell is named on the row so
			// the next stop ends it, and nothing is left holding a pod nobody knows about.
			_, _ = d.st.keep(d.id, held, "")
			return obs, fmt.Errorf("dev: this turn could not record the cell it leased: %v", err)
		}
		d.lease = held
	}
	obs.Out = text
	if failed {
		obs.Exit = 1
	}
	return obs, nil
}

// output is what an effect's observation tells the core.
func output(id uint64, o Observation) dev.Output {
	out := dev.Output{ID: id, Text: o.Out, Failed: o.Exit != 0 || o.Err != ""}
	if o.Err != "" {
		out.Text = o.Err
	}
	return out
}

// event is the core's event for an action's observation.
func event(id uint64, kind string, o Observation) dev.Event {
	out := output(id, o)
	switch kind {
	case "model":
		var reply dev.Reply
		if o.Err == "" {
			_ = json.Unmarshal([]byte(o.Out), &reply)
		} else {
			t := o.Err
			reply.Text = &t
		}
		return dev.Event{Model: &dev.Answer{ID: id, Reply: reply}}
	case "exec":
		return dev.Event{Exec: &out}
	case "git":
		return dev.Event{Git: &out}
	case "browse":
		return dev.Event{Browse: &out}
	}
	return dev.Event{File: &out}
}

// describe names an op and records the body an id is checked against.
func describe(op dev.Op) (string, json.RawMessage, error) {
	var kind string
	var v any
	switch {
	case op.Model != nil:
		kind, v = "model", op.Model
	case op.Read != nil:
		kind, v = "read", op.Read
	case op.Write != nil:
		kind, v = "write", op.Write
	case op.Patch != nil:
		kind, v = "patch", op.Patch
	case op.Exec != nil:
		kind, v = "exec", op.Exec
	case op.Git != nil:
		kind, v = "git", op.Git
	case op.Browse != nil:
		kind, v = "browse", op.Browse
	default:
		return "", nil, errors.New("dev: the core asked for nothing the loop can do")
	}
	b, err := json.Marshal(v)
	return kind, b, err
}

// OpenStore opens a ledger on db, for cloud.OrgStore.
func OpenStore(db *sql.DB) (*Store, error) { return Open(db) }

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Held is a session taken for as long as a client is connected to it, for a driver that
// runs the core itself (the Dev frame adapter). Its hold is renewed until Release, so a
// second connection is refused and a connection whose process died lets go.
type Held struct {
	// Frames is the driver's state as it was last saved, nil for a new session.
	Frames []byte

	l       *Loop
	st      *Store
	org     string
	id      string
	token   string
	stop    func()
	release sync.Once

	mu    sync.Mutex
	lease string // the cell the session holds, empty when it holds none
}

// Hold takes owner's session until Release.
func (l *Loop) Hold(ctx context.Context, org, owner, id string) (*Held, error) {
	st, err := l.Store(org)
	if err != nil {
		return nil, err
	}
	r, err := st.get(id)
	if err != nil || r.Owner != owner {
		return nil, errNoSession
	}
	switch r.Status {
	case statusStopped:
		return nil, errors.New("dev: the session is stopped")
	case statusSuspended:
		return nil, ErrSuspended
	}
	if r.Turn != "" && !r.lapsed() {
		return nil, ErrBusy
	}
	token := newToken()
	if ok, err := st.take(id, token, r.Turn, statusIdle, statusLive); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, ErrBusy
	}
	d := &drive{l: l, st: st, org: org, id: id, token: token}
	return &Held{Frames: r.Frames, lease: r.Lease, l: l, st: st, org: org, id: id, token: token, stop: d.keepHeld(context.WithoutCancel(ctx))}, nil
}

// Lease is the cell the session holds, empty when it holds none.
func (h *Held) Lease() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lease
}

// Perform does a workspace action a chat thread's core asked for, through the
// journal, as a turn of the loop does its own: the action is claimed under the
// session, the thread and its id before host is asked, so an id already answered is
// answered from its row and one dispatched and never answered is refused, and
// neither is done twice. A connection that no longer holds the session mints
// nothing, so it runs nothing. A cell the host leased is recorded on the session.
// A model ask is not a workspace action and is refused.
func (h *Held) Perform(ctx context.Context, thread string, a dev.Action, host Host) (dev.Output, error) {
	kind, body, err := describe(a.Op)
	if err != nil {
		return dev.Output{}, err
	}
	if kind == "model" {
		return dev.Output{}, errors.New("dev: a model ask is not a workspace action")
	}
	d := &drive{l: h.l, st: h.st, org: h.org, id: h.id, token: h.token, host: host, lease: h.Lease()}
	obs, err := d.dispatch(ctx, a, Action{ID: int64(a.ID), Kind: kind, Body: body, Thread: thread})
	if d.lease != "" {
		h.mu.Lock()
		h.lease = d.lease
		h.mu.Unlock()
	}
	if err != nil {
		return dev.Output{}, err
	}
	return output(a.ID, obs), nil
}

// Save keeps the driver's state.
func (h *Held) Save(frames []byte) error {
	ok, err := h.st.saveFrames(h.id, h.token, frames)
	if err == nil && !ok {
		err = errors.New("dev: this connection no longer holds the session")
	}
	return err
}

// Cell records the cell the session leased.
func (h *Held) Cell(lease string) error {
	ok, err := h.st.hold(h.id, h.token, lease)
	if err == nil && !ok {
		_, _ = h.st.keep(h.id, lease, "")
		err = errors.New("dev: this connection no longer holds the session")
	}
	if err == nil {
		h.mu.Lock()
		h.lease = lease
		h.mu.Unlock()
	}
	return err
}

// Release gives the session back.
func (h *Held) Release() {
	h.release.Do(func() {
		h.stop()
		_, _ = h.st.leave(h.id, h.token)
	})
}
