package agents

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
)

// TestRunOnAnUnlimitedPlanIsNotRefusedForAnEmptyWallet: the Unlimited plan
// pays for covered agent runs whatever the wallet holds — $0 included — so the
// pre-run balance gate must not answer 402 insufficient_balance. The run
// executes and the $0-wallet run is not a wallet refusal.
func TestRunOnAnUnlimitedPlanIsNotRefusedForAnEmptyWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// A $0 wallet, served by a live (fake) commerce that answers balance=0.
		bs := &billServer{available: 0}
		// Stand in for the tier read: this org's payer is on the Unlimited plan.
		// The gate asks cloud.TierAllows, which reads the PLAN through the peer
		// plane; here the peer answers "unlimited" for the payer's subject.
		t.Cleanup(func() { cloud.SetPeers(cloud.Peers{}) })
		cloud.SetPeers(cloud.Peers{
			PlanOf: func(ctx context.Context, subject string) (string, error) {
				if subject == "unltd-org" || subject == "u-unltd-org" {
					return "unlimited", nil
				}
				return "", nil
			},
		})
		// A fresh tier cache so the stubbed tier is actually asked (not a cached "").
		app := mountBilled(t, bs.start(t), &fakeAI{content: "the answer"})

		do(t, app, http.MethodPost, "/v1/agent", "unltd-org",
			map[string]any{"name": "a", "model": "m", "instructions": "x", "cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month"})
		code, body := do(t, app, http.MethodPost, "/v1/agent/a/run", "unltd-org", map[string]any{"input": "hi"})
		if code == http.StatusPaymentRequired {
			t.Fatalf("an Unlimited run with a $0 wallet was refused 402 insufficient_balance: %s", body)
		}
		if code != http.StatusOK {
			t.Fatalf("Unlimited run want 200, got %d (%s)", code, body)
		}
	})
}

// TestRunOnANonUnlimitedPlanIsStillRefusedForAnEmptyWallet: a $0 wallet on a
// plan that is not Unlimited is still refused 402 — the plan lane never widens
// to free leases or free runs for a non-Unlimited payer.
func TestRunOnANonUnlimitedPlanIsStillRefusedForAnEmptyWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 0}
		t.Cleanup(func() { cloud.SetPeers(cloud.Peers{}) })
		cloud.SetPeers(cloud.Peers{
			PlanOf: func(ctx context.Context, subject string) (string, error) {
				if subject == "dev-org" || subject == "u-dev-org" {
					return "dev-5x", nil
				}
				return "", nil
			},
		})
		app := mountBilled(t, bs.start(t), &fakeAI{content: "should not run"})

		do(t, app, http.MethodPost, "/v1/agent", "dev-org",
			map[string]any{"name": "a", "model": "m", "instructions": "x", "cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month"})
		code, body := do(t, app, http.MethodPost, "/v1/agent/a/run", "dev-org", map[string]any{"input": "hi"})
		if code != http.StatusPaymentRequired {
			t.Fatalf("a non-Unlimited $0 wallet want 402, got %d (%s)", code, body)
		}
		if bs.debits() != 0 {
			t.Fatalf("a refused run must not debit, got %d", bs.debits())
		}
	})
}

// TestMeterAuthorizeAdmitsAnUnlimitedPayerAtZero: the one gate the agent run
// path uses — cloud.Meter.Authorize — admits an Unlimited payer whose wallet
// holds 0, and still refuses a non-Unlimited payer at 0.
func TestMeterAuthorizeAdmitsAnUnlimitedPayerAtZero(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m, err := meter.New(meter.Config{BaseURL: "http://127.0.0.1:1", Timeout: 200 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		rm := cloud.NewMeter(cloud.Deps{Metering: m}, meterKind)

		t.Cleanup(func() { cloud.SetPeers(cloud.Peers{}) })
		cloud.SetPeers(cloud.Peers{
			PlanOf: func(ctx context.Context, subject string) (string, error) {
				if subject == "unltd-org" || subject == "u-unltd-org" {
					return "unlimited", nil
				}
				return "dev-5x", nil
			},
		})

		// Unlimited payer: allowed without ever reaching the (dead) ledger.
		if err := rm.Authorize(context.Background(), account.Org("unltd-org"), "", false, meterKind, 100); err != nil {
			t.Fatalf("an Unlimited payer at $0 must be authorized, got %v", err)
		}
		// Non-Unlimited payer: the ledger is unreachable, so fail-closed refuses —
		// never admits. (A $0 balance would be 402; an unreadable one is 503.)
		if err := rm.Authorize(context.Background(), account.Org("dev-org"), "", false, meterKind, 100); err == nil {
			t.Fatal("a non-Unlimited payer must not be authorized by the plan lane")
		}
	})
}

// Creating or switching an agent into long-running residency requires prepaid
// funds (unless on Unlimited), so a $0 wallet cannot start a 24/7 bot into debt.
func TestLongRunningAgentRequiresFundsUnlessUnlimited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 0}
		t.Cleanup(func() { cloud.SetPeers(cloud.Peers{}) })
		cloud.SetPeers(cloud.Peers{
			PlanOf: func(ctx context.Context, subject string) (string, error) {
				if subject == "unltd-org" || subject == "u-unltd-org" {
					return "unlimited", nil
				}
				return "pro-3x", nil
			},
		})
		app := mountBilled(t, bs.start(t), &fakeAI{content: "ok"})

		// Broke non-Unlimited org cannot create a long-running bot -> 402.
		code, body := do(t, app, http.MethodPost, "/v1/agent", "broke-org", map[string]any{
			"name": "bot1", "model": "m", "executionMode": "long-running", "schedule": "*/5 * * * *",
			"cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month",
		})
		if code != http.StatusPaymentRequired {
			t.Fatalf("create long-running on $0 wallet want 402, got %d (%s)", code, body)
		}

		// One-shot succeeds, but PATCHing it to long-running on $0 wallet -> 402.
		code, body = do(t, app, http.MethodPost, "/v1/agent", "broke-org", map[string]any{
			"name": "bot2", "model": "m",
			"cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month",
		})
		if code != http.StatusCreated {
			t.Fatalf("create one-shot want 201, got %d (%s)", code, body)
		}
		code, body = do(t, app, http.MethodPatch, "/v1/agent/bot2", "broke-org", map[string]any{
			"executionMode": "long-running", "schedule": "*/5 * * * *",
		})
		if code != http.StatusPaymentRequired {
			t.Fatalf("patch to long-running on $0 wallet want 402, got %d (%s)", code, body)
		}

		// Unlimited org at $0 wallet succeeds -> 201.
		code, body = do(t, app, http.MethodPost, "/v1/agent", "unltd-org", map[string]any{
			"name": "bot3", "model": "m", "executionMode": "long-running", "schedule": "*/5 * * * *",
			"cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month",
		})
		if code != http.StatusCreated {
			t.Fatalf("create long-running on Unlimited want 201, got %d (%s)", code, body)
		}
	})
}
