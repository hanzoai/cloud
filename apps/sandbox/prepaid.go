package sandbox

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/client"
	allowancepeer "github.com/hanzoai/cloud/client/allowance"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// prepaid.go is the question asked BEFORE a sandbox runs: can this wallet pay for
// it? A lease was taken, a pod was started and the meter billed afterwards, so an
// org with an empty balance could hold compute and disk for as long as the reaper
// let it and the debits simply went negative. Metering is not billing; a meter
// says what was spent, and nothing was refusing to spend it.
//
// FAIL CLOSED, INCLUDING WHEN THE LEDGER CANNOT BE ASKED. A gate that admits on
// its own failure is not a prepaid gate — it is a prepaid gate with an outage
// shaped hole, and the hole opens exactly when the finance plane is unwell, which
// is when it matters. So a balance that cannot be read refuses.
//
// THE READ GOES OVER THE PLANE, NEVER OUR OWN EDGE. apps/sandbox is its own
// process, so cloud.BalanceReader() — a package-level var the CLOUD process sets —
// is always nil here. apps/ai made this exact mistake one process over: it fell
// back to an HTTP self-call to /v1/billing/balance, which carried a service token
// and no user, was refused at the edge as an unvalidated principal, and took every
// completion down with it on a perfectly healthy ledger. The ledger has one writer
// and must be ASKED, over the socket, the way apps/admin/finance and
// apps/marketplace already ask it.
//
// WHAT IT COSTS IS ANSWERED FIRST. quote() prices the lease the caller is asking
// for — the one-shot fee plus the first hour of runtime and disk — so the refusal
// can say the number rather than "insufficient funds", and so a caller who cannot
// afford an android lease is told before a pod is pulled rather than after.

// balanceCents is how this package asks what a wallet holds, in whole cents,
// rounded DOWN. A var so a test can answer without a finance plane.
//
// Rounding down is the safe direction for a fail-closed gate: it can only refuse
// slightly early, where rounding up would admit a lease the balance cannot cover
// and leave the exact debit that follows to land as a negative nobody authorized.
var balanceCents = func(ctx context.Context, org, payer string) (int64, error) {
	if f := cloud.BalanceReader(); f != nil {
		// Co-resident with the cloud process: the direct read is strictly better.
		return f(ctx, payer, org, "usd")
	}
	bal, err := commercepeer.FinanceBalance(cloud.For(ctx, org), &client.BalanceIn{Subject: payer, Currency: "usd"})
	if err != nil {
		return 0, fmt.Errorf("plane balance read: %w", err)
	}
	if bal == nil {
		return 0, fmt.Errorf("plane balance read: commerce answered nothing")
	}
	cents, err := bal.Amount.FloorMinor()
	if err != nil {
		return 0, fmt.Errorf("plane balance read: %w", err)
	}
	return cents, nil
}

// ledgerAllows asks the org's ledger whether payer may spend cents on a lease: the
// wallet covers it AND the org's budgets (its enforced spend caps, the console's
// Budgets page) have room for it. It is the one ledger gate every priced act in
// the fleet asks (cloud.Meter.Authorize), and its refusals are the ledger's own
// errors, which the app renders as the billing 402s. A var so a test can answer as
// the ledger would without a money plane.
var ledgerAllows = func(s *Service, ctx context.Context, payer account.Account, cents int64) error {
	return s.Bill.Authorize(ctx, payer, "", false, "sandbox", cents)
}

// budgeted asks the org's ledger, before the pod, whether a paid lease fits inside
// the org's budget, at what its first hour costs (quote): the same figure afford
// weighs the wallet at, so a budget with less room than one hour refuses the lease
// rather than admitting it and overrunning by that hour.
//
// THE WALLET IS NOT THE BUDGET. afford reads the balance and nothing else, and the
// ledger was asked only for the one-shot fee, which is 0 unless somebody prices
// it. So an org past the budget it set could keep starting compute for as long as
// its wallet held money, and the only stop left was the provider's account limit,
// which halts every org on a shared account at once. The ledger is the routine
// cap; a provider's limit is an emergency breaker (supply.Exhausted, #805).
//
// A free lease (no payer) bills nobody and spends nothing against a budget, so it
// is not asked. A refusal is the ledger's own, classified once (cloud.Denied): a
// spent budget is 402 spend_cap_exceeded naming the page that raises it, and a
// ledger that cannot answer refuses with a 503 (fail closed), exactly as every
// other priced act's gate does. It is returned CLASSIFIED rather than as the bare
// sentinel because a lease also arrives over the plane (a coding run's), where an
// error with no status crosses as a 500 and the run could not tell a budget from a
// fault.
func budgeted(s *Service, ctx context.Context, admitted terms, class, disk string) error {
	if admitted.payer.Zero() {
		return nil
	}
	if err := ledgerAllows(s, ctx, admitted.payer, quote(ctx, class, disk)); err != nil {
		return cloud.Denied(err)
	}
	return nil
}

// quote is what one hour of this lease costs, in cents: the one-shot lease fee,
// plus the runtime hour of its class, plus the disk it will hold — disk is its
// size (diskSize), "" for none.
//
// ONE HOUR rather than the whole lease, because a lease has no total — it is
// metered until somebody ends it. An hour is the smallest span the meters bill in,
// so it is the smallest honest floor to admit against: a caller who cannot cover
// one hour cannot be allowed to start something that bills by the hour.
func quote(ctx context.Context, class, disk string) int64 {
	return ResourceFee(class) + hourly(ctx, class, disk)
}

