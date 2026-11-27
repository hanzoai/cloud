package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// agentPlane serves a stub commerce on the plane, as the billing endpoint reaches it.
func agentPlane(t *testing.T, register func()) *zip.App {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	register()
	stop, err := cloud.ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("ServePlane(commerce): %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	return mountApp(t)
}

// agentCall drives a request as an agent holding a bearer credential, the identity the
// gateway validated: user and org are the gateway's, never the body's.
func agentCall(t *testing.T, app *zip.App, method, path, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-agent-0123456789")
	req.Header.Set("X-User-Id", "u1")
	req.Header.Set("X-Org-Id", "hanzo")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// THE CATALOG AND THE DISCOVERY ARE PUBLIC: a request with no credential at all is served,
// and the catalog is asked for the host the request names.
func TestTheCatalogAndDiscoveryAreServedWithNoCredential(t *testing.T) {
	var host string
	app := agentPlane(t, func() {
		cloud.Plane().Post("/billing/catalog", func(_ context.Context, in *client.CatalogIn) (*client.Rendered, error) {
			host = in.Host
			return &client.Rendered{Body: []byte(`{"version":"1"}`)}, nil
		}, zip.WithOperationID(client.BillingCatalog))
		cloud.Plane().Post("/billing/discovery", func(_ context.Context, _ *cloud.Unit) (*client.Rendered, error) {
			return &client.Rendered{Body: []byte(`{"version":"1","links":{}}`)}, nil
		}, zip.WithOperationID(client.BillingDiscovery))
	})
	for _, path := range []string{"/v1/billing/catalog", "/v1/billing/discovery"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "pay.lux.cloud"
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"version":"1"`) {
			t.Errorf("GET %s with no credential = %d %s", path, resp.StatusCode, b)
		}
	}
	if host != "pay.lux.cloud" {
		t.Errorf("the catalog was asked for host %q, want the request's pay.lux.cloud", host)
	}
}

// A CHECKOUT NEEDS A VALIDATED CALLER, and the subject commerce is asked about is that
// caller's, whatever the body names.
func TestACheckoutIsTheCallersOwn(t *testing.T) {
	var saw client.CheckoutOpenIn
	app := agentPlane(t, func() {
		cloud.Plane().Post("/billing/checkout/open", func(_ context.Context, in *client.CheckoutOpenIn) (*client.AgentCheckout, error) {
			saw = *in
			return &client.AgentCheckout{ID: "chk_1", Status: "open", Currency: "usd", Items: in.Items, TotalCents: 1000}, nil
		}, zip.WithOperationID(client.BillingCheckoutOpen))
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkouts", strings.NewReader(`{"items":[{"kind":"credit","usdCents":1000}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a checkout with no credential = %d, want a refusal", resp.StatusCode)
	}
	code, body := agentCall(t, app, http.MethodPost, "/v1/billing/checkouts",
		`{"subject":"someone-else","items":[{"kind":"credit","usdCents":1000}]}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("open = %d %s, want 201", code, body)
	}
	// The links are made here from the id: a map cannot cross the plane, so the plane's checkout has none.
	if !strings.Contains(string(body), "/v1/billing/checkouts/chk_1/complete") {
		t.Errorf("the browser's checkout names no complete link: %s", body)
	}
	if saw.Subject == "" || saw.Subject == "someone-else" || len(saw.Items) != 1 {
		t.Errorf("commerce was asked for %+v: the subject is not the caller's", saw)
	}
}

// THE STATUS OF A COMPLETION IS COMMERCE'S: 200 complete, 202 waiting with the next step, a
// refusal under its own status with its code, and a status outside the document's set as
// its class. The Idempotency-Key reaches commerce.
func TestACompletionKeepsCommercesStatus(t *testing.T) {
	var status int
	var key string
	app := agentPlane(t, func() {
		cloud.Plane().Post("/billing/checkout/complete", func(_ context.Context, in *client.CheckoutCompleteIn) (*client.CheckoutCompletion, error) {
			key = in.Key
			v := client.AgentCheckout{ID: in.ID, Status: "pending", Currency: "usd", Items: []client.AgentItem{}}
			switch status {
			case 202:
				v.Next = &client.AgentNext{Type: "send_deposit", IntentID: "pi_1", Addresses: []client.DepositAddress{{Network: "base", Address: "0xabc", Tokens: []client.DepositToken{{Currency: "usdc"}}}}}
			case 402:
				v.Status, v.Error = "failed", &client.AgentError{Code: "token_over_limit", Message: "the token is too small"}
			}
			return &client.CheckoutCompletion{Status: status, Checkout: v}, nil
		}, zip.WithOperationID(client.BillingCheckoutComplete))
	})
	for _, tc := range []struct{ commerce, want int }{{200, 200}, {202, 202}, {402, 402}, {409, 409}, {500, 502}, {418, 400}} {
		status = tc.commerce
		code, body := agentCall(t, app, http.MethodPost, "/v1/billing/checkouts/chk_1/complete",
			`{"payment":{"type":"stablecoin","network":"base"}}`, map[string]string{"Idempotency-Key": "k-1"})
		if code != tc.want {
			t.Errorf("commerce %d answered %d %s, want %d", tc.commerce, code, body, tc.want)
			continue
		}
		var v struct {
			ID    string `json:"id"`
			Next  *struct{ Type string }
			Error *struct{ Code string }
		}
		if err := json.Unmarshal(body, &v); err != nil || v.ID != "chk_1" {
			t.Errorf("the body is not the checkout: %s %v", body, err)
		}
		if tc.commerce == 202 && (v.Next == nil || v.Next.Type != "send_deposit") {
			t.Errorf("202 lost its next step: %s", body)
		}
		if tc.commerce == 402 && (v.Error == nil || v.Error.Code != "token_over_limit") {
			t.Errorf("402 lost its error code: %s", body)
		}
	}
	if key != "k-1" {
		t.Errorf("commerce was given the key %q, want the Idempotency-Key header", key)
	}
}

// A TOKEN IS 200 WHEN IT IS TAKEN AND 402 WHEN IT IS DECLINED, with the reason in the body.
func TestATokenIsAcceptedOrDeclined(t *testing.T) {
	declined := false
	app := agentPlane(t, func() {
		cloud.Plane().Post("/billing/subscription/token", func(_ context.Context, in *client.AgentTokenIn) (*client.TokenSupplied, error) {
			if declined {
				return &client.TokenSupplied{Status: "declined", Error: &client.AgentError{Code: "token_not_recurring", Message: "no interval"}}, nil
			}
			return &client.TokenSupplied{Status: "ok", Token: &client.TokenRoom{RemainingCents: 5000, Covers: true}}, nil
		}, zip.WithOperationID(client.BillingSubscriptionToken))
		cloud.Plane().Post("/billing/invoice/token", func(_ context.Context, in *client.AgentTokenIn) (*client.AgentPayment, error) {
			if declined {
				return &client.AgentPayment{Status: "declined", Error: &client.AgentError{Code: "declined", Message: "no"}}, nil
			}
			return &client.AgentPayment{Status: "paid"}, nil
		}, zip.WithOperationID(client.BillingInvoiceToken))
	})
	for _, path := range []string{"/v1/billing/subscriptions/sub_1/agent-token", "/v1/billing/invoices/inv_1/agent-token"} {
		for _, d := range []bool{false, true} {
			declined = d
			want := 200
			if d {
				want = 402
			}
			if code, body := agentCall(t, app, http.MethodPost, path, `{"token":"spt_x"}`, nil); code != want {
				t.Errorf("POST %s (declined %v) = %d %s, want %d", path, d, code, body, want)
			}
		}
	}
}

// A WIRE IS RECONCILED BY THE SUPERADMIN ALONE: a member of a brand org, even its admin, is
// refused before commerce is asked.
func TestAWireIsReconciledByTheSuperAdminAlone(t *testing.T) {
	asked := false
	app := agentPlane(t, func() {
		cloud.Plane().Post("/billing/wire/reconcile", func(_ context.Context, in *client.WireReconcileIn) (*client.WireCredit, error) {
			asked = true
			return &client.WireCredit{Reference: in.Reference, AmountCents: in.AmountCents, Credited: true}, nil
		}, zip.WithOperationID(client.BillingWireReconcile))
	})
	code, body := agentCall(t, app, http.MethodPost, "/v1/billing/wire/reconcile",
		`{"reference":"WIRE-X","amountCents":2500}`, map[string]string{"X-User-Role": "admin"})
	if code != http.StatusForbidden || asked {
		t.Fatalf("a non-SuperAdmin reconcile = %d %s (commerce asked: %v), want 403 and no call", code, body, asked)
	}
}
