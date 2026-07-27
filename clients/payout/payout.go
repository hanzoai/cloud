// Package payout is the ONE attributed-credit money seam shared by the credit
// programs — referrals, affiliates, authors: read an org's metered spend (the
// qualify / accrual base) and grant a promo credit to its wallet (a payout made
// in credits, landing in commerce's Credit/trial bucket). It was three
// byte-identical commerce.go copies (their own doc-comments said so); extracted
// here so the S2S commerce binding — the HTTP deposit + usage-rollup path — lives
// exactly ONCE.
//
// Every payout lands via the SAME COMMERCE_SERVICE_TOKEN S2S path, the same
// X-Org-Id=<org> namespace + bare-org `user` subject that admin.grantCredit uses,
// so it is indistinguishable from an admin grant except by its ledger tag
// (grant:referral / grant:affiliate / grant:author, all → the commerce Credit/trial
// bucket per DepositKind's grant:* rule). The tag is supplied BY THE CALLER, so
// payout carries zero program domain logic — it is the money seam, nothing else.
//
// Commerce is an INTERFACE so each program's store/handler logic stays testable
// with a fake ledger; Client is the ONE production binding. A program keeps its
// own narrow (unexported-method) seam and a thin adapter delegating to Client —
// Go package-scoped interface methods can't cross packages, and the adapter is
// where a program still names its own grant tag.
package payout

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

	"github.com/hanzoai/cloud/clients/commerce/transport"
)

// Commerce is the narrow money seam an attributed-credit program needs: read a
// referred/deploying org's metered spend and grant a credit to a wallet. The HTTP
// impl (Client) below is the ONE production binding.
type Commerce interface {
	Configured() bool
	// Deposit grants amountCents to org's wallet (Credit/trial bucket via the
	// caller-supplied grant:* tag) and returns the ledger transaction id.
	Deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (txnID string, err error)
	// SpendCents is the org's month-to-date metered consumption — the qualify
	// signal / commission accrual base (spend × the program's rate).
	SpendCents(ctx context.Context, org, user string) (int64, error)
}

// ErrUnconfigured is returned by a Deposit against an unwired commerce so the
// caller records an honest failure rather than reporting a phantom grant.
var ErrUnconfigured = errors.New("payout: commerce endpoint not configured")

// Client is the production commerce binding (COMMERCE_SERVICE_TOKEN S2S).
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient builds the production binding. base is the commerce HTTP URL (via
// transport.BaseURL at the call site); token is COMMERCE_SERVICE_TOKEN.
func NewClient(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  transport.Client(15 * time.Second),
	}
}

func (c *Client) Configured() bool { return c != nil && c.base != "" && c.token != "" }

// Deposit posts POST /v1/billing/deposit — the ONE money-in primitive (identical
// to admin.grantCredit's deposit). Commerce's EdgeAuth pins the body `user` to the
// X-Org-Id subject, so a payout can never be mis-targeted to another wallet.
func (c *Client) Deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (string, error) {
	if !c.Configured() {
		return "", ErrUnconfigured
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

// SpendCents reads GET /v1/billing/usage-rollup and returns consumedCents. Zero
// (not an error) when commerce is unconfigured so a partial deploy degrades to
// "no spend to accrue yet" rather than a 5xx.
func (c *Client) SpendCents(ctx context.Context, org, user string) (int64, error) {
	if !c.Configured() {
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
func (c *Client) do(ctx context.Context, method, path string, q url.Values, org string, body []byte) ([]byte, error) {
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
