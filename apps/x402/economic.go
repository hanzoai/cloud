package x402

// economic.go — A SETTLEMENT IS AN ECONOMIC EVENT.
//
// Every settlement moves money from one org's balance to another's, and that
// payment is stated on the event plane — the one place both orgs' books, and the
// payer's tax year, are read from. There is no second ledger of what this rail
// settled: the table here is the rail's own record, and the event plane is what
// anybody else reads.
//
// THE RAIL IS THE ONLY ONE WHO SAYS WHAT IT MOVED. The event plane never takes a
// payment's content from whoever calls it: it asks this app, on this app's own
// socket, what one settlement records (planeEconomic), and states that. So the
// orgs, the amount and the time on every x402 event come from this table, and a
// process that is not this one cannot put an x402 payment into anybody's books.
//
// OWED IN THE SAME WRITE THAT SETTLES IT. A settled row that has not landed on the
// plane is an OUTBOX entry: `settled = 1 AND stated = 0`. markSettled is the one
// write that makes a settlement settled, and the same write makes the statement
// owed — there is no second write that could be lost between them. The settlement
// then answers the client at once; it never waits on the plane, so a plane that is
// down, slow or absent delays a statement and never a payment.
//
// DRAINED UNTIL IT LANDS. The drainer (announce) names each owed settlement to the
// plane, which answers whether both parties' rows are in the warehouse. Only that
// answer clears the entry: a statement the bus accepted but the warehouse never
// took — an outage longer than the stream keeps a message — is still owed here,
// and is stated again. The plane is idempotent on (rail, proof), so however many
// times one settlement is stated, it is one event. A failed or unlanded attempt
// waits longer each time (backoff), so an outage costs the plane a bounded trickle
// rather than a flood.
//
// EVERY FIELD COMES FROM THE ROW, so every statement of one settlement says the
// same thing: the payer and the payee orgs the settlement was claimed between, the
// agent that acted for the payer, the resource bought, the exact amount and the
// time it was accepted — this rail's own time, which is what orders it on the
// plane.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// emit is the ONE plane call, held in a variable so a test can read what leaves
// this process without an event app to receive it. Nothing else writes it.
var emit = eventpeer.EventEconomic

// Answers what one completed settlement records, as the economic event the plane
// states — to the org that paid, which is who this rail states it for, and only
// once its money has moved. The answer is the whole record whoever the plane is
// stating it for; which party's copy leaves out what is the plane's to decide.
func (r rail) planeEconomic(ctx context.Context, in *client.EconomicNotice) (*client.EconomicEvent, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("x402: no org on the call")
	}
	if in.Rail != client.RailX402 {
		return nil, zip.ErrBadRequest("x402: rail " + in.Rail + " is not this one")
	}
	st, found, err := r.s.State.store.get(ctx, in.Proof)
	switch {
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "x402: %v", err)
	case !found || org != st.PayerOrg:
		return nil, zip.ErrNotFound("x402: " + org + " paid no settlement " + in.Proof)
	case st.PayerOrg == st.PayeeOrg:
		return nil, zip.ErrNotFound("x402: settlement " + in.Proof + " moved money between one org's own books")
	case !st.Settled:
		return nil, zip.Errorf(http.StatusConflict, "x402: settlement %s has not completed", in.Proof)
	}
	return economic(st)
}

// economic is a settlement as the economic event it is.
func economic(st *Settlement) (*client.EconomicEvent, error) {
	amount, err := money.ParseInt(st.Amount)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "x402: settlement %s: %v", st.ID, err)
	}
	e := &client.EconomicEvent{
		Buyer: st.PayerOrg, Seller: st.PayeeOrg, Agent: st.Agent, Service: st.Resource,
		Currency: "USD", Gross: amount.String(), FMV: amount.String(),
		Rail: client.RailX402, Proof: st.ID, At: st.CreatedAt, Category: st.Category,
	}
	if st.TxHash != "" {
		e.Chain, e.Tx = st.Network, st.TxHash
	}
	return e, nil
}

