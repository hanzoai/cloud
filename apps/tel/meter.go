package tel

// meter.go — who pays the carrier.
//
// Three acts on this surface buy something from a telephony carrier at a price
// the carrier sets: ordering a number (a one-time charge plus a monthly rental),
// sending a message (per message, and more for media), and placing a call (per
// minute). All three were served with no gate and no debit, so an authenticated
// tenant at zero balance could order numbers and send traffic with the platform
// paying the bill. That is not uncollected revenue, it is an unbounded spend
// surface — the gate matters here more than the charge does.
//
// THE STUB BUYS NOTHING. A deployment with no carrier credential serves the whole
// surface against an in-process stub (tel.go), which is what makes the app
// runnable in a sandbox and in the suite. Nothing is bought there, so nothing is
// gated and nothing is billed — the same rule the other metered surfaces keep:
// the credential is what makes an act paid.
//
// THE UNIT IS THE ACT WE CAN SEE. A number order and a message are each one act,
// and the fee matches. A CALL is priced per placement rather than per minute, and
// that is a statement about this client rather than a rounding of the carrier's
// bill: the completion callback carries the caller's own webhook URL (tel.go's
// callInput.Webhook is forwarded to the carrier verbatim), so the duration is
// delivered to the customer and never to this process. There is no minute here to
// meter. Pricing a placement is the honest unit for what this binary observes.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// feeEnv is the operator knob: TEL_FEE_CENTS_NUMBER / _MESSAGE / _CALL per act,
// TEL_FEE_CENTS for all three.
const feeEnv = "TEL_FEE_CENTS"

// The three billed acts, named once so the gate, the debit and the ledger row all
// say the same word.
const (
	number  = "number"
	message = "message"
	call    = "call"
)

// price is what each act costs by default, in cents.
//
// Policy defaults sized against the carrier's own wholesale rates, not claims
// about market value. A number is the platform's ordinary provision fee — it IS
// provisioning a resource, and it carries a recurring rental behind it, so it is
// the one act here priced like every other create in the fleet. A message and a
// placement are single-digit-cent wholesale acts, so a cent covers either with
// room. Operators move any of them with the knobs above; 0 makes an act free
// again, and un-gated with it (ResourceMeter.Gate's own costCents<=0 rule).
var price = map[string]int64{
	number:  cloud.DefaultResourceFeeCents,
	message: 1,
	call:    1,
}

func fee(act string) int64 { return cloud.FeeCents(feeEnv, act, price[act]) }

// afford authorizes one act BEFORE the carrier is asked, so a caller who cannot
// cover it is refused having spent nothing — the property this surface was
// missing entirely. A deployment on the stub buys nothing and is never gated.
func (o ops) afford(ctx context.Context, act string) (*cloud.Charge, error) {
	if !o.s.State.live {
		return nil, nil
	}
	return o.s.Bill.Allow(ctx, cloud.PayerOf(ctx), act, fee(act))
}

// charge debits one act, after the carrier has confirmed it. Failed work is not
// billed: every caller of this returns the carrier's own error instead, and a
// refused order or an undelivered message bought nothing.
func (o ops) charge(ch *cloud.Charge, act string) {
	// Ref is left unset so the meter mints one: it names an ACT, and two messages
	// are two acts. Keyed on the number, a second month's rental would be free.
	ch.Debit(metering.Usage{Model: act, AmountCents: fee(act)})
}
