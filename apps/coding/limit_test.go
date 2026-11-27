package coding

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// A turn the model API refused for a usage limit is said for what it is. The free
// day spent, the harness ended "exceeded retry limit, last status: 429 Too Many
// Requests", and the run said only that its harness exited 1 — or, while the
// gateway still named an eleven-hour Retry-After, nothing at all until its sandbox
// ended it.
func TestLimitRefused(t *testing.T) {
	for name, c := range map[string]struct {
		ran  *client.Ran
		want bool
	}{
		"the retries ran out on a 429":     {&client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"exceeded retry limit, last status: 429 Too Many Requests, request id: r1"}}`}, true},
		"http2 bare 429 last status":       {&client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"exceeded retry limit, last status: 429"}}`}, true},
		"http2 bare 429 unexpected status": {&client.Ran{ExitCode: 1, Stderr: "unexpected status 429: rate limited"}, true},
		"the harness named the quota":      {&client.Ran{ExitCode: 1, Stdout: `{"type":"error","message":"Quota exceeded. Check your plan and billing details."}`}, true},
		"the gateway's code, on stderr":    {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"allowance_spent","type":"insufficient_quota"}}`}, true},
		"gateway rate_limit_exceeded":      {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"Retry after 12s."}}`}, true},
		"gateway usage_cap_exceeded":       {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"usage_cap_exceeded","resets_at":"2026-11-23T06:00:00Z"}}`}, true},
		"gateway plan_allowance_used":      {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"plan_allowance_used"}}`}, true},
		"gateway free_plan_cap":            {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"free_plan_cap"}}`}, true},
		"gateway model_cap":                {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"model_cap"}}`}, true},
		"gateway paid_lane_full":           {&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"paid_lane_full","message":"All paid capacity is busy. Retry after 10s."}}`}, true},
		"gateway quota_error":              {&client.Ran{ExitCode: 1, Stderr: `{"error":{"message":"Usage limit reached for this 8h. It resets at 2026-11-24T08:00:00Z.","type":"quota_error","code":429,"resets_at":"2026-11-24T08:00:00Z"}}`}, true},
		"line number 429 is not a limit":   {&client.Ran{ExitCode: 1, Stderr: "handler.go:429: nil pointer dereference"}, false},
		"a task that failed":               {&client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"go test failed"}}`}, false},
		"a payment refusal is not one":     {&client.Ran{ExitCode: 1, Stderr: "402 Payment Required"}, false},
		"no run":                           {nil, false},
	} {
		if got := limitRefused(c.ran); got != c.want {
			t.Errorf("%s: limitRefused = %v, want %v", name, got, c.want)
		}
	}
}

