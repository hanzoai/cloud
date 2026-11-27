package coord

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"path"
	"sync"
	"time"
)

// A loop's lease is one object in the deployment's object store: a round, a
// renewal count and the process that holds it. Everything about who runs a loop is
// decided there, by compare-and-set; the KV is never asked.
//
// HOLDING IS A PERIOD OF TIME, MEASURED FROM A WRITE. The holder renews by
// rewriting the object every Confirm, and a renewal that lands gives it term
// from the moment the write began. A read never confirms anything: only a write
// the store accepted against the version this process last wrote.
//
// TAKING NEEDS NO WORD FROM ANYONE. A lease nobody holds (its holder let it go after
// its passes ended) is taken at once. A held lease is taken only when its object
// has not changed for a full term, by this process's own clock, since it first
// read it: the holder's last renewal landed no later than that first read, so its
// term is over, and the compare-and-set lands only against that same version, so
// no renewal can have slipped in between. Nothing in that rule asks the KV, so one
// pod whose KV is down still takes the loop its dead predecessor held, and no
// process takes a lease another is renewing — not even another process of the
// same pod.
//
// A process that finds itself owning a round it never saw confirmed (its takeover
// landed but the reply was lost) waits a full term before acting all the same.
//
// An effect is admitted only while Effect of the term remains (Hold), and a
// pass's context ends when the term does (Do). An effect that outlives the budget
// is the one gap left, which is why every money effect also carries the ledger's
// idempotency key for its act: the lease keeps two pods from making it, and the key
// keeps a second making of it from moving money twice.
//
// THE STORE MUST BE READ AS IT IS. A read cache in front of it would show a taker
// an old version for longer and a holder nothing at all — the takeover still lands
// only against the version the store holds, but the term arithmetic above assumes
// every read is the store's own answer, so this is handed the store itself.
//
// WHERE THERE IS ONE WRITER the lease is a formality: a deployment that declares
// one writer runs its loops even while the object store cannot be read or written,
// as it always has — once the store has failed for a full wait without a break, so
// a process starting beside a live holder (a rolling update's surge pod) never
// runs on one failed read, and unless the last answer the store gave named another
// holder still within its term. A missing lease object is waited out like a new
// version: it may have been deleted from under a holder still in its term.
//
// HOW LONG EACH OF THESE LASTS is the Coord's own (Periods): Term, Effect, Confirm
// and Watch.

const leaseMagic = "loop2\x00"

func leaseKey(name string) string { return path.Join("loops", name) }

// lease is one loop's object.
type lease struct {
	round uint64 // rises on every change of holder
	seq   uint64 // rises on every renewal, so each write is new bytes and a new version
	owner string // the holding process; empty when it was let go
}

func encodeLease(l lease) []byte {
	b := make([]byte, 0, len(leaseMagic)+16+len(l.owner))
	b = append(b, leaseMagic...)
	b = binary.BigEndian.AppendUint64(b, l.round)
	b = binary.BigEndian.AppendUint64(b, l.seq)
	return append(b, l.owner...)
}

func decodeLease(b []byte) (lease, error) {
	n := len(leaseMagic)
	if len(b) < n+16 || !bytes.Equal(b[:n], []byte(leaseMagic)) {
		return lease{}, errors.New("coord: a loop lease that is not one")
	}
	return lease{
		round: binary.BigEndian.Uint64(b[n:]),
		seq:   binary.BigEndian.Uint64(b[n+8:]),
		owner: string(b[n+16:]),
	}, nil
}

func readLease(ctx context.Context, st Store, key string) (lease, string, error) {
	data, version, err := st.Get(ctx, key)
	switch {
	case errors.Is(err, ErrMissing):
		return lease{}, "", nil
	case err != nil:
		return lease{}, "", err
	}
	l, err := decodeLease(data)
	return l, version, err
}

// Leader is one loop's right to run in this process.
type Leader struct {
	c     *Coord
	name  string
	ready chan struct{} // closed when the first claim has been answered

	mu        sync.Mutex
	round     uint64    // the round this process holds; 0 when it holds none
	confirmed time.Time // when the last write that confirmed the round began
	notBefore time.Time // no effect before this: a round this process never saw confirmed
	seen      string    // the version of the lease last read ("" when there was none)
	seenAt    time.Time // when that version (or its absence) was first read
	other     bool      // the last read named another holder
	absent    bool      // the last read found no lease object at all
	failSince time.Time // when the store began failing to fence, unbroken since; zero while it answers
}

// Lead is the right to run the loop named name: the same value for every call in
// this process. The lease is asked for at once, and Do waits for that answer, so a
// loop's first pass is not lost to a claim still in flight; nothing on a mount path
// waits on the object store.
func (c *Coord) Lead(name string) *Leader {
	if c == nil {
		c = Default()
	}
	c.mu.Lock()
	if l := c.leaders[name]; l != nil {
		c.mu.Unlock()
		return l
	}
	l := &Leader{c: c, name: name, ready: make(chan struct{})}
	c.leaders[name] = l
	c.mu.Unlock()
	go l.keep()
	return l
}

