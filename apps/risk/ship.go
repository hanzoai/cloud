package risk

// ship.go is durability: an acknowledged record is one that reached the org's
// durable object, not one that reached a pod's disk.
//
// WHY THIS PLANE NEEDS IT AND WHY IT IS NOT A LINE AT EVERY WRITE. A decision is
// an ADVERSE ACTION RECORD — the answer to "which model declined this customer,
// on what evidence, under which policy" — and a label is what a regulator reads
// back as the outcome. cloud deploys Recreate at one replica, so a pod that is
// killed ungracefully takes with it everything written since the volume was last
// intact, and the next pod hydrates the older durable snapshot OVER the local
// file. Silently. The fleet's answer is ship-before-ack: cloud.OrgStore.Sync
// fences the file at the ha lease round, and a write is acknowledged only after
// a ship that included it (apps/research and apps/books both do exactly this).
//
// A SHIP IS A WHOLE-FILE SNAPSHOT, SO ONE PER WRITE IS NOT AVAILABLE. The
// decision path answers authorisations; a file upload per decision is a plane
// that cannot serve. So the ships are COALESCED — group commit: at most one is
// in flight per tenant, and every caller waiting is released by a ship that
// BEGAN after its own write committed, which is what makes the ack honest. A
// thousand concurrent decisions cost one ship, not a thousand, and each of them
// still returns only once its own row is in the durable object.
//
// TWO BOUNDS, THE SAME SHAPE THE BENCH USES. One ship in flight per tenant, and
// a fleet-wide concurrency limit — so no arrangement of one tenant's traffic can
// spend another tenant's ability to ship, and the pod's outbound I/O is bounded
// however many tenants are writing.

import (
	"context"
	"database/sql"
	"sync"

	"github.com/zap-proto/zip"
)

// shipSlots is how many tenants may be shipping at once across the deployment.
// Four: a ship is network I/O rather than CPU, so it is not the bench's two, and
// it is still a bound — a pod with two hundred busy tenants ships four files at
// a time instead of two hundred.
const shipSlots = 4

// pier coalesces ship-before-ack.
//
// The counter is what makes the ack honest. A ship that is ALREADY RUNNING
// snapshotted the file before this caller's write committed, so waiting on it
// would acknowledge a record that is not in the object. Every caller therefore
// waits for the ship AFTER the one in flight — begun+1 — which is also the ship
// it starts itself when the berth is idle.
type pier struct {
	ship  func(Tenant) (bool, error)
	slots chan struct{}

	mu     sync.Mutex
	berths map[Tenant]*berth
}

// berth is one tenant's ship state.
type berth struct {
	// begun and done count ships STARTED and FINISHED. A waiter records
	// begun+1 and returns when done reaches it.
	begun, done uint64
	running     bool
	// wake is closed on every completion and replaced. Channels rather than a
	// sync.Cond because a waiter must be able to give up on its own context.
	wake chan struct{}
	// acked and err are the LAST finished ship's result, which is the result of
	// a ship at or past every released waiter's target.
	acked bool
	err   error
}

func newPier(ship func(Tenant) (bool, error)) *pier {
	return &pier{ship: ship, slots: make(chan struct{}, shipSlots), berths: map[Tenant]*berth{}}
}

func (p *pier) berth(t Tenant) *berth {
	b, ok := p.berths[t]
	if !ok {
		b = &berth{wake: make(chan struct{})}
		p.berths[t] = b
	}
	return b
}

// ack ships this tenant's file and returns once a ship that began after now has
// finished. acked is false when this replica is not (or is no longer) the org's
// elected writer, which the caller must NOT treat as recorded.
func (p *pier) ack(ctx context.Context, t Tenant) (bool, error) {
	p.mu.Lock()
	b := p.berth(t)
	target := b.begun + 1
	for {
		if b.done >= target {
			acked, err := b.acked, b.err
			p.mu.Unlock()
			return acked, err
		}
		if !b.running {
			b.running = true
			b.begun++
			go p.sail(t, b)
		}
		wake := b.wake
		p.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		p.mu.Lock()
	}
}

func (p *pier) sail(t Tenant, b *berth) {
	p.slots <- struct{}{}
	acked, err := p.ship(t)
	<-p.slots

	p.mu.Lock()
	b.done = b.begun
	b.acked, b.err, b.running = acked, err, false
	close(b.wake)
	b.wake = make(chan struct{})
	p.mu.Unlock()
}

// reset drops every berth. Called when the shelf closes, so a torn-down plane
// leaves no memory of what it had shipped.
func (p *pier) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.berths = map[Tenant]*berth{}
}

// ── the dirty check ─────────────────────────────────────────────────────────