func TestTransientLimitAndResetsAt(t *testing.T) {
	if wait, ok := transientLimit(&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"rate_limit_exceeded","message":"You have made as many model requests as one person may in a minute. Retry after 12s."}}`}); !ok || wait != 12*time.Second {
		t.Fatalf("rate_limit_exceeded = (%v, %v), want (12s, true)", wait, ok)
	}
	if wait, ok := transientLimit(&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"paid_lane_full","message":"All paid capacity is busy. Retry after 10s."}}`}); !ok || wait != 10*time.Second {
		t.Fatalf("paid_lane_full = (%v, %v), want (10s, true)", wait, ok)
	}
	if wait, ok := transientLimit(&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"rate_limit_exceeded"}}`}); !ok || wait != 0 {
		t.Fatalf("rate_limit_exceeded without retry-after = (%v, %v), want (0, true)", wait, ok)
	}
	// Bare 429 Too Many Requests without a transient code is NOT transient, so
	// authority-backed parks without a reset time still keep ParkAt == 0.
	if _, ok := transientLimit(&client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"exceeded retry limit, last status: 429 Too Many Requests"}}`}); ok {
		t.Fatal("bare 429 Too Many Requests must not be treated as transient")
	}

	future := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	if got := limitResetsAt(&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"usage_cap_exceeded","resets_at":"` + future.Format(time.RFC3339) + `"}}`}); !got.Equal(future) {
		t.Fatalf("limitResetsAt future = %v, want %v", got, future)
	}
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if got := limitResetsAt(&client.Ran{ExitCode: 1, Stderr: `{"error":{"code":"usage_cap_exceeded","resets_at":"` + past.Format(time.RFC3339) + `"}}`}); !got.IsZero() {
		t.Fatalf("limitResetsAt past = %v, want zero", got)
	}
}

// A paid run that hits a short transient per-minute rate limit retries once in its
// warm sandbox; if it still hits the transient limit, it parks with Retry=true and
// a non-zero wake time instead of stranding permanently with resume_at=0.
func TestTransientRateLimitRetriesInSandboxAndParksWithWake(t *testing.T) {
	prevWait := waitForRetry
	var waited []time.Duration
	waitForRetry = func(_ context.Context, d time.Duration) bool {
		waited = append(waited, d)
		return true
	}
	t.Cleanup(func() { waitForRetry = prevWait })

	rate429 := client.Ran{
		ExitCode: 1,
		Stderr:   `{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"You have made as many model requests as one person may in a minute. Retry after 12s."}}`,
	}

	t.Run("recovers in-sandbox on retry", func(t *testing.T) {
		waited = nil
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				if turns == 1 {
					return rate429
				}
				return client.Ran{Stdout: checked}
			case slices.Contains(argv, "rev-parse"):
				if turns >= 2 {
					return client.Ran{Stdout: newSHA + "\n"}
				}
				return client.Ran{Stdout: theSHA + "\n"}
			case slices.Contains(argv, "status"):
				if turns >= 2 {
					return client.Ran{Stdout: " M main.go\n"}
				}
			}
			return client.Ran{}
		}
		servePod(t, p)
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.OK || res.Park != nil || !res.Changed {
			t.Fatalf("res = %+v, want OK=true Changed=true Park=nil", res)
		}
		if turns != 2 || len(waited) != 1 || waited[0] != 12*time.Second {
			t.Fatalf("turns=%d waited=%v, want 2 turns and one 12s wait", turns, waited)
		}
	})

	t.Run("parks with retry and wake time when retry still hits 429", func(t *testing.T) {
		waited = nil
		turns := 0
		authorities(t, resetsAt(0), nil)
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				return rate429
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Park == nil || res.Park.Reason != agents.ParkLimit || !res.Park.Retry || res.Park.At.IsZero() {
			t.Fatalf("park = %+v, want ParkLimit with Retry=true and non-zero At", res.Park)
		}
		if turns != 2 {
			t.Fatalf("turns = %d, want 2 (initial + 1 in-sandbox retry)", turns)
		}
	})

	t.Run("caps multi-hour retry-after at 60s and falls back to another Enso model", func(t *testing.T) {
		waited = nil
		long429 := client.Ran{
			ExitCode: 1,
			Stderr:   `{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"Rate limit reached. Retry after 5513s."}}`,
		}
		var modelsSeen []string
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				for i := range argv {
					if argv[i] == "-m" && i+1 < len(argv) {
						modelsSeen = append(modelsSeen, argv[i+1])
					}
				}
				if turns == 1 {
					return long429
				}
				return client.Ran{Stdout: checked}
			case slices.Contains(argv, "rev-parse"):
				if turns >= 2 {
					return client.Ran{Stdout: newSHA + "\n"}
				}
				return client.Ran{Stdout: theSHA + "\n"}
			case slices.Contains(argv, "status"):
				if turns >= 2 {
					return client.Ran{Stdout: " M main.go\n"}
				}
			}
			return client.Ran{}
		}
		servePod(t, p)
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.OK || res.Park != nil || !res.Changed {
			t.Fatalf("res = %+v, want OK=true Changed=true Park=nil", res)
		}
		if len(waited) != 1 || waited[0] != transientRetryCap {
			t.Fatalf("waited = %v, want [%v]", waited, transientRetryCap)
		}
		if len(modelsSeen) != 2 || modelsSeen[0] != codingModel || modelsSeen[1] != codingFallback {
			t.Fatalf("modelsSeen = %v, want [%s %s]", modelsSeen, codingModel, codingFallback)
		}
	})

	t.Run("model_cap falls back to another Enso model in-sandbox", func(t *testing.T) {
		waited = nil
		modelCap429 := client.Ran{
			ExitCode: 1,
			Stderr:   `{"error":{"code":"model_cap","message":"Model cap reached."}}`,
		}
		var modelsSeen []string
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				for i := range argv {
					if argv[i] == "-m" && i+1 < len(argv) {
						modelsSeen = append(modelsSeen, argv[i+1])
					}
				}
				if turns == 1 {
					return modelCap429
				}
				return client.Ran{Stdout: checked}
			case slices.Contains(argv, "rev-parse"):
				if turns >= 2 {
					return client.Ran{Stdout: newSHA + "\n"}
				}
				return client.Ran{Stdout: theSHA + "\n"}
			case slices.Contains(argv, "status"):
				if turns >= 2 {
					return client.Ran{Stdout: " M main.go\n"}
				}
			}
			return client.Ran{}
		}
		servePod(t, p)
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.OK || res.Park != nil || !res.Changed {
			t.Fatalf("res = %+v, want OK=true Changed=true Park=nil", res)
		}
		if len(modelsSeen) != 2 || modelsSeen[0] != codingModel || modelsSeen[1] != codingFallback {
			t.Fatalf("modelsSeen = %v, want [%s %s]", modelsSeen, codingModel, codingFallback)
		}
	})
}

// When the settle/check follow-up turn hits a 429/402/5xx refusal, the run must park
// (for remote runs) or fail with the refusal message (for local runs) instead of
// silently setting Unacted=true and failing with a misleading hollow-run error.
func TestCheckTurnRefusalParksOrFailsExplicitly(t *testing.T) {
	limit429 := client.Ran{
		ExitCode: 1,
		Stdout:   `{"type":"turn.failed","error":{"message":"exceeded retry limit, last status: 429 Too Many Requests"}}`,
	}
	narrated := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I will update the handler now:"}}
{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":20}}
`

	t.Run("remote run parks on check-turn 429", func(t *testing.T) {
		authorities(t, resetsAt(time.Now().Add(time.Hour).Unix()), nil)
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				if turns == 1 {
					return client.Ran{Stdout: narrated}
				}
				return limit429
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Park == nil || res.Park.Reason != agents.ParkLimit {
			t.Fatalf("res = %+v, want parked with ParkLimit", res)
		}
		if res.Unacted {
			t.Fatal("check-turn 429 must not mark the run Unacted")
		}
	})

	t.Run("local run fails with refusal error instead of hollow Unacted", func(t *testing.T) {
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				if turns == 1 {
					return client.Ran{Stdout: narrated}
				}
				return limit429
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)
		req := aRun()
		req.Local = true
		req.Remote = ""
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.OK {
			t.Fatalf("expected OK=false on local check-turn 429, got %+v", res)
		}
		if !strings.Contains(res.Error, "usage limit") || !strings.Contains(res.Error, "429") {
			t.Fatalf("res.Error = %q, want usage limit + 429", res.Error)
		}
	})
}

