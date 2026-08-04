// Package payout is what the referral, affiliate and author programs ask of the
// money plane, and it is a QUESTION: what has this org spent? That read is the
// qualify signal / accrual base. It was three byte-identical commerce.go copies;
// extracted here so the S2S commerce binding lives exactly ONCE.
//
// IT NO LONGER DEPOSITS. It carried a Deposit — the ONE money-in primitive all three
// programs shared — which is how a GET on three surfaces came to mint platform
// credit. Earnings are PAYABLES now: each program ACCRUES and RECORDS what is owed,
// and a human settles it out of band. Platform credit is issued only by an admin
// grant (apps/admin/core.ApplyGrant). TestSeamIsReadOnly fails if a write returns.
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

	"github.com/hanzoai/cloud/apps/commerce/transport"
)

// Commerce is the ONE thing an attributed-credit program asks of the money plane,
// and it is a QUESTION: what has this org spent? The HTTP impl (Client) below is the
// ONE production binding. There is no write method, by design.
type Commerce interface {
	Configured() bool
	// SpendCents is the org's month-to-date metered consumption — the qualify
	// signal / commission accrual base (spend × the program's rate).
	SpendCents(ctx context.Context, org, user string) (int64, error)
}

// ErrUnconfigured is returned by a read against an unwired commerce so the caller
// records an honest failure rather than inventing a number.
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

// SpendCents reads GET /v1/billing/usage/rollup and returns consumedCents. Zero
// (not an error) when commerce is unconfigured so a partial deploy degrades to
// "no spend to accrue yet" rather than a 5xx.
func (c *Client) SpendCents(ctx context.Context, org, user string) (int64, error) {
	if !c.Configured() {
		return 0, nil
	}
	q := url.Values{"user": {user}}
	raw, err := c.do(ctx, http.MethodGet, "/v1/billing/usage/rollup", q, org, nil)
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
	return c.doWithKey(ctx, method, path, q, org, body, "")
}

func (c *Client) doWithKey(ctx context.Context, method, path string, q url.Values, org string, body []byte, ref string) ([]byte, error) {
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
	if ref != "" {
		req.Header.Set("X-Idempotency-Key", ref)
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
