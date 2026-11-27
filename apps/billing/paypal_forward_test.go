package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

func TestPayPalOrderTopupAndSubscribeCarryProviderToCommerce(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	var seenOrder atomic.Pointer[client.PayPalOrderIn]
	var seenTopup atomic.Pointer[client.CardIn]
	var seenSale atomic.Pointer[client.SaleIn]

	peer := zip.New(zip.Config{Logger: luxlog.New("test")})
	peer.Post("/billing/settings", func(ctx context.Context, _ *struct{}) (*client.PaymentConfig, error) {
		return &client.PaymentConfig{
			Provider:      "square",
			ApplicationID: "sq-app-1",
			LocationID:    "sq-loc-1",
			Environment:   "sandbox",
			Live:          false,
			PayPal: &client.PayPalPublicConfig{
				Enabled:     true,
				ClientID:    "sbx-client-id",
				Environment: "sandbox",
			},
		}, nil
	}, zip.WithOperationID(client.BillingSettings))
	peer.Post("/billing/paypal/order", func(ctx context.Context, in *client.PayPalOrderIn) (*client.PayPalOrderOut, error) {
		seenOrder.Store(in)
		intent := "CAPTURE"
		if in.PlanID != "" {
			intent = "AUTHORIZE"
		}
		return &client.PayPalOrderOut{
			OrderID:     "ORDER-123",
			Intent:      intent,
			AmountCents: 2000,
			Currency:    "usd",
		}, nil
	}, zip.WithOperationID(client.BillingPayPalOrder))
	peer.Post("/billing/topup/card", func(ctx context.Context, in *client.CardIn) (*client.Charged, error) {
		seenTopup.Store(in)
		return &client.Charged{
			TransactionID: "tx_pp_1",
			ProcessorRef:  "CAP-123",
			BalanceCents:  2000,
			Status:        "ok",
		}, nil
	}, zip.WithOperationID(client.BillingTopupCard))
	peer.Post("/billing/subscribe", func(ctx context.Context, in *client.SaleIn) (*client.Sold, error) {
		seenSale.Store(in)
		return &client.Sold{
			Sale: &client.Sale{
				SubscriptionID: "sub_pp_1",
				InvoiceID:      "inv_pp_1",
				PlanID:         in.PlanID,
				AmountCents:    2000,
				Currency:       "usd",
				Status:         "ok",
			},
		}, nil
	}, zip.WithOperationID(client.BillingSubscribe))

	sock := zip.SocketPath("commerce")
	listened := make(chan error, 1)
	go func() { listened <- peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		select {
		case err := <-listened:
			t.Fatalf("stand-in commerce did not listen: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	app := mountApp(t)

	// 1. GET /v1/billing/settings exposes PayPal public SDK config.
	code, raw := call(t, app, http.MethodGet, "/v1/billing/settings", "alice", "acme")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/billing/settings: got %d (%s)", code, raw)
	}
	var cfg client.PaymentConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if cfg.PayPal == nil || !cfg.PayPal.Enabled || cfg.PayPal.ClientID != "sbx-client-id" {
		t.Fatalf("PayPal settings not projected: %+v", cfg.PayPal)
	}

	// 2. Non-admin member cannot open an org plan PayPal order (steward gate).
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/paypal/order",
			strings.NewReader(`{"kind":"plan","planId":"team","quantity":3}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "bob")
		req.Header.Set("X-Org-Id", "acme")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-admin org plan order: got %d, want 403", resp.StatusCode)
		}
	}

	// 3. Org admin creates a plan PayPal order.
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/paypal/order",
			strings.NewReader(`{"kind":"plan","planId":"dev","coupon":"HALF"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "alice")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-IsOrgAdmin", "true")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/billing/paypal/order: got %d (%s)", resp.StatusCode, b)
		}
		got := seenOrder.Load()
		if got == nil || got.PlanID != "dev" || got.Coupon != "HALF" || got.Subject != "acme" {
			t.Fatalf("commerce saw PayPal order %+v", got)
		}
	}

	// 4. POST /v1/billing/topup/token forwards provider="paypal".
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/topup/token",
			strings.NewReader(`{"sourceId":"ORDER-123","amountCents":2000,"currency":"usd","provider":"paypal"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "alice")
		req.Header.Set("X-Org-Id", "acme")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/billing/topup/token: got %d (%s)", resp.StatusCode, b)
		}
		got := seenTopup.Load()
		if got == nil || got.Provider != "paypal" || got.SourceID != "ORDER-123" {
			t.Fatalf("commerce saw topup %+v, want provider=paypal", got)
		}
	}

	// 5. POST /v1/billing/subscribe/card forwards provider="paypal" and wallet=true.
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe/card",
			strings.NewReader(`{"planId":"dev","sourceId":"ORDER-123","wallet":true,"provider":"paypal"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "alice")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-IsOrgAdmin", "true")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("POST /v1/billing/subscribe/card: got %d (%s)", resp.StatusCode, b)
		}
		got := seenSale.Load()
		if got == nil || got.Provider != "paypal" || !got.Wallet || got.SourceID != "ORDER-123" {
			t.Fatalf("commerce saw sale %+v, want provider=paypal wallet=true", got)
		}
	}
}
