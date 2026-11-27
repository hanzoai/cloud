package agents

import (
	"context"
	"sync/atomic"

	"github.com/hanzoai/cloud/sdk"
)

// x402fee.go — a run fee paid per request over x402 (sdk/x402fee.go).
//
// The run fee is gated and debited inside runAgent, the one run path the HTTP
// handler, the scheduler, groups and the tool plane share, while only the HTTP
// handler holds a request that can carry a payment. So the handler checks the
// payment and hands the run a TICKET on its context, and runAgent settles it in
// place of the fee's debit once the run has succeeded — a run that fails is not
// billed, whichever way it would have been paid.
//
// A ticket pays for ONE run. The context it rides is inherited by everything the
// run starts — an agent the run calls as a tool, a group's members — and each of
// those is its own run with its own fee. So the first runAgent to ask takes the
// ticket, and every later one finds none and bills its own fee as it always has.

// feeTicketKey carries a checked run-fee payment to the run it pays for.
type feeTicketKey struct{}

// feeTicket is a run fee's payment, taken by the first run that asks.
type feeTicket struct {
	pay   *cloud.FeePayment
	taken atomic.Bool
}

// withFeePayment states that the next run on ctx is paid by pay.
func withFeePayment(ctx context.Context, pay *cloud.FeePayment) context.Context {
	return context.WithValue(ctx, feeTicketKey{}, &feeTicket{pay: pay})
}

// takeFeePayment is the payment ctx carries for the next run, taken: nil when it
// carries none or a run already took it.
func takeFeePayment(ctx context.Context) *cloud.FeePayment {
	t, _ := ctx.Value(feeTicketKey{}).(*feeTicket)
	if t == nil || !t.taken.CompareAndSwap(false, true) {
		return nil
	}
	return t.pay
}

// feeRefusal is a refusal by the run fee's balance gate. It is told apart from a
// refusal of the run's model spend (budget.go) because paying the fee clears only
// the first, so only the first carries the fee's x402 challenge. It is the gate's
// error in every other respect: same message, and it unwraps to it, so every
// renderer classifies it exactly as before.
type feeRefusal struct{ err error }

func (e *feeRefusal) Error() string { return e.err.Error() }
func (e *feeRefusal) Unwrap() error { return e.err }
