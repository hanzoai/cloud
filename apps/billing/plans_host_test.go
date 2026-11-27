package billing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/kit"
)

// THE CATALOG IS THE SERVING BRAND'S, so the host the page is served on has to
// reach the store — the same host the org endpoint resolves the brand from
// (zip.Ctx.Host), so the brand a page wears and the plans it lists cannot
// disagree. A client's own X-Forwarded-Host is believed only from a trusted hop,
// never from the caller: it cannot pick another brand's catalog.
//
// This asserts the HANDOFF: the stub stands in for commerce and records the host
// it was told.
func TestThePlansReadNamesTheServingHost(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	var saw client.PlansIn
	cloud.Plane().Post("/billing/plans",
		func(_ context.Context, in *client.PlansIn) (*client.Rendered, error) {
			saw = *in
			return &client.Rendered{Body: []byte(`[]`)}, nil
		},
		zip.WithOperationID(client.BillingPlans))

	stop, err := cloud.ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("ServePlane(commerce): %v", err)
	}
	defer func() { _ = stop() }()

	app := mountApp(t)

	for _, tc := range []struct {
		name, host, forwarded, want string
	}{
		{"direct", "pay.lux.cloud", "", "pay.lux.cloud"},
		// A caller naming another brand's host is still on this one.
		{"spoofed", "pay.lux.cloud", "pay.hanzo.ai", "pay.lux.cloud"},
		{"spoofed chain", "pay.zoo.cloud", "pay.hanzo.ai, pay.zoo.cloud", "pay.zoo.cloud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saw = client.PlansIn{}
			req := httptest.NewRequest(http.MethodGet, "/v1/billing/plans?category=personal", nil)
			req.Host = tc.host
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Host", tc.forwarded)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("GET /v1/billing/plans: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			if saw.Host != tc.want || saw.Category != "personal" {
				t.Errorf("the store was asked for %+v, want host %q and category personal", saw, tc.want)
			}
		})
	}
}
