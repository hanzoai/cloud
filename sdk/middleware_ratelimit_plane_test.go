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
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client/commerce"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// servePlaneRules serves the scope-rules op as app "commerce" on this process's
// plane, answering each caller org with its own rules and counting the calls. It
// is the real op id and the real wire — only the row source is a fixture.
func servePlaneRules(t *testing.T, rules map[string][]client.ScopeRule, calls *atomic.Int32) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", kit.Sockets(t))
	ResetPlane()
	t.Cleanup(ResetPlane)

	Plane().Post("/finance/scope-rules",
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
