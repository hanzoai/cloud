// Package bucket is the ONE place that classifies commerce ledger transactions
// into the three money buckets the console + GPU policy read, and derives the
// per-bucket balances from the REAL tagged ledger — never a fabricated split.
//
// The buckets (see the spend policy in Split):
//
//   - Credit (granted)  — promotional/starter grants, DO-backed, NON-cash. A
//     Deposit tagged "starter-credit" (billing/credit) or "included-credit:YYYY-MM"
//     (billing/allotment) or a "credit:"/"grant:" prefix. Usable for non-GPU
//     compute; NEVER usable for GPUs.
//   - Prepaid (real money) — funds the customer deposited via card top-up
//     (deposit.go/topup.go: a Deposit tagged "topup", "husd", or a bare/other
//     deposit). Real money. Usable for metered usage AND the ONLY bucket GPUs
//     may draw from.
//   - Card — a payment method on file (square_cardonfile). Not a ledger balance;
//     surfaced separately (bucket only reports whether GPU spend is card-gated).
//
// DECOMPLECTED (Hickey): this file is PURE money math over a slice of
// transactions — no datastore, no gin, no I/O. WHO reads the ledger
// (models/transaction/util) and WHO displays it (api/billing, the console) are
// separate concerns. The split is a projection of the same transactions the
// balance the gateway debits is computed from, so it reconciles to the cent:
//
//	CreditsRemaining + PrepaidRemaining == Balance   (always)
package bucket

