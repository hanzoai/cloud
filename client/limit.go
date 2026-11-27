package client

// limit.go — the bounds staff set per org or per plan, and the one registry of
// which bounds there are.
//
// A limit's value is resolved org, then plan, then default: the row staff set on
// the org, else the row set on the plan the account is served on, else the value
// in Limits below, which is the value the enforcing code held before any row
// existed. So an empty store changes nothing. Commerce holds the rows and answers
// the resolution (BillingLimits); admin.hanzo.ai writes them through the admin
// ops, which gate and audit the act; the code that enforces a limit asks the
// resolution and holds to the default when commerce cannot answer.

import (
	"fmt"
	"strings"

	"github.com/hanzoai/account"
)

const (
	// BillingLimits reads every registered limit as it stands for the caller's
	// org, or for one plan; BillingLimitSet sets one on one scope;
	// BillingLimitDrop removes one, so the scope falls back to the next level.
	// The read answers the org's own callers, and a plan's a SuperAdmin or the
	// platform; the two writes answer a SuperAdmin only.
	BillingLimits    = "billing_limits"
	BillingLimitSet  = "billing_limit_set"
	BillingLimitDrop = "billing_limit_drop"

	// SandboxQuota reads how many of the caller org's paid sandboxes run, and how
	// many it may hold at once.
	SandboxQuota = "sandbox_quota"
)

// LimitSandboxRunning is how many paid sandboxes one account holds running or
// starting at once.
const LimitSandboxRunning = "sandbox.running"

// Where a limit's value came from.
const (
	// LimitOrg is the row staff set on the org.
	LimitOrg = "org"
	// LimitPlan is the row staff set on the plan the account is served on.
	LimitPlan = "plan"
	// LimitDefault is the registry's default: nobody set a row.
	LimitDefault = "default"
)

// Limit is one bound staff may set: what it counts, the value that holds where
// nobody set one, and the range a value may take.
type Limit struct {
	// Key names the bound, such as sandbox.running.
	Key string `json:"key"`
	// Description says what the bound counts and whose it is.
	Description string `json:"description"`
	// Unit is what one of the value is, such as sandboxes.
	Unit string `json:"unit"`
	// Default is the value where no org or plan row is set.
	Default int64 `json:"default"`
	// Min is the least a value set by staff may be.
	Min int64 `json:"min"`
	// Max is the most a value set by staff may be.
	Max int64 `json:"max"`
	// Unlimited says -1 may be set, for no bound at all.
	Unlimited bool `json:"unlimited"`
}

// Limits is every bound staff may set, and the only place one is named. A key
// that is not here is refused by every write, and a row for one is never read.
var Limits = []Limit{
	{
		Key: LimitSandboxRunning,
		Description: "Paid sandboxes one account holds running or starting at once, every class together. " +
			"In an organization the account is the organization, whoever leases; in the signup org each person is their own.",
		Unit:      "sandboxes",
		Default:   100,
		Min:       0,
		Max:       1_000_000,
		Unlimited: true,
	},
}

// LimitOf is the registered limit named key.
func LimitOf(key string) (Limit, bool) {
	for _, l := range Limits {
		if l.Key == key {
			return l, true
		}
	}
	return Limit{}, false
}

// Check refuses a value this limit cannot take.
func (l Limit) Check(v int64) error {
	if v == -1 && l.Unlimited {
		return nil
	}
	if v < l.Min || v > l.Max {
		none := ""
		if l.Unlimited {
			none = ", or -1 for no bound"
		}
		return fmt.Errorf("%s is %d to %d%s", l.Key, l.Min, l.Max, none)
	}
	return nil
}

// LimitScope reads a scope, "org:<org>" or "plan:<slug>", into its kind and the
// org or plan it names.
//
// The signup org is not a scope. It is not one customer: each person in it is
// their own account, held to their plan's limits, so a row on the org would set
// one figure for every stranger in it at once.
func LimitScope(scope string) (kind, name string, err error) {
	kind, name, _ = strings.Cut(strings.TrimSpace(scope), ":")
	if kind != LimitOrg && kind != LimitPlan {
		return "", "", fmt.Errorf("scope %q: org:<org> or plan:<slug>", scope)
	}
	if name == "" || len(name) > 128 || strings.ContainsAny(name, ":/ \t\r\n") {
		return "", "", fmt.Errorf("scope %q names no %s", scope, kind)
	}
	if kind == LimitOrg && name == account.SignupOrg {
		return "", "", fmt.Errorf("scope %q: each person in the signup org is held to their plan's limits; set the plan's", scope)
	}
	return kind, name, nil
}

// LimitRow is one value staff set on one scope.
type LimitRow struct {
	// Key is the limit, a key of Limits.
	Key string `json:"key"`
	// Scope is whom it holds for: org:<org> or plan:<slug>.
	Scope string `json:"scope"`
	// Value is the bound: the most allowed, 0 for none, -1 for no bound.
	Value int64 `json:"value"`
	// Reason is why it was set.
	Reason string `json:"reason"`
	// UpdatedBy is who set it last.
	UpdatedBy string `json:"updatedBy"`
	// UpdatedAt is when it was set last, Unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// LimitsIn asks for the limits as they stand: an org's, or one plan's.
type LimitsIn struct {
	// Subject is the account in the caller's org whose plan applies, such as a
	// person in the signup org; empty is the org's own account.
	Subject string `json:"subject,omitempty"`
	// Plan, when named, reads that plan's limits instead of the org's: its rows
	// over the defaults.
	Plan string `json:"plan,omitempty"`
}

// LimitValue is one limit as it stands: the value, the level that set it, and
// the row each level holds.
type LimitValue struct {
	// Key is the limit.
	Key string `json:"key"`
	// Value is what holds: the org's row, else the plan's, else the default.
	Value int64 `json:"value"`
	// Source is the level Value came from: org, plan or default.
	Source string `json:"source"`
	// Org is the row set on the org, when one is.
	Org *LimitRow `json:"org,omitempty"`
	// Plan is the row set on the plan, when one is.
	Plan *LimitRow `json:"plan,omitempty"`
}

// LimitSheet is every registered limit as it stands for one org, or one plan.
type LimitSheet struct {
	// Org is the org read; empty for a plan's.
	Org string `json:"org,omitempty"`
	// Plan is the plan whose rows applied: the account's, or the one asked for.
	// Empty when the account is served on none.
	Plan string `json:"plan,omitempty"`
	// Values are the limits, in the registry's order.
	Values []LimitValue `json:"values"`
}

// LimitSet sets one limit on one scope. The SuperAdmin who set it is read off
// the call.
type LimitSet struct {
	// Key is the limit, a key of Limits.
	Key string `json:"key"`
	// Scope is org:<org> or plan:<slug>.
	Scope string `json:"scope"`
	// Value is within the limit's range, or -1 where it allows no bound.
	Value int64 `json:"value"`
	// Reason is why; required.
	Reason string `json:"reason"`
}

// LimitDrop removes one limit's row from one scope.
type LimitDrop struct {
	// Key is the limit.
	Key string `json:"key"`
	// Scope is org:<org> or plan:<slug>.
	Scope string `json:"scope"`
}

// Quota is how many of an account's paid sandboxes run, and how many it may
// hold at once.
type Quota struct {
	// Account is the account the quota belongs to: the org, or a person in the
	// signup org.
	Account string `json:"account"`
	// Running is how many of its paid sandboxes hold a pod now.
	Running int64 `json:"running"`
	// Quota is how many it may hold at once; -1 for no bound.
	Quota int64 `json:"quota"`
}
