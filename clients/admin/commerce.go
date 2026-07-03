package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// commerceClient reads the commerce billing S2S surface (/v1/billing/*, /v1/costs)
// for the money panels (spend, tokens, credits, COGS). Commerce runs as its own
// deployment; these are HTTP calls authenticated with the admin-scoped
// COMMERCE_SERVICE_TOKEN (a KMS-sourced secret already on the cloud env — never
// hard-coded here). PER-ORG reads (balance/usage-rollup/subscriptions) resolve the
// org's billing namespace from the TRUSTED X-Org-Id header — commerce's EdgeAuth
// trusts it ONLY when the bearer is the service token — and key the wallet under the
// bare org slug (`user`). The fleet-wide /v1/costs god-view is org-INDEPENDENT
// (DigitalOcean + provider vendor bills) and sends NO org, so commerce falls back to
// its own service namespace (COMMERCE_SERVICE_ORG) there. (An earlier revision sent
// X-IAM-Org-Id, which commerce does NOT read — every per-org money panel read $0.)
type commerceClient struct {
	base  string // e.g. http://commerce.hanzo.svc.cluster.local:8001
	token string // admin S2S bearer (secret; never logged)
	http  *http.Client
}

func newCommerceClient(base, token string) *commerceClient {
	return &commerceClient{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *commerceClient) configured() bool { return c != nil && c.base != "" }

// rollup is the org-scoped billing view commerce serves at /v1/billing/usage-rollup.
// Cents are the canonical unit; consumedCents is the org's month-to-date spend.
type rollup struct {
	ConsumedCents int64 `json:"consumedCents"`
	OverageCents  int64 `json:"overageCents"`
	Balance       struct {
		BalanceCents   int64 `json:"balanceCents"`
		AvailableCents int64 `json:"availableCents"`
	} `json:"balance"`
}

// usageRollup fetches the current-month rollup for one billing subject (an IAM
// "org/user" identity) in org `org`. commerce keys usage per user; the operator
// aggregates across an org's users when a full breakdown is needed. Returns a
// zero rollup (not an error) when commerce is not configured so a partial deploy
// degrades to honest zeros rather than a 5xx.
func (c *commerceClient) usageRollup(ctx context.Context, org, user string) (rollup, error) {
	var out rollup
	if !c.configured() {
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

// balanceAll is the org's prepaid credit balance across currencies (cents).
// Sourced from /v1/billing/balance/all for the "Credits" tile.
type balanceAll struct {
	Balances map[string]struct {
		Available int64 `json:"available"`
		Balance   int64 `json:"balance"`
	} `json:"balances"`
}

// creditsCents returns the org's available credit balance in USD cents. Zero
// (not an error) when commerce is unconfigured.
func (c *commerceClient) creditsCents(ctx context.Context, org, user string) (int64, error) {
	if !c.configured() {
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
// readers fold over. Only the fields we need are decoded (status + plan
// name/price/interval); commerce emits plan.price in the currency's minor unit
// (cents) per its wire.
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

// subSummary is the plan + MRR view of an org's subscriptions in ONE read: the
// active plan name (the customer's tier), the normalized monthly recurring cents,
// and whether any subscription is active. "pay-as-you-go" is the honest default
// for a metered customer with no active subscription (not a fabricated tier).
type subSummary struct {
	Plan   string // active plan name, else "pay-as-you-go"
	MRR    int64  // monthly-normalized recurring cents from active subs
	Active bool   // any active/trialing subscription present
}

// subscriptionSummary reads /v1/billing/subscriptions ONCE and derives both the
// plan tier and the MRR contribution, so the customer list/detail and the revenue
// board share a single upstream read (DRY). Only "active"/"trialing" subscriptions
// count; canceled/past-due do not. An honest zero/"pay-as-you-go" (not an error)
// when commerce is unconfigured, so a partial deploy degrades to honest values.
func (c *commerceClient) subscriptionSummary(ctx context.Context, org, user string) (subSummary, error) {
	sum := subSummary{Plan: "pay-as-you-go"}
	if !c.configured() {
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

// mrrCents returns the monthly-recurring-revenue contribution of org `org`'s
// ACTIVE subscriptions (see subscriptionSummary). Kept as the narrow reader the
// finance/revenue folds call; it delegates so there is ONE subscriptions decode.
func (c *commerceClient) mrrCents(ctx context.Context, org, user string) (int64, error) {
	sum, err := c.subscriptionSummary(ctx, org, user)
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

// vendorCost mirrors commerce's api/costs.VendorCost — one line of what WE pay a
// vendor for a service in a period (COGS, USD cents). Decoded verbatim from
// GET /v1/costs so the finance board renders the per-vendor breakdown without
// re-deriving any cost cloud-side.
type vendorCost struct {
	Vendor      string `json:"vendor"`
	Service     string `json:"service"`
	AmountCents int64  `json:"amountCents"`
	Source      string `json:"source"` // "actual" | "estimated"
	Note        string `json:"note,omitempty"`
}

// costReport is the GET /v1/costs response: every vendor COGS line for a period
// plus the total. TotalCents is the platform's whole COGS (DigitalOcean compute +
// the LLM providers we resell) — the single figure the finance margin math folds.
type costReport struct {
	Period     string       `json:"period"`
	Vendors    []vendorCost `json:"vendors"`
	TotalCents int64        `json:"totalCents"`
	Currency   string       `json:"currency"`
}

// costs reads commerce's vendor-COGS god-view (GET /v1/costs) for a period — the
// SINGLE source of truth for what we pay every vendor. It authenticates with the
// admin S2S service token (COMMERCE_SERVICE_TOKEN, no IAM user identity), which
// commerce's requireCostsAdmin admits on its M2M path (Admin bit + empty Subject).
//
// This is a PLATFORM god-view, deliberately NOT per-org, so NO org selector is
// sent: the DigitalOcean compute and OpenAI COGS lines are read from the vendor
// billing APIs (global, org-independent) and the metered LLM estimates come from
// commerce's own service namespace (COMMERCE_SERVICE_ORG) — which commerce resolves
// from its service-token config, never from a request header. Returns a zero report
// (not an error) when commerce is unconfigured so a partial deploy degrades to
// honest zeros rather than a 5xx.
func (c *commerceClient) costs(ctx context.Context, period string) (costReport, error) {
	var out costReport
	if !c.configured() {
		return out, nil
	}
	q := url.Values{}
	if period != "" {
		q.Set("period", period)
	}
	// Empty org: /v1/costs is fleet-wide; commerce uses COMMERCE_SERVICE_ORG.
	body, err := c.get(ctx, "/v1/costs", q, "")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("commerce costs decode: %w", err)
	}
	return out, nil
}

// depositResult is the /v1/billing/deposit 201 response — the transaction id of
// the credit that landed. The operator surfaces it as the receipt of a grant.
type depositResult struct {
	TransactionID string `json:"transactionId"`
	User          string `json:"user"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
}

// deposit grants credit to an org's wallet by creating a commerce Deposit
// transaction (POST /v1/billing/deposit). This is the ONE money-in primitive the
// admin credit action uses (refunds/comps/support) — it is symmetric with the
// balance READ: the same X-Org-Id=<org> namespace + `user`=<org> subject the
// creditsCents/usageRollup reads resolve, so a grant lands exactly where the
// balance panel reads it. Authenticated with the admin S2S COMMERCE_SERVICE_TOKEN
// (commerce's /billing admin group), which is the same credential the reads use.
// Commerce's EdgeAuth additionally pins the body `user` to the X-Org-Id subject,
// so the grant can never be mis-targeted to another org's wallet. amountCents must
// be positive (a grant, never a silent debit) — the handler validates + caps it.
func (c *commerceClient) deposit(ctx context.Context, org, user string, amountCents int64, currency, notes, tags string) (depositResult, error) {
	var out depositResult
	if !c.configured() {
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

// txn is one commerce ledger row (GET /v1/billing/transactions). Cents is the
// canonical unit; Type is "deposit" (credit) or "withdraw" (usage/consumption).
// CreatedAt is the RFC3339 event time the analytics fold buckets on.
type txn struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Tags      string `json:"tags,omitempty"`
	Notes     string `json:"notes,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// transactions reads an org's ledger (GET /v1/billing/transactions) for one
// billing subject. The rows carry a real event timestamp + type, so the analytics
// aggregator can derive signup-cohort retention, active-customer windows, churn,
// and usage-over-time from actual consumption events — NOT a fabricated series.
// `limit` bounds the read (the endpoint sorts newest-first). Returns an empty
// slice (not an error) when commerce is unconfigured so a partial deploy degrades
// to an honest empty history rather than a 5xx.
func (c *commerceClient) transactions(ctx context.Context, org, user string, limit int) ([]txn, error) {
	if !c.configured() {
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
	// Commerce serves the ledger WRAPPED as { count, transactions:[...] } (verified
	// live). Decode that shape; tolerate a bare array too so a contract change in
	// either direction degrades gracefully rather than silently reading zero rows
	// (which would make the analytics honest-empty despite real usage).
	var wrap struct {
		Transactions []txn `json:"transactions"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.Transactions != nil {
		return wrap.Transactions, nil
	}
	var rows []txn
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("commerce transactions decode: %w", err)
	}
	return rows, nil
}

// post performs one admin-authenticated commerce POST (JSON body) and returns the
// raw response. It carries the SAME trust context as get: the admin S2S service
// token as the bearer and X-Org-Id=<org> as the per-org namespace selector that
// commerce's EdgeAuth trusts only after verifying the service token. A non-2xx is
// an error (the caller surfaces it honestly + records the failed attempt in the
// audit trail — a grant that did not land is never reported as success).
func (c *commerceClient) post(ctx context.Context, path, org string, body []byte) ([]byte, error) {
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
func (c *commerceClient) get(ctx context.Context, path string, q url.Values, org string) ([]byte, error) {
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
		// Commerce's EdgeAuth (middleware/edgeauth.go) trusts X-Org-Id ONLY after it
		// verifies the bearer is the COMMERCE_SERVICE_TOKEN, then resolves the per-org
		// billing namespace from it. This is the service-to-service org selector.
		// X-IAM-Org-Id is NOT read by commerce — it silently resolved to the default
		// (COMMERCE_SERVICE_ORG) namespace, so every real org's balance/spend read $0.
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

// healthClient probes an upstream's /v1/o11y/health (or any health path) so the
// overview can report System Health honestly. A non-2xx or unreachable upstream
// is reported as not-ok — never masked.
type healthClient struct {
	url  string
	http *http.Client
}

func newHealthClient(u string) *healthClient {
	return &healthClient{url: strings.TrimSpace(u), http: &http.Client{Timeout: 8 * time.Second}}
}

func (h *healthClient) configured() bool { return h != nil && h.url != "" }

// ok reports whether the o11y health endpoint answers 2xx.
func (h *healthClient) ok(ctx context.Context) (bool, error) {
	if !h.configured() {
		return false, fmt.Errorf("o11y health not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return false, err
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("o11y unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("o11y health %d", resp.StatusCode)
	}
	return true, nil
}