import (
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// Kind is the bucket a DEPOSIT funds: non-cash Credit or real-money Prepaid.
type Kind int

const (
	// Prepaid is the default for any deposit not explicitly a grant — real money
	// (card top-up, HUSD, settlement, manual). Defaulting UNKNOWN→Prepaid is the
	// fail-closed choice: an un-tagged deposit is treated as real money the
	// customer can spend on GPUs, never as free credit — so a tagging gap can
	// never silently mint spendable-on-GPU value out of a grant.
	Prepaid Kind = iota
	// Credit is a non-cash grant (starter/welcome, plan-included allotment,
	// promo). Spendable on non-GPU usage only.
	Credit
)

// DepositKind classifies a Deposit transaction by its Tags into Credit vs
// Prepaid. Only the grant tags map to Credit; everything else (topup, husd,
// bare deposit, settlement) is real-money Prepaid.
func DepositKind(tags string) Kind {
	t := strings.TrimSpace(strings.ToLower(tags))
	switch {
	case t == "starter-credit",
		strings.HasPrefix(t, "included-credit:"),
		strings.HasPrefix(t, "credit:"),
		strings.HasPrefix(t, "grant:"):
		return Credit
	default:
		return Prepaid
	}
}

// IsGPUWithdrawal reports whether a Withdraw draws GPU compute — the spend that
// is barred from credits and must come ONLY from prepaid real money. GPU debits
// are tagged "gpu" / "gpu-<...>" / "gpu:<...>" by the GPU charge path.
func IsGPUWithdrawal(tags string) bool {
	t := strings.TrimSpace(strings.ToLower(tags))
	return t == "gpu" || strings.HasPrefix(t, "gpu-") || strings.HasPrefix(t, "gpu:")
}

// Split is the per-currency bucket projection derived from the real ledger.
// All amounts in cents. It reconciles to the ledger balance exactly:
//
//	CreditsRemaining + PrepaidRemaining == Balance
//	Available                          == CreditsRemaining + PrepaidAvailable
type Split struct {
	// CreditsGranted is the total non-cash credit EVER granted (incl. expired) —
	// the "granted" figure. Display: "Credits: $X granted".
	CreditsGranted currency.Cents `json:"creditsGranted"`
	// CreditsRemaining is the non-cash credit still spendable now (active grants
	// minus non-GPU spend, credits-first). Display: "$Y left". Also surfaced under
	// the alias `trialBalance` (see bucketFields) — "trial" and "credit" name the
	// SAME non-cash bucket (starter/welcome/comp grants); the alias is the
	// consumer-facing name so a UI reads trial-vs-prepaid without ambiguity.
	CreditsRemaining currency.Cents `json:"creditsRemaining"`
	// PrepaidBalance is the real-money balance still on the ledger (deposits minus
	// the spend charged against prepaid). GPUs draw ONLY from this.
	PrepaidBalance currency.Cents `json:"prepaidBalance"`
	// PrepaidAvailable is PrepaidBalance minus holds — the amount a new prepaid
	// (GPU or metered) charge may actually draw. The GPU gate reads this.
	PrepaidAvailable currency.Cents `json:"prepaidAvailable"`

	// Balance/Holds/Available mirror the existing ledger fields so a bucketed read
	// is a superset of GetBalance's shape (backward compatible).
	Balance   currency.Cents `json:"balance"`
	Holds     currency.Cents `json:"holds"`
	Available currency.Cents `json:"available"`
}

// Compute derives the bucket split for ONE currency from the RAW transactions
// (source- and destination-side, NOT pre-filtered for expiry). now is the
// instant expiry is evaluated against (injected for testability).
//
// Spend policy (documented, enforced consistently with the GPU charge path):
//   - Non-GPU usage draws CREDITS FIRST, then prepaid — so free grants are spent
//     before the customer's real money, exactly what a customer expects.
//   - GPU usage draws ONLY prepaid real money — never credits. GPU debits are
//     tagged so they reduce prepaid directly and can never consume a grant.
//
// Because both classes reduce the same ledger, the arithmetic is:
//
//	creditsRemaining = max(0, creditsActive - nonGpuSpend)
//	prepaidBalance   = prepaidActive - gpuSpend - max(0, nonGpuSpend - creditsActive)
//
// which sums to (creditsActive + prepaidActive - nonGpuSpend - gpuSpend) = the
// ledger balance — no value invented or lost.
func Compute(transs []*transaction.Transaction, id string, now time.Time) Split {
	var creditsGranted, creditsActive, prepaidActive currency.Cents
	var nonGpuSpend, gpuSpend, holds currency.Cents

	for _, t := range transs {
		// A transaction to self is anomalous — skip (mirrors util.TallyTransactions).
		if t.SourceId == t.DestinationId {
			continue
		}
		expired := !t.ExpiresAt.IsZero() && t.ExpiresAt.Before(now)

		switch t.Type {
		case transaction.Deposit:
			if DepositKind(t.Tags) == Credit {
				creditsGranted += t.Amount // granted total includes expired
				if !expired {
					creditsActive += t.Amount
				}
			} else if !expired {
				prepaidActive += t.Amount
			}
		case transaction.Withdraw:
			if IsGPUWithdrawal(t.Tags) {
				gpuSpend += t.Amount
			} else {
				nonGpuSpend += t.Amount
			}
		case transaction.Transfer:
			// A transfer INTO id credits real money; OUT debits like a withdrawal.
			if t.DestinationId == id {
				prepaidActive += t.Amount
			} else if t.SourceId == id {
				nonGpuSpend += t.Amount
			}
		case transaction.Hold:
			holds += t.Amount
		case transaction.HoldRemoved:
			// no-op (matches util.TallyTransactions)
		}
	}

	if holds < 0 {
		holds = 0
	}

	creditsRemaining := creditsActive - nonGpuSpend
	if creditsRemaining < 0 {
		creditsRemaining = 0
	}
	// Prepaid absorbs: all GPU spend, plus any non-GPU spend beyond credits.
	nonGpuOverflow := nonGpuSpend - creditsActive
	if nonGpuOverflow < 0 {
		nonGpuOverflow = 0
	}
	prepaidBalance := prepaidActive - gpuSpend - nonGpuOverflow

	balance := creditsRemaining + prepaidBalance
	available := balance - holds
	if available < 0 {
		available = 0
	}
	// Holds apply to spendable real money first.
	prepaidAvailable := prepaidBalance - holds
	if prepaidAvailable < 0 {
		prepaidAvailable = 0
	}

	return Split{
		CreditsGranted:   creditsGranted,
		CreditsRemaining: creditsRemaining,
		PrepaidBalance:   prepaidBalance,
		PrepaidAvailable: prepaidAvailable,
		Balance:          balance,
		Holds:            holds,
		Available:        available,
	}
}
