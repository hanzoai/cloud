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

// x402.go — per-request x402 payment and actionable 402 checkout guidance on
// /v1/responses and /v1/chat/completions.
//
// Installed in apps/zen's front chain after Paced and Capable, ahead of zen's
// Claim and ai's catch-all, so every inference request gets the same treatment
// whichever model serves it:
//
//   - Incoming PAYMENT-SIGNATURE: verified via cloud.CheckFee before any model
//     runs. The balance gate still admits; once the call succeeds, the payment
//     settles once (X402Settle) with PAYMENT-RESPONSE and skips duplicate debit.
//   - Outgoing 402 insufficient_balance: carries the x402 PAYMENT-REQUIRED
//     challenge header (when the rail prices the route) and an actionable
//     clickable checkout URL + plans URL + automated top-up guidance in the
//     error body so terminal coding agents display a direct way to pay.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

type x402PayKey struct{}

// x402Holder wraps a checked FeePayment so it settles at most once across the
// usage recorder and the middleware's return path.
type x402Holder struct {
	pay     *cloud.FeePayment
	once    sync.Once
	settled bool
}

func (h *x402Holder) settle() bool {
	if h == nil || h.pay == nil {
		return false
	}
	h.once.Do(func() {
		h.settled = h.pay.Settle()
	})
	return h.settled
}

// X402Settle settles the request's checked x402 payment, if any, at most once.
// Returns true when x402 paid for the call so the caller skips duplicate debit.
func X402Settle(ctx context.Context) bool {
	h, _ := ctx.Value(x402PayKey{}).(*x402Holder)
	return h.settle()
}

// inferenceResource maps POST /v1/responses and POST /v1/chat/completions to
// their metered x402 resource name.
func inferenceResource(method, path string) (string, bool) {
	if method != http.MethodPost {
		return "", false
	}
	switch strings.ToLower(strings.TrimRight(path, "/")) {
	case "/v1/responses":
		return client.ResponsesCall.Resource(), true
	case "/v1/chat/completions":
		return client.ChatCompletion.Resource(), true
	default:
		return "", false
	}
}

// Payment wires x402 settlement and actionable 402 checkout links over
// POST /v1/responses and POST /v1/chat/completions.
func Payment() zip.Handler {
	return func(c *zip.Ctx) error {
		resource, ok := inferenceResource(c.Method(), cloud.RoutePath(c.Path()))
		if !ok {
			return c.Continue()
		}

		pay, err := cloud.CheckFee(c, resource)
		if err != nil {
			return cloud.Denied(err)
		}
		var holder *x402Holder
		if pay != nil {
			holder = &x402Holder{pay: pay}
			c.SetContext(context.WithValue(c.Context(), x402PayKey{}, holder))
		}

		err = c.Next()

		if holder != nil {
			if err == nil && c.Fiber().Response().StatusCode() < http.StatusBadRequest {
				holder.settle()
			}
			return err
		}

		// Handle returned 402 errors (e.g. un-enveloped HTTPError or Denied).
		if err != nil {
			he := cloud.Denial(err, c.Host())
			if he != nil && he.Status == http.StatusPaymentRequired && isFundsCode(he.Code) {
				attachX402Challenge(c, resource)
				enrichHTTPError(c, he)
				return he
			}
			return err
		}

		// Handle written 402 responses (both zen's Enveloped and hanzoai/ai's direct JSON).
		if c.Fiber().Response().StatusCode() == http.StatusPaymentRequired {
			enrichWritten402(c, resource)
		}
		return nil
	}
}

func isFundsCode(code string) bool {
	switch code {
	case "", "insufficient_balance", "prepaid_required", "payment_required":
		return true
	default:
		return false
	}
}

func attachX402Challenge(c *zip.Ctx, resource string) bool {
	if len(c.Fiber().Response().Header.Peek(client.HeaderPaymentRequired)) == 0 {
		cloud.ChallengeFee(c, resource, meter.ErrInsufficientBalance)
	}
	return len(c.Fiber().Response().Header.Peek(client.HeaderPaymentRequired)) > 0
}