func (l *Leader) keep() {
	p := l.c.p
	ctx, cancel := context.WithTimeout(context.Background(), p.claimWithin())
	l.claim(ctx)
	cancel()
	close(l.ready)
	t := time.NewTicker(p.Confirm)
	defer t.Stop()
	for {
		select {
		case <-l.c.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), p.claimWithin())
			l.claim(ctx)
			cancel()
		}
	}
}

// claimWithin bounds one claim: long enough for a slow store to answer a read and a
// write, and short enough that a renewal still lands with most of the term left.
// A renewal's term is counted from when it began, so a slow one only shortens it.
func (p Periods) claimWithin() time.Duration { return p.Term / 2 }

// staleAfter is how long a held lease's object must stay unchanged before it is
// taken: a term, and one renewal period of slack for a holder whose renewal is
// merely late.
func (p Periods) staleAfter() time.Duration { return p.Term + p.Confirm }

// claim renews the lease this process holds, takes one that is free or stale, or
// waits. A store that does not answer changes nothing: what this process holds
// stays held until its term runs out, so one failed write never hands the loop to
// nobody. The store is never asked while l.mu is held, so a slow write never
// delays an effect's Hold.
func (l *Leader) claim(ctx context.Context) {
	st := l.c.storeOf()
	if st == nil || l.c.closed.Load() {
		return
	}
	key := leaseKey(l.name)
	start := time.Now()
	cur, version, err := readLease(ctx, st, key)
	read := time.Now()
	if err != nil {
		l.refused(err)
		return
	}
	if cur.owner == l.c.owner {
		l.mu.Lock()
		if cur.round != l.round {
			// A round this process never saw confirmed: its takeover landed and the
			// reply was lost. It is this process's now, after a full term.
			l.round, l.notBefore = cur.round, read.Add(l.c.p.Term)
			l.c.log.log.Info("coord: this process runs the loop after a term", "loop", l.name, "round", cur.round, "after", l.notBefore)
		}
		l.other, l.absent = false, false
		l.mu.Unlock()
		_, err := st.PutIfVersion(ctx, key, encodeLease(lease{round: cur.round, seq: cur.seq + 1, owner: l.c.owner}), version)
		l.mu.Lock()
		defer l.mu.Unlock()
		if err != nil {
			if errors.Is(err, ErrConflict) {
				l.failSince = time.Time{}
			} else {
				l.failing(read)
				l.c.log.warn("lease:"+l.name, "coord: renewing a loop's lease failed; holding it until its term runs out", "loop", l.name, "err", err)
			}
			return
		}
		l.failSince = time.Time{}
		if l.round == cur.round {
			l.confirmed = start
		}
		return
	}
	l.mu.Lock()
	if l.round != 0 {
		l.c.log.log.Info("coord: another process holds the loop now", "loop", l.name, "holder", cur.owner, "round", cur.round)
		l.round = 0
	}
	// A missing object is a version like any other: it may have been deleted from
	// under a holder still in its term, so it is waited out before it is taken. A
	// lease its holder let go is an object naming nobody, and is taken at once.
	if version != l.seen || l.seenAt.IsZero() {
		l.seen, l.seenAt = version, read
	}
	l.other, l.absent = cur.owner != "", version == ""
	unchanged := read.Sub(l.seenAt)
	waiting := (l.other || l.absent) && unchanged < l.c.p.staleAfter()
	if waiting {
		l.failSince = time.Time{}
	}
	l.mu.Unlock()
	if waiting {
		holder := cur.owner
		if holder == "" {
			holder = "(no lease object)"
		}
		l.c.log.warn("wait:"+l.name, "coord: waiting for the loop's lease to go a term unrenewed", "loop", l.name, "holder", holder, "round", cur.round, "unchanged", unchanged.Round(time.Millisecond), "takes", l.c.p.staleAfter())
		return
	}
	next := lease{round: cur.round + 1, owner: l.c.owner}
	_, err = st.PutIfVersion(ctx, key, encodeLease(next), version)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		if errors.Is(err, ErrConflict) {
			l.failSince = time.Time{}
			return
		}
		l.failing(read)
		l.c.log.warn("lease:"+l.name, "coord: taking a loop's lease failed", "loop", l.name, "err", err)
		return
	}
	l.round, l.confirmed, l.notBefore, l.failSince, l.other, l.absent = next.round, start, time.Time{}, time.Time{}, false, false
	l.c.log.log.Info("coord: this process runs the loop", "loop", l.name, "round", next.round, "from", cur.owner)
}

// failing records that the store failed to fence at now, keeping the start of an
// unbroken run of failures. Caller holds l.mu.
func (l *Leader) failing(now time.Time) {
	if l.failSince.IsZero() {
		l.failSince = now
	}
}

func (l *Leader) refused(err error) {
	l.mu.Lock()
	l.failing(time.Now())
	l.mu.Unlock()
	l.c.log.warn("lease:"+l.name, "coord: a loop's lease cannot be read; holding what is held until its term runs out", "loop", l.name, "err", err)
}

