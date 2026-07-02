package admin

import (
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
// hard-coded here). Commerce resolves the billing namespace from its OWN
// service-token config (COMMERCE_SERVICE_ORG), NOT from a request header; the
// per-org money reads distinguish orgs by the `user` query param within that
// namespace. (The X-IAM-Org-Id header get() sends is advisory — commerce does not
// consult it — so it is omitted for the fleet-wide /v1/costs god-view.)
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

// subscriptionsWire is the /v1/billing/subscriptions list shape the MRR reader
// folds over. Only the fields MRR needs are decoded (status + plan price/interval);
// commerce emits plan.price in the currency's minor unit (cents) per its wire.
type subscriptionsWire struct {
	Subscriptions []struct {
		Status string `json:"status"`
		Plan   struct {
			Price    int64  `json:"price"`
			Currency string `json:"currency"`
			Interval string `json:"interval"`
		} `json:"plan"`
	} `json:"subscriptions"`
}

// mrrCents returns the monthly-recurring-revenue contribution of org `org`'s
// ACTIVE subscriptions, normalized to a monthly figure (a yearly plan counts as
// price/12). Only "active"/"trialing" subscriptions count toward MRR; canceled or
// past-due do not. Zero (not an error) when commerce is unconfigured, so a partial
// deploy degrades to honest zero rather than a fabricated recurring number.
func (c *commerceClient) mrrCents(ctx context.Context, org, user string) (int64, error) {
	if !c.configured() {
		return 0, nil
	}
	q := url.Values{"user": {user}}
	body, err := c.get(ctx, "/v1/billing/subscriptions", q, org)
	if err != nil {
		return 0, err
	}
	var w subscriptionsWire
	if err := json.Unmarshal(body, &w); err != nil {
		return 0, fmt.Errorf("commerce subscriptions decode: %w", err)
	}
	var mrr int64
	for _, s := range w.Subscriptions {
		switch strings.ToLower(strings.TrimSpace(s.Status)) {
		case "active", "trialing":
			mrr += monthlyNormalizedCents(s.Plan.Price, s.Plan.Interval)
		}
	}
	return mrr, nil
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
		req.Header.Set("X-IAM-Org-Id", org)
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
