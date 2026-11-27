package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
)

// selecting mounts billing on deps behind the boundary's record of a selection it
// did not open, which a request here states with X-Test-Unopened.
func selecting(t *testing.T, deps cloud.Deps) *zip.App {
	t.Helper()
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	app.Use(zip.Handler(func(c *zip.Ctx) error {
		if sel := c.Header("X-Test-Unopened"); sel != "" {
			principal.Unopen(c, sel)
		}
		return c.Next()
	}))
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

// posted posts body to path with the headers given as pairs and answers the status.
func posted(t *testing.T, app *zip.App, path, body string, hdr ...string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A top-up for an org the sign-in does not list is refused before the card, as a
// sale for it is: it would land in the org the caller acts in instead.
func TestATopUpForAnOrgNotOpenedIsRefused(t *testing.T) {
	app := selecting(t, cloud.Deps{})
	who := []string{"X-User-Id", "ada", "X-User-Owner", "ada", "X-Org-Id", "ada", "X-User-IsOrgAdmin", "true", "X-Test-Unopened", "acme-new"}
	for path, body := range map[string]string{
		"/v1/billing/topup/token": `{"sourceId":"cnon:ok","amountCents":2000}`,
		"/v1/billing/topup":       `{"paymentMethodId":"pm_1","amountCents":2000}`,
	} {
		if got := posted(t, app, path, body, who...); got != http.StatusConflict {
			t.Errorf("%s for an org not opened: %d, want 409", path, got)
		}
	}
}

// A plan sale is on the audit trail under the org the request acted in, whoever's
// home org that is, and so is every refusal: this endpoint's (403, 409) and
// commerce's (400). A SuperAdmin acting in an org is recorded with their home org.
func TestASaleIsAuditedUnderTheOrgItActedIn(t *testing.T) {
	seller(t, func(_ context.Context, in *client.SaleIn) (*client.Sold, error) {
		if in.PlanID == "nope" {
			return nil, zip.ErrBadRequest("plan not found")
		}
		return &client.Sold{Sale: &client.Sale{SubscriptionID: "sub_" + in.Subject, PlanID: in.PlanID, Seats: 20,
			AmountCents: 480000, BalanceCents: in.BalanceCents, Currency: "usd", Status: "active"}}, nil
	})
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("open trail: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	app := selecting(t, cloud.Deps{Audit: trail})
	buy := func(body string, hdr ...string) int {
		return posted(t, app, "/v1/billing/subscribe/card", body, hdr...)
	}
	order := `{"planId":"enterprise","sourceId":"cnon:ok","interval":"year","quantity":20,"balanceCents":40000}`
	admin := []string{"X-User-Id", "ada", "X-User-Owner", "ada", "X-Org-Id", "acme", "X-User-IsOrgAdmin", "true"}
	for name, tc := range map[string]struct {
		body string
		hdr  []string
		want int
	}{
		"an admin's sale":         {order, admin, http.StatusCreated},
		"commerce refuses":        {`{"planId":"nope","sourceId":"cnon:ok"}`, admin, http.StatusBadRequest},
		"a member is refused":     {order, []string{"X-User-Id", "bob", "X-User-Owner", "bob", "X-Org-Id", "acme"}, http.StatusForbidden},
		"a SuperAdmin acts in it": {order, []string{"X-User-Id", "z", "X-User-Owner", "admin", "X-Org-Id", "acme", "X-User-IsAdmin", "true", "X-User-IsOrgAdmin", "true"}, http.StatusCreated},
		"a selection not opened":  {order, []string{"X-User-Id", "ada", "X-User-Owner", "ada", "X-Org-Id", "ada", "X-User-IsOrgAdmin", "true", "X-Test-Unopened", "acme-new"}, http.StatusConflict},
	} {
		if got := buy(tc.body, tc.hdr...); got != tc.want {
			t.Errorf("%s: answered %d, want %d", name, got, tc.want)
		}
	}

	rows, _, err := trail.Query(context.Background(), audit.Filter{Action: "billing.plan.sale"})
	if err != nil || len(rows) != 5 {
		t.Fatalf("trail holds %d sale rows, %v; want 5", len(rows), err)
	}
	type row struct {
		org, home, sub string
		status         int
	}
	var got []row
	for _, r := range rows {
		got = append(got, row{r.Actor.Org, r.Actor.Home, r.Actor.Sub, r.Outcome.Status})
		switch {
		case r.Actor.Sub == "z" && (r.Actor.Org != "acme" || r.Actor.Home != "admin" || !r.Auth.IsAdmin):
			t.Errorf("the SuperAdmin's sale was filed %+v, want under acme with home admin", r.Actor)
		case r.Actor.Sub == "ada" && r.Outcome.Status == http.StatusCreated &&
			(r.Actor.Org != "acme" || r.Actor.Home != "" || !strings.Contains(string(r.After), `"amountCents":480000`)):
			t.Errorf("the admin's sale was filed %+v %s, want under acme with what it charged", r.Actor, r.After)
		case r.Outcome.Status == http.StatusBadRequest && (r.Actor.Org != "acme" || r.Resource.ID != "nope"):
			t.Errorf("commerce's refusal was filed %+v on %+v, want under acme naming the plan", r.Actor, r.Resource)
		case r.Outcome.Status == http.StatusForbidden && (r.Actor.Org != "acme" || r.Actor.Sub != "bob"):
			t.Errorf("the member's refusal was filed %+v, want under acme", r.Actor)
		case r.Outcome.Status == http.StatusConflict && (r.Actor.Org != "ada" || !strings.Contains(string(r.After), `"selected":"acme-new"`)):
			t.Errorf("the unopened refusal was filed %+v %s, want under the org it acted in, naming the selection", r.Actor, r.After)
		}
	}
	t.Logf("filed: %+v", got)
}
