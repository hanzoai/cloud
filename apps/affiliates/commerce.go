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
	spendCents(ctx context.Context, org string) (int64, error)
}

// commerceSeam adapts the shared payout.Client onto this program's lowercase seam
// (Go package-scoped interface methods cannot cross packages). Zero logic — pure
// delegation, and it delegates exactly one read.
type commerceSeam struct{ c *payout.Client }

func (s commerceSeam) spendCents(ctx context.Context, org string) (int64, error) {
	return s.c.SpendCents(ctx, org)
}

// newCommerceClient builds the production binding, delegating to apps/payout. It
// takes no address: a peer is reached by NAME.
func newCommerceClient() commerce {
	return commerceSeam{payout.NewClient()}
}
