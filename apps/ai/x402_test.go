// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

func installX402Hook(t *testing.T, fn func(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error)) {
	t.Helper()
	t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })
	cloud.SetHooks(cloud.Hooks{X402Fee: fn})
}

// Unfunded POST /v1/responses and POST /v1/chat/completions carry:
//  1. A direct clickable checkout URL + plans URL + agent top-up instructions in error.message.
//  2. Structured topup_url, billingUrl, plans_url, topup, agent, x402 fields and X-Hanzo-Topup-Url header.
//  3. The x402 PAYMENT-REQUIRED v2 challenge header when the payment rail prices the route.
func TestInference402IncludesCheckoutLinkAndX402Challenge(t *testing.T) {
	const fakeChallenge = "eyJ4NDAyVmVyc2lvbiI6MiwiYWNjZXB0cyI6W3sic2NoZW1lIjoiZXhhY3QifV19"

	for _, tc := range []struct {
		name     string
		path     string
		resource string
		mode     string // "written-ai" | "written-zen" | "returned-err"
	}{
		{"responses written by ai", "/v1/responses", client.ResponsesCall.Resource(), "written-ai"},
		{"chat written by ai", "/v1/chat/completions", client.ChatCompletion.Resource(), "written-ai"},
		{"responses written by zen", "/v1/responses", client.ResponsesCall.Resource(), "written-zen"},
		{"chat returned as error", "/v1/chat/completions", client.ChatCompletion.Resource(), "returned-err"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var challengedResource string
			installX402Hook(t, func(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
				if step == cloud.FeeTerms {
					challengedResource = resource
					c.SetHeader(client.HeaderPaymentRequired, fakeChallenge)
				}
				return cloud.FeeAnswer{}, nil
			})

			app := zip.New(zip.Config{DisableStartupMessage: true, ErrorHandler: cloud.ErrorHandler})
			app.Use(zip.H(cloud.Under("/v1", Payment())))
			handler := func(c *zip.Ctx) error {
				switch tc.mode {
				case "written-ai":
					c.SetHeader("Content-Type", "application/json")
					return c.Bytes(http.StatusPaymentRequired, []byte(`{"error":{"message":"Insufficient balance for the estimated request cost. Add credits to your wallet at https://hanzo.ai/pay","type":"billing_error","code":"insufficient_balance","topup_url":"https://hanzo.ai/pay","actions":[{"kind":"topup","label":"Buy usage credits","url":"https://hanzo.ai/pay"}]}}`))
				case "written-zen":
					return cloud.Enveloped(func(*zip.Ctx) error {
						return cloud.Denied(meter.ErrInsufficientBalance)
					})(c)
				default:
					return cloud.Denied(meter.ErrInsufficientBalance)
				}
			}
			app.Raw(http.MethodPost, tc.path, handler)

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"gpt-5.4","input":"hello"}`))
			req.Host = "api.hanzo.ai"
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusPaymentRequired {
				t.Fatalf("status = %d, want 402 (%s)", resp.StatusCode, raw)
			}
			if challengedResource != tc.resource {
				t.Fatalf("challenged resource = %q, want %q", challengedResource, tc.resource)
			}
			if got := resp.Header.Get(client.HeaderPaymentRequired); got != fakeChallenge {
				t.Fatalf("PAYMENT-REQUIRED = %q, want %q", got, fakeChallenge)
			}
			if got := resp.Header.Get("X-Hanzo-Topup-Url"); got != "https://hanzo.ai/pay/amount/card" {
				t.Fatalf("X-Hanzo-Topup-Url = %q, want https://hanzo.ai/pay/amount/card", got)
			}

			type errFields struct {
				Code       string `json:"code"`
				Message    string `json:"message"`
				Detail     string `json:"detail"`
				TopupURL   string `json:"topup_url"`
				BillingURL string `json:"billingUrl"`
				PlansURL   string `json:"plans_url"`
				Topup      string `json:"topup"`
				Agent      string `json:"agent"`
				X402       bool   `json:"x402"`
			}
			var e errFields
			if tc.mode == "returned-err" {
				if err := json.Unmarshal(raw, &e); err != nil {
					t.Fatalf("decode %s: %v", raw, err)
				}
				if e.Message == "" {
					e.Message = e.Detail
				}
			} else {
				var body struct {
					Error errFields `json:"error"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("decode %s: %v", raw, err)
				}
				e = body.Error
			}
			if e.Code != "insufficient_balance" {
				t.Errorf("code = %q, want insufficient_balance", e.Code)
			}
			if !strings.Contains(e.Message, "https://hanzo.ai/pay/amount/card") {
				t.Errorf("message missing direct checkout URL: %q", e.Message)
			}
			if !strings.Contains(e.Message, "https://hanzo.ai/pay/cart?tab=personal") {
				t.Errorf("message missing plans URL: %q", e.Message)
			}
			if !strings.Contains(e.Message, client.HeaderPaymentSignature) {
				t.Errorf("message missing x402 retry hint: %q", e.Message)
			}
			if e.TopupURL != "https://hanzo.ai/pay/amount/card" || e.BillingURL != "https://hanzo.ai/pay/amount/card" {
				t.Errorf("urls = (%q, %q), want https://hanzo.ai/pay/amount/card", e.TopupURL, e.BillingURL)
			}
			if e.PlansURL != "https://hanzo.ai/pay/cart?tab=personal" {
				t.Errorf("plans_url = %q, want https://hanzo.ai/pay/cart?tab=personal", e.PlansURL)
			}
			wantAgent := cloud.AgentURL("api.hanzo.ai")
			if e.Topup != cloud.TopupPath || e.Agent != wantAgent || !e.X402 {
				t.Errorf("agent fields = (topup=%q agent=%q x402=%v), want agent=%q", e.Topup, e.Agent, e.X402, wantAgent)
			}
		})
	}
}

