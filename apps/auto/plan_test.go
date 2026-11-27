package auto

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// planned answers the plan, the run allowance and the wallet for a test, and
// counts the allowance takes.
func planned(t *testing.T, paid bool, left *client.Allowance, broke bool) *int {
	t.Helper()
	taken := 0
	op, ot, og, oa := paidPlan, takeRun, giveRun, authorize
	paidPlan = func(context.Context, *cloud.Service[state], string) (bool, error) { return paid, nil }
	takeRun = func(context.Context, string) (*client.Allowance, error) { taken++; return left, nil }
	giveRun = func(context.Context, string) { taken-- }
	authorize = func(_ context.Context, _ *cloud.Service[state], _ string, cents int64) error {
		if broke && cents > 0 {
			return errors.New("insufficient balance")
		}
		return nil
	}
	t.Cleanup(func() { paidPlan, takeRun, giveRun, authorize = op, ot, og, oa })
	return &taken
}

func refusal(t *testing.T, err error) *zip.HTTPError {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("refusal %v is not an HTTP error", err)
	}
	return he
}

// A paid plan's runs cost nothing and are counted against the runs its plan
// allows a day; a run past them is refused until the window starts again.
func TestAPaidPlanRunsWithinItsAllowance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t)
		ctx := context.Background()
		taken := planned(t, true, &client.Allowance{Plan: "pro", Limit: 50, Used: 3}, true)
		if counted, err := gateRun(mounted, ctx, "acme"); err != nil || !counted {
			t.Fatalf("a paid plan's run = counted %v, %v", counted, err)
		}
		if *taken != 1 || feeFor(ctx, mounted, "acme") != 0 {
			t.Fatalf("taken %d times; fee %d", *taken, feeFor(ctx, mounted, "acme"))
		}
		planned(t, true, &client.Allowance{Plan: "pro", Limit: 50, Used: 50, Spent: true, Window: "day", Resets: 1}, true)
		_, err := gateRun(mounted, ctx, "acme")
		if he := refusal(t, err); he.Status != http.StatusTooManyRequests || he.Code != "plan_allowance_used" || !strings.Contains(he.Msg, "50 automation runs") {
			t.Fatalf("past the allowance = %+v", he)
		}
	})
}

// Without a paid plan a run pays the run fee from credit; with no credit it is
// refused, naming the plan that runs it.
func TestTheFreePlanPaysOrIsAskedToUpgrade(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		taken := planned(t, false, nil, false)
		if counted, err := gateRun(mounted, ctx, "acme"); err != nil || counted || feeFor(ctx, mounted, "acme") <= 0 {
			t.Fatalf("a funded free org = counted %v, %v, fee %d", counted, err, feeFor(ctx, mounted, "acme"))
		}
		if *taken != 0 {
			t.Fatal("a free org's run was counted against a plan allowance")
		}
		planned(t, false, nil, true)
		_, err := gateRun(mounted, ctx, "acme")
		if he := refusal(t, err); he.Status != http.StatusPaymentRequired || he.Code != "paid_plan_required" {
			t.Fatalf("an unfunded free org = %+v", he)
		}
		a := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{"name": "a", "instructions": "x"}))
		run := ask(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)
		if run.Code != http.StatusPaymentRequired || !strings.Contains(string(run.Body), "paid_plan_required") {
			t.Fatalf("Run now on an unfunded free org = %d %s", run.Code, run.Body)
		}
	})
}

// A plan that cannot be read admits a run that covers the fee and charges
// nothing it cannot account for; it refuses nobody for the read alone.
func TestAPlanThatCannotBeReadPaysTheFee(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t)
		ctx := context.Background()
		planned(t, false, nil, false)
		paidPlan = func(context.Context, *cloud.Service[state], string) (bool, error) {
			return false, errors.New("commerce: unreachable")
		}
		if counted, err := gateRun(mounted, ctx, "acme"); err != nil || counted {
			t.Fatalf("an unread plan with credit = counted %v, %v", counted, err)
		}
		if feeFor(ctx, mounted, "acme") != 0 {
			t.Fatal("an unread plan was charged the fee")
		}
	})
}

// A start that does not run gives back what a paid plan's allowance counted.
func TestASkippedStartGivesItsRunBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		devPlane(t, client.TurnEvents{})
		taken := planned(t, true, &client.Allowance{Plan: "pro", Limit: 50, Used: 1}, false)
		a := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{"name": "a", "instructions": "x"}))
		first := ask(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)
		second := decode[runStarted](t, ask(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil))
		if first.Code != http.StatusCreated || second.Run.Status != "skipped" || *taken != 1 {
			t.Fatalf("first %d, second %+v, counted %d", first.Code, second, *taken)
		}
	})
}