// Drain bounds.
const (
	// drainEvery is how often the drainer looks for owed statements when nothing
	// has asked it to sooner.
	drainEvery = 30 * time.Second
	// drainBatch is the most statements one pass names.
	drainBatch = 100
	// backoffFirst and backoffMost bound how long an owed statement waits after an
	// attempt that did not land it: doubling from the first to the most, which is
	// well inside the stream's own age bound, so an outage costs each owed
	// statement a few attempts a day.
	backoffFirst = 30 * time.Second
	backoffMost  = 6 * time.Hour
)

// backoff is how long an owed statement waits after its tries-th attempt.
func backoff(tries int64) time.Duration {
	if tries >= 20 {
		return backoffMost
	}
	return min(backoffFirst<<tries, backoffMost)
}

// Announce names every owed statement that is due to the event plane, and answers
// how many have now landed. It is what the drainer runs; it is safe to run at any
// time and any number of times.
func Announce(ctx context.Context) (int, error) {
	s := live
	if s == nil {
		return 0, nil
	}
	landed, _, err := announce(ctx, s)
	return landed, err
}

// announce is one pass: at most drainBatch owed statements, and whether that was
// all of them.
func announce(ctx context.Context, s *cloud.Service[state]) (landed int, more bool, err error) {
	now := time.Now().Unix()
	owed, err := s.State.store.owed(ctx, now, drainBatch)
	if err != nil {
		return 0, false, err
	}
	more = len(owed) == drainBatch
	for _, o := range owed {
		pctx, cancel := context.WithTimeout(cloud.For(ctx, o.PayerOrg), peerCallTimeout)
		out, err := emit(pctx, &client.EconomicNotice{Rail: client.RailX402, Proof: o.ID})
		cancel()
		if err == nil && out != nil && out.Landed {
			if err := s.State.store.landed(ctx, o.ID); err != nil {
				return landed, false, err
			}
			landed++
			continue
		}
		if err != nil {
			s.Log.Warn("x402: settlement not yet stated on the event plane — owed, retrying",
				"id", o.ID, "tries", o.Tries+1, "err", err)
		}
		if err := s.State.store.retry(ctx, o.ID, o.Tries+1, now+int64(backoff(o.Tries).Seconds())); err != nil {
			return landed, false, err
		}
	}
	return landed, more, nil
}

// drainer is the process's one loop over the outbox. kick asks it to run now
// rather than at its next tick; stop ends it and waits for the pass in flight.
type drainer struct {
	kick chan struct{}
	stop context.CancelFunc
	done sync.WaitGroup
}

// drain starts the loop for s.
func drain(s *cloud.Service[state]) *drainer {
	ctx, cancel := context.WithCancel(context.Background())
	d := &drainer{kick: make(chan struct{}, 1), stop: cancel}
	d.done.Add(1)
	lead := s.Coord.Lead("x402:announce")
	s.Go("x402.announce", nil, func() {
		defer d.done.Done()
		t := time.NewTicker(drainEvery)
		defer t.Stop()
		for {
			var more bool
			lead.Do(ctx, func(ctx context.Context) {
				var err error
				_, more, err = announce(ctx, s)
				if err != nil && !errors.Is(err, context.Canceled) {
					s.Log.Error("x402: drain the event plane outbox", "err", err)
				}
			})
			if more && ctx.Err() == nil {
				continue // a full batch: the backlog behind it is due now too
			}
			select {
			case <-ctx.Done():
				return
			case <-d.kick:
			case <-t.C:
			}
		}
	})
	return d
}

// nudge asks the drainer to run now. It never blocks: a pass already asked for
// will see this settlement too.
func (d *drainer) nudge() {
	if d == nil {
		return
	}
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// close stops the loop and waits for it.
func (d *drainer) close() {
	if d == nil {
		return
	}
	d.stop()
	d.done.Wait()
}

// owedStatement is one settled payment the plane has not yet landed.
type owedStatement struct {
	ID       string
	PayerOrg string
	Tries    int64
}
