// Package commerce is the admin cockpit's typed reader for the commerce billing
// S2S surface (/v1/billing/*, /v1/costs) — the money panels' single upstream.
//
// It is a self-contained HTTP client with NO dependency on the admin handlers or
// their shared state, so the aggregators read it as `commerce.Client.UsageRollup`,
// `commerce.Client.Deposit`, etc. Commerce runs as its own deployment; these are
// HTTP calls authenticated with the admin-scoped COMMERCE_SERVICE_TOKEN (a KMS-
// sourced secret already on the cloud env — never hard-coded here). PER-ORG reads
// (balance/usage-rollup/subscriptions) resolve the org's billing namespace from the
// TRUSTED X-Org-Id header — commerce's EdgeAuth trusts it ONLY when the bearer is
// the service token — and key the wallet under the bare org slug (`user`). The
// fleet-wide /v1/costs god-view is org-INDEPENDENT and sends NO org, so commerce
// falls back to its own service namespace (COMMERCE_SERVICE_ORG) there.
package commerce

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
)

// errUnconfigured marks a write (Deposit) attempted against an unwired commerce.
var errUnconfigured = errors.New("not configured")

// Client reads the commerce billing S2S surface for the money panels (spend,
// tokens, credits, COGS).
type Client struct {
	base  string // e.g. http://commerce.hanzo.svc.cluster.local:8001
	token string // admin S2S bearer (secret; never logged)
	http  *http.Client
}

// New builds a commerce client for base + admin S2S token.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured reports whether a commerce endpoint is wired on this deployment.
func (c *Client) Configured() bool { return c != nil && c.base != "" }

// Rollup is the org-scoped billing view commerce serves at /v1/billing/usage-rollup.
// Cents are the canonical unit; ConsumedCents is the org's month-to-date spend.
type Rollup struct {
	ConsumedCents int64 `json:"consumedCents"`
	OverageCents  int64 `json:"overageCents"`
	Balance       struct {
		BalanceCents   int64 `json:"balanceCents"`
		AvailableCents int64 `json:"availableCents"`
	} `json:"balance"`
}

// UsageRollup fetches the current-month rollup for one billing subject (an IAM
// "org/user" identity) in org `org`. Returns a zero rollup (not an error) when
// commerce is not configured so a partial deploy degrades to honest zeros.
func (c *Client) UsageRollup(ctx context.Context, org, user string) (Rollup, error) {
	var out Rollup
	if !c.Configured() {
		return out, nil
	}
	q := url.Values{"user": {user}}
	body, err := c.get(ctx, "/v1/billing/usage-rollup", q, org)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("commerce rollup decode: %w", err)
	}
	return out, nil
}

