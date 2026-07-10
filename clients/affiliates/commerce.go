package affiliates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerceinproc"
)

// commerce is the narrow money seam the affiliate loop needs: read a referred
// org's metered spend (the accrual base) and grant a promo credit to a wallet (a
// payout made in credits). It is an INTERFACE so the store/handler logic is
// testable with a fake ledger — the HTTP impl below is the ONE production binding.
//
// This mirrors clients/referrals/commerce.go EXACTLY (which itself mirrors
// clients/admin/commerce.go): the same COMMERCE_SERVICE_TOKEN S2S path, the same
// X-Org-Id=<org> namespace + bare org `user` subject that admin.grantCredit uses —
// so an affiliate payout-in-credits lands in precisely the wallet the balance
// panel reads, indistinguishable from an admin grant except by its ledger tag
// (grant:affiliate vs grant:referral / grant:admin, all → the commerce Credit/trial
// bucket per DepositKind's grant:* rule).
type commerce interface {
	configured() bool
	// deposit grants amountCents to org's wallet (Credit/trial bucket via the
	// grant:affiliate tag) and returns the ledger transaction id.
	deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (txnID string, err error)
	// spendCents is a referred org's month-to-date metered consumption — the
	// commission accrual base (spend × the affiliate's rate).
	spendCents(ctx context.Context, org, user string) (int64, error)
}

// errUnconfigured is returned by a deposit against an unwired commerce so the
// caller records an honest failure rather than reporting a phantom payout.
var errUnconfigured = errors.New("affiliates: commerce endpoint not configured")

// httpCommerce is the production commerce binding (COMMERCE_SERVICE_TOKEN S2S).
type httpCommerce struct {
	base  string
	token string
	http  *http.Client
}

func newCommerceClient(base, token string) *httpCommerce {
	return &httpCommerce{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  commerceinproc.Client(15 * time.Second),
	}
}

func (c *httpCommerce) configured() bool { return c != nil && c.base != "" && c.token != "" }

// deposit posts POST /v1/billing/deposit — the ONE money-in primitive (identical
// to admin.commerceClient.deposit). Commerce's EdgeAuth pins the body `user` to
// the X-Org-Id subject, so a payout can never be mis-targeted to another wallet.
func (c *httpCommerce) deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (string, error) {
	if !c.configured() {
		return "", errUnconfigured
	}
	if currency == "" {
		currency = "usd"
	}
	body, err := json.Marshal(map[string]any{
		"user":     user,
		"currency": currency,
		"amount":   amountCents,
		"notes":    notes,
		"tags":     tags,
	})
	if err != nil {
		return "", err
	}
	raw, err := c.do(ctx, http.MethodPost, "/v1/billing/deposit", nil, org, body)
	if err != nil {
		return "", err
	}
	var out struct {
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("commerce deposit decode: %w", err)
	}
	return out.TransactionID, nil
}

// spendCents reads GET /v1/billing/usage-rollup and returns consumedCents. Zero
// (not an error) when commerce is unconfigured so a partial deploy degrades to
// "no spend to accrue yet" rather than a 5xx.
func (c *httpCommerce) spendCents(ctx context.Context, org, user string) (int64, error) {
	if !c.configured() {
		return 0, nil
	}
	q := url.Values{"user": {user}}
	raw, err := c.do(ctx, http.MethodGet, "/v1/billing/usage-rollup", q, org, nil)
	if err != nil {
		return 0, err
	}
	var out struct {
		ConsumedCents int64 `json:"consumedCents"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("commerce rollup decode: %w", err)
	}
	return out.ConsumedCents, nil
}

// do performs one admin-S2S commerce request. X-Org-Id=<org> is the per-org
// namespace selector commerce's EdgeAuth trusts only behind the service token.
func (c *httpCommerce) do(ctx context.Context, method, path string, q url.Values, org string, body []byte) ([]byte, error) {
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commerce status %d", resp.StatusCode)
	}
	return out, nil
}