// alone reports whether this process may run the loop as the deployment's one
// writer without a lease: the deployment declares no second writer, and the store
// cannot fence — there is none, or it has failed without a break for a full wait
// (so a process starting beside a live holder never runs on one failed read) — and
// it did not last name another holder still within its term. Caller holds l.mu.
func (l *Leader) alone(now time.Time) bool {
	if l.c.Peered() {
		return false
	}
	if l.c.storeOf() == nil {
		return true
	}
	if l.failSince.IsZero() || now.Sub(l.failSince) < l.c.p.staleAfter() {
		return false
	}
	return !l.other || now.Sub(l.seenAt) >= l.c.p.staleAfter()
}

// held reports whether this process holds the lease with Effect of its term
// left. Caller holds l.mu.
func (l *Leader) held(now time.Time) bool {
	return l.round != 0 && !now.Before(l.notBefore) && now.Add(l.c.p.Effect).Before(l.confirmed.Add(l.c.p.Term))
}

// Handover is how long a loop's lease can be held by a pod that is gone before another
// takes it: a term, and one renewal period of slack. A pass that fell due while another
// pod led, and was not made, is asked again once it has passed.
func (c *Coord) Handover() time.Duration { return c.p.staleAfter() }

// Round is the lease round this process holds for the loop, 0 when it holds none.
// It rises on every change of holder, so an effect written elsewhere can carry it
// and be fenced by it.
func (l *Leader) Round() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.round
}

// Hold is the fence an effect asks immediately before it is made: nil when this
// process may act for the loop now, else ErrNotHeld.
func (l *Leader) Hold(context.Context) error {
	if l.c.closed.Load() {
		return ErrNotHeld
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.held(now) || l.alone(now) {
		return nil
	}
	return ErrNotHeld
}

// end is when this process's right to the loop runs out, and the zero time when it
// holds none.
func (l *Leader) end() time.Time {
	if l.c.closed.Load() {
		return time.Time{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	switch {
	case l.round != 0 && !now.Before(l.notBefore):
		if end := l.confirmed.Add(l.c.p.Term); end.After(now) {
			return end
		}
	}
	if l.alone(now) {
		return now.Add(l.c.p.Watch) // re-asked as often as the watcher looks
	}
	return time.Time{}
}

// Do runs pass while this process holds the loop, and answers ErrNotHeld when it
// does not. A round this process must wait out before acting is waited out (until
// ctx ends) rather than skipped. pass's context ends when the right does, and Close
// waits for pass before it lets the loop go, so no pass outlives the right it
// started under.
func (l *Leader) Do(ctx context.Context, pass func(context.Context)) error {
	select {
	case <-l.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	// A round this process must wait out, or a lease with no object that it will
	// take once the object has stayed missing for a term (a deployment's first run
	// of a loop), is waited for rather than skipped, so the first pass is late and
	// not lost.
	for {
		l.mu.Lock()
		now := time.Now()
		var wait time.Duration
		switch {
		case l.round != 0:
			wait = l.notBefore.Sub(now)
		case l.absent:
			wait = min(l.seenAt.Add(l.c.p.staleAfter()+l.c.p.Confirm).Sub(now), l.c.p.Watch)
		}
		l.mu.Unlock()
		if wait <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	l.c.mu.Lock()
	if l.c.closed.Load() {
		l.c.mu.Unlock()
		return ErrNotHeld
	}
	l.c.passes.Add(1)
	l.c.mu.Unlock()
	defer l.c.passes.Done()
	if err := l.Hold(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			end := l.end()
			left := time.Until(end)
			if end.IsZero() || left <= 0 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(min(left, l.c.p.Watch)):
			}
		}
	}()
	pass(ctx)
	return nil
}

// Close takes this process out of the deployment in the order that keeps two
// holders apart: no pass starts after it, the passes running finish (until ctx
// ends), and only then is each lease this process holds written free, so the next
// holder starts without waiting. The pod's root also leaves the live set.
func (c *Coord) Close(ctx context.Context) {
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return
	}
	c.closed.Store(true)
	leaders := make([]*Leader, 0, len(c.leaders))
	for _, l := range c.leaders {
		leaders = append(leaders, l)
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.passes.Wait(); close(done) }()
	drained := true
	select {
	case <-done:
	case <-ctx.Done():
		drained = false
		c.log.log.Warn("coord: closing with passes still running; their leases lapse instead of being let go", "err", ctx.Err())
	}
	close(c.stop)
	if drained {
		if st := c.storeOf(); st != nil {
			for _, l := range leaders {
				l.release(ctx, st)
			}
		}
	}
	c.Leave(ctx)
	c.closeWire()
}

// release writes the lease free when this process still holds it.
func (l *Leader) release(ctx context.Context, st Store) {
	key := leaseKey(l.name)
	cur, version, err := readLease(ctx, st, key)
	if err != nil || cur.owner != l.c.owner {
		return
	}
	free := lease{round: cur.round, seq: cur.seq + 1}
	if _, err := st.PutIfVersion(ctx, key, encodeLease(free), version); err != nil {
		l.c.log.warn("release:"+l.name, "coord: letting a loop go failed; its lease lapses instead", "loop", l.name, "err", err)
	}
}