// A valid PAYMENT-SIGNATURE is checked before the model runs, settles ONCE via
// X402Settle (skipping duplicate wallet debit), and returns PAYMENT-RESPONSE.
// An invalid PAYMENT-SIGNATURE is refused 402 before the model handler runs.
// A failed upstream (502) never settles the payment.
func TestInferenceX402PaymentLifecycle(t *testing.T) {
	const (
		validSig     = "valid-signed-x402-payload"
		badSig       = "tampered-x402-payload"
		fakeReceipt  = "eyJzdWNjZXNzIjp0cnVlLCJ0cmFuc2FjdGlvbiI6Ing0MDJfMTIzIn0="
		fakeRejected = "eyJ4NDAyVmVyc2lvbiI6Mn0="
	)

	t.Run("valid signature settles once and skips duplicate debit", func(t *testing.T) {
		var checks, settles, handlerCalls, ordinaryDebits int
		installX402Hook(t, func(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
			if resource != client.ResponsesCall.Resource() {
				t.Errorf("unexpected resource %q", resource)
			}
			switch step {
			case cloud.FeeCheck:
				checks++
				if c.Header(client.HeaderPaymentSignature) == validSig {
					return cloud.FeeAnswer{OK: true}, nil
				}
			case cloud.FeeSettle:
				settles++
				c.SetHeader(client.HeaderPaymentResponse, fakeReceipt)
				return cloud.FeeAnswer{OK: true}, nil
			}
			return cloud.FeeAnswer{}, nil
		})

		app := zip.New(zip.Config{DisableStartupMessage: true, ErrorHandler: cloud.ErrorHandler})
		app.Use(zip.H(cloud.Under("/v1", Payment())))
		app.Raw(http.MethodPost, "/v1/responses", func(c *zip.Ctx) error {
			handlerCalls++
			// Simulate usage recorder checking X402Settle before posting ordinary debit.
			if !X402Settle(c.Context()) {
				ordinaryDebits++
			}
			return c.JSON(http.StatusOK, map[string]any{"id": "resp_1", "status": "completed"})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hi"}`))
		req.Header.Set(client.HeaderPaymentSignature, validSig)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if checks != 1 || settles != 1 || handlerCalls != 1 || ordinaryDebits != 0 {
			t.Fatalf("checks=%d settles=%d handler=%d ordinaryDebits=%d, want 1/1/1/0", checks, settles, handlerCalls, ordinaryDebits)
		}
		if got := resp.Header.Get(client.HeaderPaymentResponse); got != fakeReceipt {
			t.Fatalf("PAYMENT-RESPONSE = %q, want %q", got, fakeReceipt)
		}
	})

	t.Run("invalid signature is refused before model runs", func(t *testing.T) {
		handlerRan := false
		installX402Hook(t, func(c *zip.Ctx, _ string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
			if step == cloud.FeeCheck && c.Header(client.HeaderPaymentSignature) == badSig {
				c.SetHeader(client.HeaderPaymentRequired, fakeRejected)
				return cloud.FeeAnswer{Status: http.StatusPaymentRequired, Code: "invalid_signature", Reason: "signature does not match"}, nil
			}
			return cloud.FeeAnswer{}, nil
		})

		app := zip.New(zip.Config{DisableStartupMessage: true, ErrorHandler: cloud.ErrorHandler})
		app.Use(zip.H(cloud.Under("/v1", Payment())))
		app.Raw(http.MethodPost, "/v1/chat/completions", func(c *zip.Ctx) error {
			handlerRan = true
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","messages":[]}`))
		req.Header.Set(client.HeaderPaymentSignature, badSig)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusPaymentRequired || handlerRan {
			t.Fatalf("status=%d handlerRan=%v, want 402 and handler not run", resp.StatusCode, handlerRan)
		}
		if resp.Header.Get(client.HeaderPaymentRequired) != fakeRejected {
			t.Fatalf("missing PAYMENT-REQUIRED header on bad signature")
		}
	})

	t.Run("upstream 502 never settles x402 payment", func(t *testing.T) {
		settles := 0
		installX402Hook(t, func(c *zip.Ctx, _ string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
			switch step {
			case cloud.FeeCheck:
				return cloud.FeeAnswer{OK: true}, nil
			case cloud.FeeSettle:
				settles++
				return cloud.FeeAnswer{OK: true}, nil
			}
			return cloud.FeeAnswer{}, nil
		})

		app := zip.New(zip.Config{DisableStartupMessage: true, ErrorHandler: cloud.ErrorHandler})
		app.Use(zip.H(cloud.Under("/v1", Payment())))
		app.Raw(http.MethodPost, "/v1/responses", func(c *zip.Ctx) error {
			return c.Bytes(http.StatusBadGateway, []byte(`{"error":{"message":"upstream down"}}`))
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
		req.Header.Set(client.HeaderPaymentSignature, validSig)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusBadGateway || settles != 0 {
			t.Fatalf("status=%d settles=%d, want 502 and 0 settles", resp.StatusCode, settles)
		}
	})
}

// A spend cap refusal is NOT an insufficient_balance refusal: x402 cannot clear
// an admin's spend cap, so PAYMENT-REQUIRED is not attached.
func TestSpendCap402GetsNoX402Challenge(t *testing.T) {
	challenged := false
	installX402Hook(t, func(c *zip.Ctx, _ string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
		if step == cloud.FeeTerms {
			challenged = true
		}
		return cloud.FeeAnswer{}, nil
	})

	app := zip.New(zip.Config{DisableStartupMessage: true, ErrorHandler: cloud.ErrorHandler})
	app.Use(zip.H(cloud.Under("/v1", Payment())))
	app.Raw(http.MethodPost, "/v1/responses", func(c *zip.Ctx) error {
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(http.StatusPaymentRequired, []byte(`{"error":{"code":"spend_cap_exceeded","message":"Monthly spend cap reached."}}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	req.Host = "api.hanzo.ai"
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if challenged {
		t.Fatal("spend_cap_exceeded unexpectedly triggered x402 challenge")
	}
}
