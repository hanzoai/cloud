package employee

import (
	"math"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// Unlimited is the sentinel remaining-spend returned when an employee's limit
// is 0 (meaning no cap). It is math.MaxInt64 cents so callers that compare
// against it treat any request as affordable.
const Unlimited = currency.Cents(math.MaxInt64)

// RemainingSpendCents reports how much an employee may still commit given the
// amount already committed and their limit. A limit of 0 means unlimited, in
// which case Unlimited is returned. Otherwise the remainder (limit-committed)
// is returned and may be negative if the employee is already over limit.
func RemainingSpendCents(committedCents, limitCents currency.Cents) currency.Cents {
	if limitCents == 0 {
		return Unlimited
	}
	return limitCents - committedCents
}

// WithinLimit reports whether adding requested cents to what is already
// committed stays within limit. A limit of 0 is unlimited and always passes.
// The boundary is inclusive: committing exactly up to the limit is allowed.
func WithinLimit(committed, requested, limit currency.Cents) bool {
	if limit == 0 {
		return true
	}
	return committed+requested <= limit
}