// CreditsCents returns the org's available credit balance in USD cents. Zero
// (not an error) when commerce is unconfigured.
func (c *Client) CreditsCents(ctx context.Context, org, user string) (int64, error) {
	if !c.Configured() {
		return 0, nil
	}
	q := url.Values{"user": {user}, "currency": {"usd"}}
	body, err := c.get(ctx, "/v1/billing/balance", q, org)
	if err != nil {
		return 0, err
	}
	var b struct {
		Available int64 `json:"available"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return 0, fmt.Errorf("commerce balance decode: %w", err)
	}
	return b.Available, nil
}

// subscriptionsWire is the /v1/billing/subscriptions list shape the MRR + plan
// readers fold over.
type subscriptionsWire struct {
	Subscriptions []struct {
		Status string `json:"status"`
		Plan   struct {
			Name     string `json:"name"`
			ID       string `json:"id"`
			Price    int64  `json:"price"`
			Currency string `json:"currency"`
			Interval string `json:"interval"`
		} `json:"plan"`
	} `json:"subscriptions"`
}

// SubSummary is the plan + MRR view of an org's subscriptions in ONE read: the
// active plan name, the normalized monthly recurring cents, and whether any
// subscription is active. "pay-as-you-go" is the honest default for a metered
// customer with no active subscription (not a fabricated tier).
type SubSummary struct {
	Plan   string // active plan name, else "pay-as-you-go"
	MRR    int64  // monthly-normalized recurring cents from active subs
	Active bool   // any active/trialing subscription present
}

// SubscriptionSummary reads /v1/billing/subscriptions ONCE and derives both the
// plan tier and the MRR contribution, so the customer list/detail and the revenue
// board share a single upstream read (DRY). Only "active"/"trialing" subscriptions
// count. An honest zero/"pay-as-you-go" (not an error) when commerce is unconfigured.
func (c *Client) SubscriptionSummary(ctx context.Context, org, user string) (SubSummary, error) {
	sum := SubSummary{Plan: "pay-as-you-go"}
	if !c.Configured() {
		return sum, nil
	}
	q := url.Values{"user": {user}}
	body, err := c.get(ctx, "/v1/billing/subscriptions", q, org)
	if err != nil {
		return sum, err
	}
	var w subscriptionsWire
	if err := json.Unmarshal(body, &w); err != nil {
		return sum, fmt.Errorf("commerce subscriptions decode: %w", err)
	}
	for _, s := range w.Subscriptions {
		switch strings.ToLower(strings.TrimSpace(s.Status)) {
		case "active", "trialing":
			sum.MRR += monthlyNormalizedCents(s.Plan.Price, s.Plan.Interval)
			sum.Active = true
			if name := strings.TrimSpace(s.Plan.Name); name != "" && sum.Plan == "pay-as-you-go" {
				sum.Plan = name
			}
		}
	}
	return sum, nil
}

// MRRCents returns the monthly-recurring-revenue contribution of org `org`'s ACTIVE
// subscriptions (see SubscriptionSummary). It delegates so there is ONE decode.
func (c *Client) MRRCents(ctx context.Context, org, user string) (int64, error) {
	sum, err := c.SubscriptionSummary(ctx, org, user)
	return sum.MRR, err
}

// monthlyNormalizedCents normalizes a plan price to a monthly figure by its
// billing interval so annual and monthly plans are comparable in one MRR sum.
func monthlyNormalizedCents(priceCents int64, interval string) int64 {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "year", "yearly", "annual", "annually":
		return priceCents / 12
	case "week", "weekly":
		return priceCents * 52 / 12
	case "day", "daily":
		return priceCents * 365 / 12
	default: // month/monthly and anything unrecognized → treat as monthly
		return priceCents
	}
}

// VendorCost mirrors commerce's api/costs.VendorCost — one line of what WE pay a
// vendor for a service in a period (COGS, USD cents).
type VendorCost struct {
	Vendor      string `json:"vendor"`
	Service     string `json:"service"`
	AmountCents int64  `json:"amountCents"`
	Source      string `json:"source"` // "actual" | "estimated"
	Note        string `json:"note,omitempty"`
}

// CostReport is the GET /v1/costs response: every vendor COGS line for a period
// plus the total (the platform's whole COGS).
type CostReport struct {
	Period     string       `json:"period"`
	Vendors    []VendorCost `json:"vendors"`
	TotalCents int64        `json:"totalCents"`
	Currency   string       `json:"currency"`
}

// Costs reads commerce's vendor-COGS god-view (GET /v1/costs) for a period — the
// SINGLE source of truth for what we pay every vendor. It authenticates with the
// admin S2S service token (no IAM user identity). This is a PLATFORM god-view,
// deliberately NOT per-org, so NO org selector is sent. Returns a zero report (not
// an error) when commerce is unconfigured so a partial deploy degrades to zeros.
func (c *Client) Costs(ctx context.Context, period string) (CostReport, error) {
	var out CostReport
	if !c.Configured() {
		return out, nil
	}
	q := url.Values{}
	if period != "" {
		q.Set("period", period)
	}
	body, err := c.get(ctx, "/v1/costs", q, "")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("commerce costs decode: %w", err)
	}
	return out, nil
}

// DepositResult is the /v1/billing/deposit 201 response — the transaction id of
// the credit that landed.
type DepositResult struct {
	TransactionID string `json:"transactionId"`
	User          string `json:"user"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
}

// Deposit grants credit to an org's wallet by creating a commerce Deposit
// transaction (POST /v1/billing/deposit) — the ONE money-in primitive the admin
// credit action uses. Symmetric with the balance READ: the same X-Org-Id=<org>
// namespace + `user`=<org> subject the reads resolve. amountCents must be positive
// (the handler validates + caps it).
func (c *Client) Deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (DepositResult, error) {
	var out DepositResult
	if !c.Configured() {
		return out, errUnconfigured
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
		return out, err
	}
	respBody, err := c.post(ctx, "/v1/billing/deposit", org, body)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return out, fmt.Errorf("commerce deposit decode: %w", err)
	}
	return out, nil
}

// Txn is one commerce ledger row (GET /v1/billing/transactions). Amount is the
// canonical cents; Type is "deposit" (credit) or "withdraw" (usage). CreatedAt is
// the RFC3339 event time the analytics fold buckets on.
type Txn struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Tags      string `json:"tags,omitempty"`
	Notes     string `json:"notes,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// Transactions reads an org's ledger (GET /v1/billing/transactions) for one billing
// subject. `limit` bounds the read (newest-first). Returns an empty slice (not an
// error) when commerce is unconfigured so a partial deploy degrades to an honest
// empty history rather than a 5xx.
func (c *Client) Transactions(ctx context.Context, org, user string, limit int) ([]Txn, error) {
	if !c.Configured() {
		return nil, nil
	}
	q := url.Values{"user": {user}}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	body, err := c.get(ctx, "/v1/billing/transactions", q, org)
	if err != nil {
		return nil, err
	}
	// Commerce serves the ledger WRAPPED as { count, transactions:[...] }; tolerate a
	// bare array too so a contract change in either direction degrades gracefully.
	var wrap struct {
		Transactions []Txn `json:"transactions"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.Transactions != nil {
		return wrap.Transactions, nil
	}
	var rows []Txn
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("commerce transactions decode: %w", err)
	}
	return rows, nil
}

// post performs one admin-authenticated commerce POST (JSON body) and returns the
// raw response. The admin S2S service token is the bearer and X-Org-Id=<org> the
// per-org namespace selector commerce's EdgeAuth trusts only after verifying the
// service token. A non-2xx is an error the caller surfaces + audits honestly.
func (c *Client) post(ctx context.Context, path, org string, body []byte) ([]byte, error) {
	u := c.base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
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
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commerce status %d", resp.StatusCode)
	}
	return respBody, nil
}

// get performs one admin-authenticated commerce GET and returns the raw body.
func (c *Client) get(ctx context.Context, path string, q url.Values, org string) ([]byte, error) {
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if org != "" {
		// Commerce's EdgeAuth trusts X-Org-Id ONLY after it verifies the bearer is the
		// COMMERCE_SERVICE_TOKEN, then resolves the per-org billing namespace from it.
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commerce status %d", resp.StatusCode)
	}
	return body, nil
}
