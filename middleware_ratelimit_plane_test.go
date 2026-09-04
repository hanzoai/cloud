package cloud

// The 502-recursion regression. ScopeRateLimit's config source is commerce's
// spend-alert rows, and it used to fetch them with a service-token
// GET /v1/billing/alerts through the commerce transport. That transport
// dispatches in-process by publishing the WHOLE shared app, so the fetch re-ran
// the entire edge chain — including this middleware, whose cache is filled only
// AFTER the fetch returns and is therefore still cold. It fetched again, and
// again, until the transport's depth guard refused: 502, ~135 per half hour on
// the live pod, and the ceiling failing open the whole time.
//
// The fix asks the process that owns the rows over the plane. A plane socket
// carries that app's ops and no edge chain, so nothing it reaches can re-enter
// the middleware that called it — the recursion is absent, not bounded. These
// tests publish the app on the transport exactly as commerce's Mount does, so the
// self-dispatch is available; the counters prove it never happens.

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client/commerce"

	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sock"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// servePlaneRules serves the scope-rules op as app "commerce" on this process's
// plane, answering each caller org with its own rules and counting the calls. It
// is the real op id and the real wire — only the row source is a fixture.
func servePlaneRules(t *testing.T, rules map[string][]client.ScopeRule, calls *atomic.Int32) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", sock.Dir(t))
	ResetPlane()
	t.Cleanup(ResetPlane)

	zip.Post[struct{}, client.ScopeRules](Plane(), "/finance/scope-rules",
		func(ctx context.Context, _ *struct{}) (*client.ScopeRules, error) {
			if calls != nil {
				calls.Add(1)
			}
			// The org rides the CALLER, never an argument — the same rule the real
			// op enforces, so a test cannot pass through a path production closes.
			org := Who(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("scope rules: no org on the call")
			}
			return &client.ScopeRules{Rules: rules[org]}, nil
		}, zip.WithOperationID(client.FinanceScopeRules))

	stop, err := ServePlane(commerce.App, luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// coresidentRateApp is rateApp plus the two things the production arrangement
// has and a bare test app does not: a counter on the way in, and the app
// PUBLISHED on the commerce transport — so an in-process self-dispatch is
// available to anything that tries one.
func coresidentRateApp(t *testing.T, entries *atomic.Int32) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
	app.Use(zip.H(func(c *zip.Ctx) error {
		entries.Add(1)
		return c.Next()
	}))
	app.Use(ScopeRateLimit(mustClient(t, transport.PlaceholderBase, false), nil))
	app.Post("/v1/agent/run", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	transport.SetApp(app.Fiber())
	t.Cleanup(func() { transport.SetHandler(nil) })
	return app
}

// TestScopeRulesComeOverThePlaneNotThroughTheApp: the ceiling still binds, and
// getting it costs exactly one plane call and ZERO extra trips through the app.
// A re-entrant fetch would show up as more entries than requests — and at depth 8
// as the 502 that was in production.
func TestScopeRulesComeOverThePlaneNotThroughTheApp(t *testing.T) {
	var planeCalls, entries atomic.Int32
	servePlaneRules(t, map[string][]client.ScopeRule{
		"hanzo": {{Project: "P", RateLimitRpm: 2}},
	}, &planeCalls)
	app := coresidentRateApp(t, &entries)

	for i, want := range []int{200, 200, http.StatusTooManyRequests} {
		if got := rateReq(t, app, "hanzo", "P").StatusCode; got != want {
			t.Fatalf("req%d = %d, want %d", i+1, got, want)
		}
	}

	if got := entries.Load(); got != 3 {
		t.Fatalf("the app was entered %d times for 3 requests — the rules fetch re-entered it (this is the self-dispatch that 502'd)", got)
	}
	if got := planeCalls.Load(); got != 1 {
		t.Fatalf("plane calls = %d, want 1 (one fetch, then the TTL cache)", got)
	}
}

// TestScopeRulesFailOpenWhenCommerceIsNotDeployed: a rate ceiling is a policy
// overlay, never a gate on availability. With no commerce on the plane the read
// answers ErrNoPeer and the request is NOT limited — the funds and spend-cap
// gates still apply, and a config outage must not take down paid traffic.
func TestScopeRulesFailOpenWhenCommerceIsNotDeployed(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", sock.Dir(t)) // an empty run dir: no commerce socket.
	ResetPlane()
	t.Cleanup(ResetPlane)

	var entries atomic.Int32
	app := coresidentRateApp(t, &entries)
	for i := range 5 {
		if got := rateReq(t, app, "hanzo", "P").StatusCode; got != http.StatusOK {
			t.Fatalf("req%d = %d, want 200 (an unreachable config source must fail OPEN)", i+1, got)
		}
	}
	if got := entries.Load(); got != 5 {
		t.Fatalf("the app was entered %d times for 5 requests, want 5", got)
	}
}
