package bot

// What calls a suspended run back.
//
// A run parks with a resume token and, if it wants one, a standing reason to
// come back: the events it should be called back for (POST
// /v1/bot/runs/:id/suspend, "wake"). While it is parked it is a row and nothing
// else — no goroutine, no timer, no connection, and nothing scanning for it.
// When one of the events it named is published in its org, the row is marked
// and the org's open connections are told. That is the pair the agent wake
// already keeps (notify.go): the mark is what survives an operator who is not
// looking, and the event is what makes "now" mean now.
//
// The wake does not perform the resume. Resume is where the token leaves, and
// it leaves to whoever brings the run back — a machine, an operator, an
// executor — which is never this cloud: nothing here starts a run (registry.go,
// unplaceable). A wake that moved the row to running would spend the one state
// in which the token can be collected and strand the run holding a checkpoint
// nobody can reach. So a wake makes the run due, and POST
// /v1/bot/runs/:id/resume stays the one way a run comes back, with the same
// token it left.
//
// A reason is spent when it fires. A run that asked to be called back on a
// session message is called back on the next one, not on every one after it,
// and it arms itself again by parking again.
//
// The index below is a hint, never the truth. The row decides: the wake re-reads
// it inside the transaction and refuses a run that has stopped or that no longer
// names the event, so an index out of step with the file can waste a read and
// can never wake a run that should have stayed asleep.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

func init() { Declare(runWake) }

// runWake says a suspended run's reason arrived and it is due back. It is
// addressed to the org rather than to a partition, because the run it names is
// a row in the org's own file and every connection of the org reads that file.
const runWake = "run.wake"

// maxWake bounds how many events one suspension may name. Every one of them has
// to be an event this surface declares, so the ceiling is there only to bound
// the decode.
const maxWake = 32

// wakeWait bounds the wake's own reads and writes. Every other write in this
// package runs on a request's context; this one runs on no request, because it
// happens on whatever goroutine published the event. A file that cannot be
// reached in this long leaves the run asleep with its reason intact, which is
// the state a later event wakes it from.
const wakeWait = 5 * time.Second

// runWoken is what a wake says: which run is due back, and what called it. It
// carries nothing of the event that caused it. An event may be addressed to the
// connections that asked for one key (Publish), and copying such an event's
// payload into this one — which the whole org hears — would widen the audience
// its author chose.
type runWoken struct {
	RunID   string `json:"runId"`
	WokenBy string `json:"wokenBy"`
	TS      int64  `json:"ts"`
}

// sleepers is which suspended runs one event of one org calls back.
//
// A waiting run has an entry here and nothing else, so waiting costs its row and
// a few words of memory rather than a goroutine. Nothing reads it on a schedule:
// it is consulted by the event's own delivery path, so a bot nobody is talking
// to is never looked at at all.
//
// The org is part of every address, which is what keeps one tenant's event from
// reaching another's run: an event is published for the org the validated caller
// acts in, and the runs it can reach are that org's.
type sleepers struct {
	mu sync.Mutex
	// read is the orgs whose roster this process has looked at. It is kept apart
	// from at because "no run of this org is waiting" and "this org has not been
	// read" are different facts, and only the second is worth a read of the file.
	read map[string]bool
	// at is org, then event, then the runs waiting there.
	at map[string]map[string]map[string]bool
}

var asleep = &sleepers{
	read: map[string]bool{},
	at:   map[string]map[string]map[string]bool{},
}

// due is the runs one event of an org calls back, and whether this process has
// read that org's roster at all. A caller told false has learned nothing about
// who is waiting, only that nobody has looked.
func (s *sleepers) due(org, event string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.read[org] {
		return nil, false
	}
	ids := make([]string, 0, len(s.at[org][event]))
	for id := range s.at[org][event] {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, true
}

// fill records what an org's roster says and marks the org read. It merges
// rather than replaces: a run may have parked while the roster was being read,
// and that run's entry is already right.
func (s *sleepers) fill(org string, rows []Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range rows {
		s.hold(org, x)
	}
	s.read[org] = true
}

// mind makes the index say what one row says: a suspended run waits at each
// event it named, and a run in any other state waits nowhere. Every write of a
// run row calls it, so no transition can drift the index by being forgotten.
func (s *sleepers) mind(org string, x Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, waiting := range s.at[org] {
		delete(waiting, x.ID)
	}
	s.hold(org, x)
}

// hold adds one row's entries. The caller holds the lock.
func (s *sleepers) hold(org string, x Run) {
	if !x.parked() {
		return
	}
	for _, event := range x.Wake {
		if s.at[org] == nil {
			s.at[org] = map[string]map[string]bool{}
		}
		if s.at[org][event] == nil {
			s.at[org][event] = map[string]bool{}
		}
		s.at[org][event][x.ID] = true
	}
}

// forget drops everything this process learned. Shutdown calls it: the index
// answers for the files the surface had open, and a surface mounted again over
// another directory must not be answered out of the last one's.
func (s *sleepers) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = map[string]bool{}
	s.at = map[string]map[string]map[string]bool{}
}

