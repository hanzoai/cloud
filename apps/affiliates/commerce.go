package affiliates

import (
	"context"

	"github.com/hanzoai/cloud/apps/payout"
)

// commerce is the ONE thing the commission loop asks of the money plane, and it is a
// QUESTION, not an instruction: what has this org spent? That read is the accrual
// base. It is an INTERFACE so the sweep is testable against a fake.
//
// THERE IS NO DEPOSIT HERE, AND THERE IS NOT GOING TO BE ONE. This seam used to
// carry `deposit`, which is how a GET on this surface came to mint platform credit:
// the capability existed, so a caller eventually reached it. An affiliate commission is a PAYABLE —
// accrued and recorded here, settled by a human out of band — and platform credit is
// issued only by an admin grant. Re-adding a write method here re-opens exactly the
// hole that was shut, so the SHAPE of this interface is load-bearing and
// TestCommerceSeamIsReadOnly fails if it ever grows one.
type commerce interface {
	configured() bool
	spendCents(ctx context.Context, org, user string) (int64, error)
}

// errUnconfigured is the shared sentinel a read against an unwired commerce returns,
// so accrual stays honestly pending rather than silently earning.
var errUnconfigured = payout.ErrUnconfigured

// commerceSeam adapts the shared payout.Client onto this program's lowercase seam
// (Go package-scoped interface methods cannot cross packages). Zero logic — pure
// delegation, and it delegates exactly one read.
type commerceSeam struct{ c *payout.Client }

func (s commerceSeam) configured() bool { return s.c.Configured() }
func (s commerceSeam) spendCents(ctx context.Context, org, user string) (int64, error) {
	return s.c.SpendCents(ctx, org, user)
}

// newCommerceClient builds the production binding, delegating to clients/payout.
func newCommerceClient(base, token string) commerce {
	return commerceSeam{payout.NewClient(base, token)}
}
