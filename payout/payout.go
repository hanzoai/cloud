package payout

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
)

// Commerce is the ONE thing an attributed-credit program asks of the money plane,
// and it is a QUESTION: what has this org spent? Client below is the ONE production
// binding. There is no write method, by design.
type Commerce interface {
	// SpendCents is the org's month-to-date metered consumption — the qualify
	// signal / commission accrual base (spend × the program's rate).
	SpendCents(ctx context.Context, org string) (int64, error)
}

// ErrNoLedger reports that this deployment runs no commerce at all, so there is
// no spend to read and none to accrue against.
//
// It is the ONLY absence a program may act on, and it is the ROUTER'S word
// (cloud.ErrNoPeer), never an inference from a failed call. The predicate it
// replaces asked whether a base URL and a token were set — two things that do not
// exist for a peer reached by name, and which were unset in every process but one,
// which is how "no commerce here" and "commerce is right there and I cannot spell
// its address" became the same silent zero.
var ErrNoLedger = errors.New("payout: this deployment runs no commerce")

// Client is the production commerce binding: the ledger, asked by name over the
// internal plane. It holds no address and no credential, because reaching a peer
// takes neither.
type Client struct{}

// NewClient builds the production binding. It takes no arguments: a peer is
// reached through zip.SocketPath(name), so there was never an address to supply.
func NewClient() *Client { return &Client{} }

// SpendCents reads the org's month-to-date metered consumption from the process
// that owns the ledger.
//
// The ORG rides the call — cloud.For states the tenant for a read with no request
// behind it — because a caller that could name the org in an argument could accrue
// a commission against another tenant's spend. There is no `user`: the figure was
// always the org's, and the rollup's `user` parameter was the org's own name sent
// back to it.
//
// ABSENCE IS THE ROUTER'S WORD. cloud.ErrNoPeer becomes ErrNoLedger, and only that
// means there is honestly nothing to read. Every other error is an OUTAGE and is
// returned as one: a dead ledger read as a zero is a program that quietly stops
// paying people.
func (c *Client) SpendCents(ctx context.Context, org string) (int64, error) {
	spend, err := commercepeer.FinanceSpend(cloud.For(ctx, org), &client.SpendIn{})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			return 0, ErrNoLedger
		}
		return 0, fmt.Errorf("payout: commerce spend read: %w", err)
	}
	if spend == nil {
		// A void reply is not a zero month. Nothing was read, so nothing is known.
		return 0, errors.New("payout: commerce answered nothing")
	}
	// A month's consumption is a figure someone reads and a rate is applied to.
	// Per-token debits are routinely finer than a cent, so the rounding is
	// explicit here rather than an exactness guard turning a real ledger into an
	// error.
	// What a payout accrues on is what cash paid for: a share of money a customer
	// paid, never of credit Hanzo minted.
	return spend.Paid.RoundMinor()
}
