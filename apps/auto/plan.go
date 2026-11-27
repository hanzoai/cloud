package auto

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	allowancepeer "github.com/hanzoai/cloud/client/allowance"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// plan.go — what a run costs an org.
//
// A PAID PLAN INCLUDES ITS RUNS. An org with an active paid plan is charged
// nothing per run; each run is counted against the runs its plan allows a day and
// an hour (apps/allowance, the automations meter), and one past them is refused
// until the window starts again. Any other org pays the run fee from its credit,
// and one without the credit is refused with the plan that runs it, which is the
// refusal the page turns into an upgrade.

// The plan, the allowance and the wallet, as values so a test answers for them.
// Production never reassigns them.
var (
	// paidPlan reports whether org pays for a plan: commerce's tier for the org's
	// own wallet, asked over the plane (cloud.Tier), so it answers in whichever
	// process automations runs. Free, or no tier at all, is no paid plan.
	paidPlan = func(ctx context.Context, _ *cloud.Service[state], org string) (bool, error) {
		tier, err := cloud.Tier(ctx, account.PayerOf("", org).Subject(), org)
		if err != nil {
			return false, err
		}
		return tier != "" && tier != "free", nil
	}
	// takeRun counts one run against org's plan allowance, and giveRun gives back
	// one a start that never ran took.
	takeRun = func(ctx context.Context, org string) (*client.Allowance, error) {
		return allowancepeer.AllowanceTake(cloud.For(ctx, org), &client.AllowanceIn{Subject: account.PayerOf("", org).Subject(), Meter: client.AllowanceAutomations})
	}
	giveRun = func(ctx context.Context, org string) {
		_, _ = allowancepeer.AllowanceGive(cloud.For(ctx, org), &client.AllowanceIn{Subject: account.PayerOf("", org).Subject(), Meter: client.AllowanceAutomations, At: time.Now().Unix()})
	}
	// authorize asks org's ledger whether it covers cents.
	authorize = func(ctx context.Context, s *cloud.Service[state], org string, cents int64) error {
		return s.Bill.Authorize(ctx, account.PayerOf("", org), "", false, meterKind, cents)
	}
)

// runFee is the per-run fee an org without a paid plan pays.
func runFee() int64 { return cloud.ResourceFeeCents(feeEnvPrefix, meterKind) }

// feeFor is what one run of org is charged: nothing on a paid plan, the run fee
// on any other. A plan that cannot be read charges nothing: the gate admitted the
// run on what it could read, and a paying customer is never charged on a guess.
func feeFor(ctx context.Context, s *cloud.Service[state], org string) int64 {
	paid, err := paidPlan(ctx, s, org)
	if err != nil || paid {
		return 0
	}
	return runFee()
}

// gateRun admits one run of org, or refuses it, BEFORE its row is written and the
// engine dispatched, and reports whether it counted the run against a plan's
// allowance, for a start that then does not run to give back. A paid plan takes
// from its allowance; any other org — and one whose plan cannot be read — must
// cover the fee.
func gateRun(s *cloud.Service[state], ctx context.Context, org string) (bool, error) {
	if paid, err := paidPlan(ctx, s, org); err == nil && paid {
		a, err := takeRun(ctx, org)
		if err != nil {
			return false, zip.Errorf(http.StatusServiceUnavailable, "automations: this plan's run allowance could not be read: %v", err)
		}
		if a != nil && a.Spent {
			return false, &zip.HTTPError{Status: http.StatusTooManyRequests, Code: "plan_allowance_used",
				Msg: fmt.Sprintf("this plan's %d automation runs this %s are used; they start again at %s", a.Limit, a.Window,
					time.Unix(a.Resets, 0).UTC().Format("15:04 UTC"))}
		}
		return true, nil
	}
	if err := authorize(ctx, s, org, runFee()); err != nil {
		if errors.Is(err, context.Canceled) {
			return false, err
		}
		return false, &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "paid_plan_required",
			Msg: "automations run on a paid plan; upgrade to run this one"}
	}
	return false, nil
}
