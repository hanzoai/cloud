package cloud

// Reservations — the money a call has COMMITTED but has not spent yet.
//
// A prepaid gate reads a SETTLED balance, and a completion's cost is not known
// until it finishes. Between those two facts sit the two ways an org spends money
// it does not have: the gate weighed only the prompt, so the completion was never
// covered; and N calls in flight each read the same balance before any of them
// debited, so each was authorized for the whole of it. A reservation closes both
// by making a commitment VISIBLE before the work runs and dropping it only when
// the debit reaches the ledger.
//
// It is per-POD state on purpose. apps/finance owns the settled truth and says so
// — "transient holds are the caller's in-pod concern, never persisted here" — so
// this never becomes a second ledger. Two pods hold their own commitments; each
// is conservative within itself and the ledger stays the one settled authority.

import "sync"

// commitments is the per-org total committed by calls that have not settled.
// The zero value is ready to use.
type commitments struct {
	mu    sync.Mutex
	cents map[string]int64
}

// commit adds cost to org's in-flight total and returns that TOTAL — the amount
// the balance must cover for this call to be affordable ALONGSIDE everything
// already committed.
//
// Returning the running sum rather than cost is the whole mechanism: the balance
// check that follows is handed a figure that already includes every other call in
// flight, so the second concurrent caller must clear both its own cost and the
// first one's. Nothing else has to know reservations exist.
func (c *commitments) commit(org string, cost int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cents == nil {
		c.cents = map[string]int64{}
	}
	c.cents[org] += cost
	return c.cents[org]
}

// drop removes cost from org's in-flight total, forgetting the org once nothing
// is outstanding so an idle deployment does not accumulate a row per tenant.
func (c *commitments) drop(org string, cost int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cents == nil {
		return
	}
	if rest := c.cents[org] - cost; rest > 0 {
		c.cents[org] = rest
		return
	}
	delete(c.cents, org)
}

// pending reports org's committed-but-unsettled total. For tests and diagnostics.
func (c *commitments) pending(org string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cents[org]
}

// hold is ONE call's commitment against an org's balance.
//
// Its release MUST run exactly once, and it must run on EVERY path — a hold that
// leaks is not a billing inaccuracy, it is a paying customer locked out of their
// own balance until the pod restarts. So release is idempotent (a caller may
// safely both defer it and hand it to the debit) and every exit from the metering
// path invokes it.
type hold struct {
	to   *commitments
	org  string
	cost int64
	once sync.Once
}

// release drops the commitment. Safe to call repeatedly, on a nil hold, and on
// the EMPTY hold a system call gets — an ungated call commits nothing, so there
// is nothing to give back, and that path must not be a panic on the one code
// route that runs inference without a customer behind it.
func (h *hold) release() {
	if h == nil || h.to == nil {
		return
	}
	h.once.Do(func() { h.to.drop(h.org, h.cost) })
}