// changed reads SQLite's own count of the rows this connection has inserted,
// updated or deleted since it was opened.
//
// It is what lets durability be a PROPERTY OF THE PLANE rather than a line
// somebody remembers to add to each write. The alternative — ship at every
// mutating call site — is every write op in this package today and one more with
// every op added, and the failure mode of forgetting one is a record that is
// acknowledged and not durable, which is exactly the defect this file exists to
// close. Asked here, the question "did this request write anything" is answered
// by the database rather than by a convention.
//
// cloud.OrgDB sets MaxOpenConns(1), so there is one connection and the count is
// monotone under it. A connection that is REPLACED — the promotion path swaps the
// whole handle when this replica becomes the org's writer — restarts the count,
// which reads as "different from the mark" and ships. The mark is therefore set
// to whatever was counted, never to the larger of the two: kept as a maximum, a
// restarted counter would never reach it again and every later request would
// ship a file with nothing in it to ship.
func changed(db *sql.DB) (int64, bool) {
	var n int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// ship makes every acknowledged write of this tenant's durable.
//
// It is a no-op when the tenant has written nothing since its last ship, which
// is what makes it affordable on the read path: BenchmarkKeepOnARead measures
// 2.1 us and 509 B, against BenchmarkOneRuleRead's 30.8 us and 12.2 kB for ONE
// of the several statements /v1/risk/decide already runs — under a fifteenth of
// one of them. On a deployment with no object store configured Sync acks
// trivially, so this whole path is the counter read and nothing else.
func (s *shelf) ship(ctx context.Context, t Tenant, db *sql.DB) (bool, error) {
	now, ok := changed(db)
	// A count that could not be read leaves "is there anything to ship" unknown,
	// and an unknown is shipped rather than skipped.
	if ok && now == s.mark(t) {
		return true, nil
	}
	acked, err := s.pier.ack(ctx, t)
	if err != nil || !acked {
		return acked, err
	}
	if ok {
		// The count was read BEFORE the ship, and ack waited for a ship that
		// BEGAN after that read — so every row counted here is in the object.
		// Two callers whose ships overlapped may write this out of order, which
		// costs at most one extra ship and can never skip one: a mark only ever
		// names a count some acknowledged ship carried.
		s.setMark(t, now)
	}
	return true, nil
}

// mark reads the row-change count at this tenant's last acknowledged ship, and
// -1 when it has never shipped — which no counter equals, so a tenant's first
// request always ships.
func (s *shelf) mark(t Tenant) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.marks[t]
	if !ok {
		return -1
	}
	return n
}

// setMark records what the last acknowledged ship carried. It ASSIGNS rather
// than taking the larger of the two: a connection that was replaced restarts the
// count, and a mark kept as a maximum would never be reached again — every later
// request would then ship a file with nothing in it to ship, forever.
func (s *shelf) setMark(t Tenant, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks[t] = n
}

// durable is the middleware that makes ship-before-ack a property of the
// surface rather than a step each op remembers.
//
// (It is not called keep: this package already uses that word for writing a
// model's learned state into the tenant's file — keepFit, keepAll, the stable's
// own keep — and one word means one thing.)
//
// It runs AFTER the leaf — fasthttp writes the response only once the whole
// chain has returned, so a ship here still precedes the acknowledgement on the
// wire — and it ships whenever the request changed the tenant's file, whatever
// the op was and whether or not the op knew it should. Installed on the group,
// it cannot be forgotten by the next op somebody adds, which a call at each
// write site can.
//
// A SHIP THAT IS NOT ACKED TURNS THE ANSWER INTO A REFUSAL. This replica is not
// the org's elected writer, or was deposed mid-request; the row is in a local
// file that the next hydrate will overwrite. Answering 200 there is the precise
// failure this exists to prevent, so the caller is told to retry rather than
// told it was recorded. A retried decide is the reason /v1/risk/decide takes an
// idempotency key: the key is claimed IN the insert, so the retry reads back the
// first answer rather than scoring a second time.
func durable(s *stateService) zip.Handler {
	return func(c *zip.Ctx) error {
		err := c.Continue()
		t, terr := tenantOf(c.Context(), s.State.brand)
		if terr != nil {
			return err // no validated tenant: nothing of anyone's was written
		}
		db, derr := s.State.shelf.open(t.tenant)
		if derr != nil {
			return err
		}
		acked, serr := s.State.shelf.ship(c.Context(), t.tenant, db)
		if serr != nil {
			s.Log.Error("risk: a tenant's records were not shipped",
				"tenant", t.tenant.String(), "err", serr)
			return zip.Errorf(503, "this record is written but not yet durable; retry")
		}
		if !acked {
			s.Log.Error("risk: a tenant's records were not acknowledged by the durable store; this replica does not own them",
				"tenant", t.tenant.String())
			return zip.Errorf(503, "this replica is not this org's writer; retry")
		}
		return err
	}
}
