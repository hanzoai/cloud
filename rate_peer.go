// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/plane/commerce"
	luxlog "github.com/luxfi/log"
)

// What one unit of metered work costs, asked of the process that owns the price.
//
// The authority is commerce's, because the charges land in commerce's ledger and
// an operator edits it at admin.hanzo.ai. The apps that meter — storage,
// translation, screening — are separate processes and cannot open it, so they
// ask, exactly as the spend gate does one file over.
//
// It replaces the same eight lines copied into three apps: read an env var,
// parse it, fall back to a compiled constant. That shape has no history, so
// nothing could answer what we charged in March, and three copies of one rule is
// three chances for them to disagree.

// rateCallTimeout bounds the ask. It is short because a price is not worth
// waiting on: the floor below is a real number that was charging yesterday, and
// serving it is strictly better than making a customer wait on a lookup.
var rateCallTimeout = 2 * time.Second

// RateNano is the published price of one unit of product/meter, in nano-dollars,
// or floor when the platform has not published one.
//
// THE FLOOR IS A PRICE, NOT AN ERROR PATH. Every caller's floor is the constant
// it charged before this existed, so a meter with no row, an authority that is
// unwell, and a process with no commerce beside it all keep charging exactly
// what they charged yesterday. The alternative — failing the work — would stop a
// customer's storage provisioning over a missing price row, which is a worse
// outcome than a price that is briefly one version old.
//
// A zero rate IS published if a row says zero: something the platform meters and
// gives away. That is why the answer carries Found rather than leaving a reader
// to guess from the number, which cannot tell nothing-published from free.
func RateNano(ctx context.Context, product, meter string, floor int64) int64 {
	ctx, cancel := context.WithTimeout(ctx, rateCallTimeout)
	defer cancel()

	out, err := commerce.BillingRate(ctx, &plane.RateIn{Product: product, Meter: meter})
	switch {
	case err != nil:
		// The authority is unreachable or unwell. Said once, at warn, because it
		// is a price that is stale rather than money that went missing — and it
		// is per metered act, so an outage must not write a line per charge at
		// error level.
		luxlog.New("cloud").New("subsystem", "rate").Warn(
			"price unreadable, charging the compiled floor",
			"product", product, "meter", meter, "floor_nano", floor, "err", err)
		return floor
	case out == nil || !out.Found:
		// Nothing published. The ordinary state of a meter nobody has priced yet,
		// so it is not worth a line: the floor IS the price until someone sets one.
		return floor
	default:
		return out.Nano
	}
}