func checkoutURLs(host, code string) (topupURL, plansURL string) {
	topupURL = cloud.BillingURL(host, code)
	if pay := brand.For(brand.ForHost(host)).Pay; pay != "" {
		plansURL = pay + "/cart?tab=personal"
	}
	return topupURL, plansURL
}

func actionableMessage(base, topupURL, plansURL, topupAPI string, hasX402 bool) string {
	msg := strings.TrimSpace(base)
	if msg == "" {
		msg = "Insufficient balance."
	}
	if topupURL != "" && !strings.Contains(msg, topupURL) {
		// Upgrade bare pay root (e.g. https://hanzo.ai/pay) to the direct checkout step.
		if payRoot := strings.TrimSuffix(topupURL, "/amount/card"); payRoot != topupURL && strings.Contains(msg, payRoot) {
			msg = strings.ReplaceAll(msg, payRoot, topupURL)
		} else {
			if !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, ":") {
				msg += "."
			}
			msg += " Add credits at " + topupURL
		}
	}
	if plansURL != "" && !strings.Contains(msg, plansURL) {
		msg = strings.TrimRight(msg, ".") + " (or choose a plan at " + plansURL + ")."
	} else if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	var hints []string
	if topupAPI != "" && !strings.Contains(msg, topupAPI) {
		hints = append(hints, "top up via POST "+topupAPI)
	}
	if hasX402 && !strings.Contains(msg, client.HeaderPaymentSignature) {
		hints = append(hints, "retry with an x402 "+client.HeaderPaymentSignature+" header")
	}
	if len(hints) > 0 {
		msg += " Automated agents: " + strings.Join(hints, " or ") + "."
	}
	return msg
}

func enrichHTTPError(c *zip.Ctx, he *zip.HTTPError) {
	code := he.Code
	if code == "" {
		code = "insufficient_balance"
	}
	topupURL, plansURL := checkoutURLs(c.Host(), code)
	hasX402 := len(c.Fiber().Response().Header.Peek(client.HeaderPaymentRequired)) > 0
	if topupURL != "" {
		c.SetHeader("X-Hanzo-Topup-Url", topupURL)
	}
	he.Msg = actionableMessage(he.Msg, topupURL, plansURL, cloud.TopupPath, hasX402)
	extra := map[string]any{}
	if topupURL != "" {
		extra["topup_url"] = topupURL
		extra["billingUrl"] = topupURL
	}
	if plansURL != "" {
		extra["plans_url"] = plansURL
	}
	extra["topup"] = cloud.TopupPath
	if agent := cloud.AgentURL(c.Host()); agent != "" {
		extra["agent"] = agent
	}
	if hasX402 {
		extra["x402"] = true
	}
	he.With(extra)
}

func enrichWritten402(c *zip.Ctx, resource string) {
	raw := c.Fiber().Response().Body()
	if len(raw) == 0 {
		return
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		return
	}
	code, _ := errObj["code"].(string)
	if !isFundsCode(code) {
		return
	}
	if code == "" {
		code = "insufficient_balance"
	}
	hasX402 := attachX402Challenge(c, resource)
	topupURL, plansURL := checkoutURLs(c.Host(), code)
	if topupURL != "" {
		c.SetHeader("X-Hanzo-Topup-Url", topupURL)
		errObj["topup_url"] = topupURL
		errObj["billingUrl"] = topupURL
	}
	if plansURL != "" {
		errObj["plans_url"] = plansURL
	}
	if _, has := errObj["topup"]; !has {
		errObj["topup"] = cloud.TopupPath
	}
	if _, has := errObj["agent"]; !has {
		if agent := cloud.AgentURL(c.Host()); agent != "" {
			errObj["agent"] = agent
		}
	}
	if hasX402 {
		errObj["x402"] = true
	}
	msg, _ := errObj["message"].(string)
	errObj["message"] = actionableMessage(msg, topupURL, plansURL, cloud.TopupPath, hasX402)
	if actions, ok := errObj["actions"].([]any); ok && topupURL != "" {
		for _, a := range actions {
			if m, ok := a.(map[string]any); ok && m["kind"] == "topup" {
				m["url"] = topupURL
			}
		}
	}
	if out, err := json.Marshal(env); err == nil {
		c.Fiber().Response().SetBody(out)
	}
}
