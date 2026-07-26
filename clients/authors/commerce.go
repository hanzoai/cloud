package authors

import (
	"context"

	"github.com/hanzoai/cloud/clients/payout"
)

// commerce is the narrow money seam the author royalty loop needs: read a deploying
// org's metered spend (the royalty accrual base) and grant a promo credit to a wallet
// (a payout made in credits, ledger tag grant:author). It is an INTERFACE so the
// store/handler logic is testable with a fake ledger; the production binding is
// clients/payout, reached through the thin adapter below.
//
// The S2S impl (COMMERCE_SERVICE_TOKEN path, X-Org-Id=<org> namespace, bare-org
// `user` subject) was three byte-identical commerce.go copies; it now lives ONCE in
// clients/payout. An author payout-in-credits still lands in precisely the wallet the
// balance panel reads, indistinguishable from an admin grant except by its
// grant:author tag.
type commerce interface {
	configured() bool
	deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (txnID string, err error)
	spendCents(ctx context.Context, org, user string) (int64, error)
}

// errUnconfigured is the shared sentinel a deposit against an unwired commerce
// returns, so the caller records an honest failure rather than a phantom payout.
var errUnconfigured = payout.ErrUnconfigured

// commerceSeam adapts the shared payout.Client onto this program's lowercase seam
// (Go package-scoped interface methods cannot cross packages). Zero logic — pure
// delegation; the money path lives in clients/payout.
type commerceSeam struct{ c *payout.Client }

func (s commerceSeam) configured() bool { return s.c.Configured() }
func (s commerceSeam) deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (string, error) {
	return s.c.Deposit(ctx, org, user, amountCents, currency, notes, tags)
}
func (s commerceSeam) spendCents(ctx context.Context, org, user string) (int64, error) {
	return s.c.SpendCents(ctx, org, user)
}

// newCommerceClient builds the production binding, delegating to clients/payout.
func newCommerceClient(base, token string) commerce {
	return commerceSeam{payout.NewClient(base, token)}
}
