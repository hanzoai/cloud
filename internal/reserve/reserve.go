package reserve

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
// IT IS THE DEPLOYMENT'S, NOT THE POD'S. apps/finance owns the settled truth, and a hold is
// never written to it: "transient holds are the caller's concern, never persisted here",
// so this never becomes a second ledger. But the caller is the deployment, not a pod. A
// pod that weighed a balance against its own holds alone let three pods each authorize
// the whole of it, so the holds are summed in the KV every pod shares (coord.Commit), and
// a hold a pod never gave back (it died) lapses with its ttl rather than locking the
// payer out until something restarts. A KV that cannot be asked leaves each pod its own
// holds, which is what the pod had before and no worse.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
)

// reserveFor is how long a hold stands without being given back. The longest a request
// runs is fifteen minutes (the plugin response ceiling), so a hold past this belongs to a
// pod that is gone.
const Lapse = 16 * time.Minute

// commitments is the total committed, per org, by calls that have not settled. The zero
// value is ready to use. It counts in the deployment's Coord when the pods share a KV, and
// in a count of its own when they do not (one pod, a laptop, a test): what each Meter
// holds is then its own, as it always was, and not every Meter's in the process.
type Commitments struct {
	once sync.Once
	Pods *coord.Coord // set before first use to name the deployment's
}

func (c *Commitments) coord() *coord.Coord {
	c.once.Do(func() {
		if c.Pods == nil {
			if d := coord.Default(); d.Shared() {
				c.Pods = d
			} else {
				c.Pods = coord.Alone()
			}
		}
	})
	return c.Pods
}

// commit holds cost against org as hold id, and returns that TOTAL: the amount the
// balance must cover for this call to be affordable ALONGSIDE everything already
// committed, by this pod and every other.
//
// Returning the running sum rather than cost is the whole mechanism: the balance
// check that follows is handed a figure that already includes every other call in
// flight, so the second concurrent caller must clear both its own cost and the
// first one's. Nothing else has to know reservations exist.
func (c *Commitments) commit(ctx context.Context, org, id string, cost int64) int64 {
	return c.coord().Commit(ctx, "reserve", org, id, cost, Lapse)
}

// drop gives hold id back. The context is a fresh one: a hold is given back on the way
// out of a request that may be over, and must not be left standing because it ended.
func (c *Commitments) drop(org, id string, cost int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.coord().Uncommit(ctx, "reserve", org, id, cost)
}

// pending reports org's committed-but-unsettled total. For tests and diagnostics.
func (c *Commitments) Pending(org string) int64 {
	return c.coord().Committed(context.Background(), "reserve", org)
}

// hold is ONE call's commitment against an org's balance.
//
// Its release MUST run exactly once, and it must run on EVERY path — a hold that
// leaks is not a billing inaccuracy, it is a paying customer locked out of their
// own balance until the hold lapses. So release is idempotent (a caller may
// safely both defer it and hand it to the debit) and every exit from the metering
// path invokes it.
type Hold struct {
	to   *Commitments
	org  string
	cost int64
	id   string
	once sync.Once
}

// holdOf is a hold of cost against org, under an id of its own.
func HoldOf(to *Commitments, org string, cost int64) *Hold {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return &Hold{to: to, org: org, cost: cost, id: hex.EncodeToString(id)}
}

// commit takes the hold and answers the total held against its org.
func (h *Hold) Commit(ctx context.Context) int64 { return h.to.commit(ctx, h.org, h.id, h.cost) }

// release drops the commitment. Safe to call repeatedly, on a nil hold, and on
// the EMPTY hold a system call gets — an ungated call commits nothing, so there
// is nothing to give back, and that path must not be a panic on the one code
// route that runs inference without a customer behind it.
func (h *Hold) Release() {
	if h == nil || h.to == nil {
		return
	}
	h.once.Do(func() { h.to.drop(h.org, h.id, h.cost) })
}