// affordable checks the resolved payer wallet (e.g. hanzo/alice in the signup org,
// not bare "hanzo") and honors the Unlimited plan via cloud.TierAllows.
func TestAffordableUsesResolvedPayerAndUnlimitedPlan(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	var (
		mu          sync.Mutex
		authSeen    []string
		authOrgSeen []string
	)
	commerce := zip.New(zip.Config{AppName: "commerce", DisableStartupMessage: true})
	commerce.Post("/billing/tier", func(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
		if in.Subject == "hanzo/unlim" {
			return &client.Tier{User: in.Subject, Plan: "unlimited", Tier: client.TierLimits{Name: "pro"}}, nil
		}
		return &client.Tier{User: in.Subject, Plan: "pro", Tier: client.TierLimits{Name: "pro"}}, nil
	}, zip.WithOperationID(client.BillingTier))
	commerce.Post("/commerce/authorize", func(ctx context.Context, in *client.AuthorizeIn) (*client.Verdict, error) {
		mu.Lock()
		authSeen = append(authSeen, in.Subject)
		authOrgSeen = append(authOrgSeen, zip.CallerOf(ctx).Org)
		mu.Unlock()
		if in.Subject == "hanzo" {
			return &client.Verdict{OK: false, CapSpent: true}, nil
		}
		if in.Subject == "hanzo/unlim" {
			return &client.Verdict{OK: false, CapSpent: true}, nil
		}
		return &client.Verdict{OK: true}, nil
	}, zip.WithOperationID(client.FinanceAuthorize))
	plane.Bind()
	go func(path string) { _ = commerce.Listen(path) }(zip.SocketPath("commerce"))
	t.Cleanup(func() { _ = commerce.Shutdown() })
	waitListening(t, "commerce")

	// Person in signup org: must authorize hanzo/alice, not bare "hanzo" (which has CapSpent).
	aliceCtx := zip.WithCaller(context.Background(), zip.Caller{Org: account.SignupOrg, Owner: account.SignupOrg, User: "sub-1", Name: "alice"})
	if err := affordable(aliceCtx, account.SignupOrg); err != nil {
		t.Fatalf("affordable(hanzo/alice) failed: %v", err)
	}
	// Unlimited plan payer: allowed without hitting CapSpent.
	unlimCtx := zip.WithCaller(context.Background(), zip.Caller{Org: account.SignupOrg, Owner: account.SignupOrg, User: "sub-2", Name: "unlim"})
	if err := affordable(unlimCtx, account.SignupOrg); err != nil {
		t.Fatalf("affordable(hanzo/unlim) failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authSeen) != 1 || authSeen[0] != "hanzo/alice" || authOrgSeen[0] != "hanzo" {
		t.Fatalf("FinanceAuthorize saw subjects=%v orgs=%v, want [hanzo/alice] [hanzo]", authSeen, authOrgSeen)
	}
}

// A run waits for a payer's refusal only when the API said so in words. The bare "402" that
// earns the free model one retry is also a line number, a port, a byte count in a failing
// command's stderr, and parking a run for that would hold it for credits it does not need.
//
// MUTATION: park on paymentRefused and a run that failed on `file.go:402` waits for credits.
func TestPaymentWaits(t *testing.T) {
	for name, c := range map[string]struct {
		ran            *client.Ran
		retries, waits bool
		reason         string
	}{
		"the API's words":  {&client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"unexpected status 402 Payment Required: add funds"}}`}, true, true, "payment"},
		"no balance":       {&client.Ran{ExitCode: 1, Stderr: "Insufficient balance for this model"}, true, true, "payment"},
		"a number":         {&client.Ran{ExitCode: 1, Stderr: "handler.go:402: nil pointer"}, true, false, ""},
		"a limit":          {&client.Ran{ExitCode: 1, Stderr: "exceeded retry limit, last status: 429 Too Many Requests"}, false, false, "limit"},
		"a task's failure": {&client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"go test failed"}}`}, false, false, ""},
	} {
		if got := paymentRefused(c.ran); got != c.retries {
			t.Errorf("%s: paymentRefused = %v, want %v", name, got, c.retries)
		}
		if got := paymentWaits(c.ran); got != c.waits {
			t.Errorf("%s: paymentWaits = %v, want %v", name, got, c.waits)
		}
		if got := refusedFor(c.ran); got != c.reason {
			t.Errorf("%s: refusedFor = %q, want %q", name, got, c.reason)
		}
	}
}