// wakes reads the reasons a suspension names. Each one must be an event this
// surface declares, which is the same list a client chooses what to listen for
// from — the handshake's `events`. An event nobody publishes is a run that
// sleeps forever, so it is refused here rather than accepted and never honoured.
func wakes(in []string) ([]string, error) {
	if len(in) > maxWake {
		return nil, zip.ErrBadRequest("wake names too many events")
	}
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = clip(e, maxField)
		if e == "" {
			continue
		}
		if !emits(e) {
			return nil, zip.ErrBadRequest("wake may name only events this surface publishes; the handshake lists them")
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// rouse calls back the runs of one org that named this event. It runs on
// whatever goroutine published the event, which is what makes waiting free:
// there is no scheduler here, and a sleeping run is not looked at until the
// thing it is waiting for happens.
func rouse(org, event string) {
	s := mounted.Load()
	if s == nil || org == "" {
		return
	}
	ids, read := asleep.due(org, event)
	if !read {
		// A process that has just started has read no rows, so the first event
		// an org publishes reads that org's roster. Once.
		learn(s, org)
		ids, _ = asleep.due(org, event)
	}
	for _, id := range ids {
		if roused(s, org, id, event) {
			// After the write and outside it: an org's file is opened one
			// connection at a time, and a run waiting on this event would open a
			// second transaction on that file from inside the first.
			PublishOrg(org, "", runWake, runWoken{
				RunID: id, WokenBy: event, TS: time.Now().UnixMilli(),
			})
		}
	}
}

// learn reads one org's runs and tells the index what is waiting. A read that
// fails leaves the org unread, so the next event tries again rather than
// answering out of a roster nobody managed to load.
func learn(s *cloud.Service[state], org string) {
	ctx, stop := context.WithTimeout(context.Background(), wakeWait)
	defer stop()
	st, err := storeFor(s, org, "")
	if err != nil {
		return // storeFor has logged what went wrong
	}
	rows, err := ours(ctx, st)
	if err != nil {
		s.Log.Error("bot: read the roster for wakes", "org", org, "err", err)
		return
	}
	asleep.fill(org, rows)
}

// roused marks one run due and reports whether this event is what did it.
//
// The row is re-read and re-checked inside the act: the index is a hint, and a
// run that stopped, resumed or was forgotten between the event and this write
// must not be woken by an entry nobody had cleared yet. The mark and the report
// land together, as every other transition of a run does.
func roused(s *cloud.Service[state], org, run, event string) bool {
	ctx, stop := context.WithTimeout(context.Background(), wakeWait)
	defer stop()
	st, err := storeFor(s, org, "")
	if err != nil {
		return false
	}
	var x Run
	var called bool
	err = st.Do(ctx, func(st *Store) error {
		cur, err := readRun(ctx, st, run)
		if err != nil {
			return err
		}
		x = cur
		if !cur.parked() || !slices.Contains(cur.Wake, event) {
			return nil
		}
		now := time.Now().UnixMilli()
		cur.Wake = nil // spent: the reason it named has arrived
		cur.WokenAt, cur.WokenBy, cur.UpdatedAt = now, event, now
		x, called = cur, true
		if err := st.Put(ctx, colRun, run, cur); err != nil {
			return err
		}
		r := Report{ID: mint("report"), Kind: "wake", Message: event, At: now}
		return st.Put(ctx, reported(run), r.ID, r)
	})
	switch {
	case errors.Is(err, ErrNoDoc):
		asleep.mind(org, Run{ID: run}) // gone from the file, gone from the index
		return false
	case err != nil:
		s.Log.Error("bot: wake a run", "org", org, "run", run, "event", event, "err", err)
		return false // a write that failed is not a run that moved
	}
	asleep.mind(org, x)
	return called
}
