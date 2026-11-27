package marketplace

// sweep.go — the clocks a job runs on, run by nobody in particular.
//
//	given an unpaid ending             that ending, once the rail returns the money
//	delivered past its review window   released: the buyer's held payment settles
//	                                   disputed: the payment no longer clears
//	open past its deadline             cancelled: the seller never took it on
//	accepted past its deadline         refunded:  the seller never delivered
//	disputed by the clock              released:  the payment clears again
//	disputed past the arbiter's time   refunded:  nobody ruled, and the clock's own
//	                                              dispute still does not clear
//	quoted or funding past quoteTTL    dropped:   nobody paid, or the rail never
//	                                              opened it; a payment presented
//	                                              for it is given back first
//
// Every step it takes is on the trail, by "marketplace", for both parties. A
// payment principal no longer clears is not made by a clock: the job goes to the
// arbiter, is paid by the clock once it clears again, and is refunded if it never
// does in the arbiter's time. A step the rail cannot take now — the rail down, the
// payment not clearing yet — leaves the job where it was and is tried again, after
// a backoff that doubles from a minute to a day, behind every job that has not
// failed, for as long as it takes: a job whose money is set aside is never left
// without a way to end.

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/zap-proto/zip"
)

const (
	// sweepEvery is how often the clocks are read.
	sweepEvery = time.Minute
	// sweepBatch bounds the jobs one pass takes on.
	sweepBatch = 500
)

// sweepLoop names the sweep's lease: one process in the deployment runs it.
const sweepLoop = "marketplace:sweep"

// sweep runs the clocks until ctx ends.
func (o marketOps) sweep(ctx context.Context) {
	lead := o.s.Coord.Lead(sweepLoop)
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		_ = lead.Do(ctx, o.sweepOnce)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// clock is the time a sweep reads: o.sweepAt when a test fixed it, else the wall clock.
func (o marketOps) clock() time.Time {
	if o.sweepAt != nil {
		return o.sweepAt()
	}
	return time.Now()
}

// sweepOnce is one pass. A failure for one job is logged and the next is tried.
//
// Each job is fenced before it is moved: a pass that outlives this process's hold on
// the sweep stops before its next payout or refund. A payout settles on the rail
// once whoever asks (the job's resource and its payment), and its state moves only
// from the status it was read in, so the fence keeps two pods from racing and the
// rail keeps one payment from paying twice.
func (o marketOps) sweepOnce(ctx context.Context) {
	if o.s.State.audit == nil {
		return
	}
	lead := o.s.Coord.Lead(sweepLoop)
	st := o.s.State.store
	now := o.clock().Unix()
	due, err := st.due(ctx, now)
	if err != nil {
		o.s.Log.Error("marketplace: sweep", "err", err)
		return
	}
	for _, j := range due {
		if lead.Hold(ctx) != nil {
			return
		}
		var err error
		switch {
		case j.ending != "":
			err = o.finish(ctx, j.ID)
		case slices.Contains(unopened, j.Status):
			err = o.lapse(ctx, j.ID)
		case j.Status == StatusDelivered:
			if _, err = o.pay(ctx, j, bySweep, StatusDelivered); errors.Is(err, errUncleared) {
				err = o.contest(ctx, j, err, now)
			}
		case j.Status == StatusOpen:
			_, err = o.unpaid(ctx, j, StatusCancelled, bySweep, map[string]any{"reason": "the deadline passed before the seller accepted"}, StatusOpen)
		case j.Status == StatusAccepted:
			_, err = o.unpaid(ctx, j, StatusRefunded, bySweep, map[string]any{"reason": "the deadline passed before delivery"}, StatusAccepted)
		case j.Status == StatusDisputed:
			err = o.rule(ctx, j, now)
		}
		if err != nil {
			o.s.Log.Warn("marketplace: a job's clock ran out and it did not move", "job", j.ID, "status", j.Status, "err", err)
			if lerr := st.later(ctx, j.ID, now); lerr != nil {
				o.s.Log.Error("marketplace: sweep", "job", j.ID, "err", lerr)
			}
		}
	}
}

// contest puts a delivered job whose payment no longer clears before the arbiter,
// as the clock: the seller was blocked, or owes its tax form, since the job was
// funded. The arbiter releases it once the payment clears again, or refunds it;
// the clock releases it the moment it clears (rule).
func (o marketOps) contest(ctx context.Context, j jobRow, why error, now int64) error {
	reason := "the payment no longer clears"
	if he, ok := errors.AsType[*zip.HTTPError](why); ok {
		reason += ": " + he.Msg
	}
	_, err := o.step(ctx, j, []string{StatusDelivered}, StatusDisputed, bySweep,
		map[string]any{"dispute_reason": clip(reason), "disputed_by": bySweep, "disputed_at": now, "contested": 1}, map[string]any{"reason": clip(reason)})
	return err
}

// rule is the clock's part in a dispute. One the clock raised itself, because the
// payment stopped clearing — known by the job's own mark, never by a name a party
// could also hold — is paid the moment it clears again — a seller that
// furnishes its tax form is paid for the work it delivered — and any dispute the
// arbiter has not ruled on in its time lapses to a refund: the clock's own only
// when it still does not clear, or can no longer be paid because the buyer's
// authorization expired.
func (o marketOps) rule(ctx context.Context, j jobRow, now int64) error {
	lapsed := j.Dispute != nil && j.Dispute.At+int64(disputeTimeout.Seconds()) < now
	reason := "the dispute lapsed without a ruling"
	if j.contested {
		_, err := o.pay(ctx, j, bySweep, StatusDisputed)
		if err == nil || !lapsed || !(errors.Is(err, errUncleared) || now > j.until) {
			return err
		}
		reason = "the payment was not made by the time the arbiter's time ran out"
	}
	if !lapsed {
		return nil
	}
	_, err := o.unpaid(ctx, j, StatusRefunded, bySweep, map[string]any{"reason": reason}, StatusDisputed)
	return err
}

// lapse drops a quote nobody opened. A payment presented for it is given up on the
// rail first — the job never opened, and whatever the payment set aside returns
// to the buyer — and a rail that does not answer keeps the quote for the clock to
// try again.
func (o marketOps) lapse(ctx context.Context, id string) error {
	release, err := o.hold(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	st := o.s.State.store
	j, found, err := st.job(ctx, id)
	if err != nil || !found || !slices.Contains(unopened, j.Status) {
		return err
	}
	if j.payment != "" {
		if _, err := o.void(j, j.payment); err != nil {
			return err
		}
	}
	return st.dropQuote(ctx, id)
}