// hourly is what holding this lease for one more hour costs, in cents: the runtime
// hour of its class plus the disk it holds, without the one-shot lease fee. It is
// the floor a running lease is carried on (afloat), as quote is the floor it was
// admitted on.
func hourly(ctx context.Context, class, disk string) int64 {
	micros := rate(ctx, class)
	if disk != "" {
		micros += storageRate(ctx) * gib(disk)
	}
	// micro-USD to cents, rounded UP: a price that rounded down would admit a
	// balance a cent short of the hour.
	return (micros + 9_999) / 10_000
}

// terms is what a lease was admitted on: the wallet that pays for it, and how long
// it may be held.
type terms struct {
	// payer is the wallet the lease's meters debit. Zero for a free lease, which the
	// meters bill to nobody: cloud.RuntimeCharge charges no row without a payer.
	payer account.Account
	// ttl caps the lease in seconds; 0 leaves the class default.
	ttl int
}

// freeTTL is the longest a free lease is held, in seconds. A free lease is not
// carried (reap.go): an hour is what the free lane grants, and it ends any free
// pod that nobody ended. A paid lease runs for as long as its wallet covers the
// next hour.
const freeTTL = 3600

// freeLease takes one lease from the payer's free sandbox allowance and answers the
// standing that follows. A var so a test can answer without the allowance process.
var freeLease = func(ctx context.Context, org, payer string) (*client.Allowance, error) {
	return allowancepeer.AllowanceTake(cloud.For(ctx, org), &client.AllowanceIn{Subject: payer, Meter: client.AllowanceSandbox})
}

// planAllows reports whether the payer's plan covers this lease whatever the
// wallet holds — the Unlimited plan, whose premium/ours allowances are
// flags.Unbounded (apps/flags/carried.go). It reads the same tier commerce
// derives for the payer's subject, so no two opinions about who is Unlimited.
// A tier that cannot be read answers not-allowed: fail-closed.
var planAllows = func(ctx context.Context, org string, payer account.Account) bool {
	return cloud.TierAllows(ctx, org, payer)
}

// afford answers what a lease is admitted on, or refuses it.
//
// A wallet that covers one hour of the lease pays for it. The payer is the account
// cloud.PayerOf resolved, not the org: a platform SuperAdmin leasing inside
// somebody else's org spends their own wallet, a person in the signup org spends
// theirs and never the platform pool beside them, and the gate has to read the
// wallet the debit will land in — in that wallet's own books — or spend can outrun
// the balance that admitted it.
//
// A WALLET THAT CANNOT IS THE FREE LANE'S, the way a zero-priced model call is the
// free pool's: the payer's plan grants a count of free leases a day (apps/allowance,
// the sandbox meter), and a lease taken on one bills nobody and is held at most
// freeTTL. Only a lease that keeps nothing past its end is free — a scratch class
// with no project disk — because a project disk outlives the lease and would be
// storage nobody pays for. The count is asked, not read: the allowance store has one
// writer, and it fails closed like the balance does.
func afford(ctx context.Context, payer account.Account, class, project, disk string) (terms, error) {
	if payer.Zero() {
		// No wallet is named, so nothing can be charged and nothing may be spent.
		// This is the same refusal cloud.RuntimeCharge already makes of an unpayable
		// row, moved to before the pod instead of after.
		return terms{}, zip.Errorf(http.StatusPaymentRequired, "this lease names no wallet, so it cannot be paid for")
	}
	want := quote(ctx, class, disk)
	have, err := balanceCents(ctx, payer.Org(), payer.Subject())
	if err != nil {
		return terms{}, zip.Errorf(http.StatusServiceUnavailable,
			"the balance could not be read, and a lease is not started on an unknown balance: %v", err)
	}
	// THE PLAN PAYS BEFORE THE WALLET IS ASKED ABOUT A SHORTFALL. An Unlimited
	// payer (a class saved flags.Unbounded) covers the lease whatever the wallet
	// holds — $0 included — and this lane never consumes the free-sandbox
	// allowance, which stays for a plan that is not Unlimited.
	// planAllows is a var so a test can answer without the tier plane.
	if have >= want || planAllows(ctx, payer.Org(), payer) {
		return terms{payer: payer}, nil
	}
	short := fmt.Sprintf("a %s lease costs %d cents for its first hour and this wallet holds %d", class, want, have)
	if project != "" || !classes[class].scratch() {
		return terms{}, zip.Errorf(http.StatusPaymentRequired, "%s", short)
	}
	a, err := freeLease(ctx, payer.Org(), payer.Subject())
	if err != nil {
		return terms{}, zip.Errorf(http.StatusServiceUnavailable,
			"%s, and the free sandbox allowance could not be read: %v", short, err)
	}
	if a == nil || a.Spent {
		spent := "its free sandboxes are used"
		if a != nil && a.Limit > 0 {
			spent = fmt.Sprintf("its %d free sandboxes this %s are used", a.Limit, a.Window)
		}
		return terms{}, zip.Errorf(http.StatusPaymentRequired, "%s, and %s; top up to lease more", short, spent)
	}
	return terms{ttl: freeTTL}, nil
}

// lifetime is the TTL a lease gets: the one asked for, else the class default, and
// never more than its terms allow.
func lifetime(asked int, class string, t terms) int {
	if asked <= 0 {
		asked = classes[class].ttl
	}
	if t.ttl > 0 && asked > t.ttl {
		return t.ttl
	}
	return asked
}
