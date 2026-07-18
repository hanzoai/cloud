// Package commerce is the admin cockpit's typed reader for the commerce billing
// plane. It models the domain, not the endpoints: a billing subject (an org's
// slug) has orthogonal, independently-readable facets —
//
//	Spend    — what it consumed this month
//	Credits  — what prepaid balance it holds
//	Plan     — its subscription tier + monthly-recurring revenue
//	Ledger   — its transaction history
//
// plus one fleet god-view (Costs, our vendor COGS) and one write (Deposit, the
// grant-credit primitive). Each read is total: an unwired or unreachable commerce
// degrades to an honest zero, never a fabricated number.
//
// Commerce runs as its own deployment; these are HTTP calls authenticated with the
// admin-scoped COMMERCE_SERVICE_TOKEN (a KMS-sourced secret already on the cloud
// env — never hard-coded). A per-subject read resolves the org's billing namespace
// from the TRUSTED X-Org-Id header (commerce's EdgeAuth trusts it only when the
// bearer is the service token) AND keys the wallet under the bare slug — one value,
// the subject, is both. The fleet Costs god-view is org-independent and sends no
// subject.
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

	"github.com/hanzoai/cloud/clients/admin/money"
)

// errUnconfigured marks a write (Deposit) attempted against an unwired commerce.
var errUnconfigured = errors.New("commerce not configured")

// Client reads the commerce billing plane.
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

// Ready reports whether a commerce endpoint is wired on this deployment.
func (c *Client) Ready() bool { return c != nil && c.base != "" }

// Spend is a subject's month-to-date consumption.
type Spend struct {
	Consumed money.Cents `json:"consumedCents"`
	Overage  money.Cents `json:"overageCents"`
}

// Spend reads a subject's month-to-date consumption (GET /v1/billing/usage-rollup).
// Zero (not an error) when commerce is unwired, so a partial deploy degrades to
// honest zeros.
func (c *Client) Spend(ctx context.Context, subject string) (Spend, error) {
	var out Spend
	if !c.Ready() {
		return out, nil
	}
	body, err := c.get(ctx, "/v1/billing/usage-rollup", url.Values{"user": {subject}}, subject)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("commerce spend decode: %w", err)
	}
	return out, nil
}

// Credits reads a subject's available prepaid credit (GET /v1/billing/balance).
// Zero (not an error) when commerce is unwired.
func (c *Client) Credits(ctx context.Context, subject string) (money.Cents, error) {
	if !c.Ready() {
		return 0, nil
	}
	q := url.Values{"user": {subject}, "currency": {"usd"}}
	body, err := c.get(ctx, "/v1/billing/balance", q, subject)
	if err != nil {
		return 0, err
	}
	var b struct {
		Available money.Cents `json:"available"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return 0, fmt.Errorf("commerce credits decode: %w", err)
	}
	return b.Available, nil
}

// Plan is a subject's subscription: the active tier, the monthly-normalized
// recurring revenue, and whether any subscription is active. Name is
// "pay-as-you-go" for a metered subject with no active subscription (the honest
// default, never a fabricated tier).
type Plan struct {
	Name   string
	MRR    money.Cents
	Active bool
}

// subscriptionsWire is the /v1/billing/subscriptions list shape Plan folds over.
type subscriptionsWire struct {
	Subscriptions []struct {
		Status string `json:"status"`
		Plan   struct {
			Name     string      `json:"name"`
			Price    money.Cents `json:"price"`
			Interval string      `json:"interval"`
		} `json:"plan"`
	} `json:"subscriptions"`
}

// Plan reads a subject's subscription tier + MRR in ONE decode (GET
// /v1/billing/subscriptions), so the customer + revenue surfaces share a single
// upstream read. Only "active"/"trialing" subscriptions count. Honest
// zero/"pay-as-you-go" (not an error) when commerce is unwired.
func (c *Client) Plan(ctx context.Context, subject string) (Plan, error) {
	out := Plan{Name: "pay-as-you-go"}
	if !c.Ready() {
		return out, nil
	}
	body, err := c.get(ctx, "/v1/billing/subscriptions", url.Values{"user": {subject}}, subject)
	if err != nil {
		return out, err
	}
	var w subscriptionsWire
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("commerce plan decode: %w", err)
	}
	for _, s := range w.Subscriptions {
		switch strings.ToLower(strings.TrimSpace(s.Status)) {
		case "active", "trialing":
			out.MRR += monthlyNormalized(s.Plan.Price, s.Plan.Interval)
			out.Active = true
			if name := strings.TrimSpace(s.Plan.Name); name != "" && out.Name == "pay-as-you-go" {
				out.Name = name
			}
		}
	}
	return out, nil
}

// monthlyNormalized normalizes a plan price to a monthly figure by its billing
// interval so annual and monthly plans are comparable in one MRR sum.
func monthlyNormalized(price money.Cents, interval string) money.Cents {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "year", "yearly", "annual", "annually":
		return price / 12
	case "week", "weekly":
		return price * 52 / 12
	case "day", "daily":
		return price * 365 / 12
	default: // month/monthly and anything unrecognized → treat as monthly
		return price
	}
}

// Entry is one ledger row. Kind is "deposit" (credit) or "withdraw" (usage). At is
// the RFC3339 event time analytics buckets on.
type Entry struct {
	ID       string      `json:"id"`
	Kind     string      `json:"type"`
	Amount   money.Cents `json:"amount"`
	Currency string      `json:"currency"`
	Tags     string      `json:"tags,omitempty"`
	Notes    string      `json:"notes,omitempty"`
	At       string      `json:"createdAt"`
}

// Ledger reads a subject's transaction history (GET /v1/billing/transactions),
// newest-first, bounded by limit. Empty (not an error) when commerce is unwired.
func (c *Client) Ledger(ctx context.Context, subject string, limit int) ([]Entry, error) {
	if !c.Ready() {
		return nil, nil
	}
	q := url.Values{"user": {subject}}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	body, err := c.get(ctx, "/v1/billing/transactions", q, subject)
	if err != nil {
		return nil, err
	}
	// Commerce serves the ledger WRAPPED as { count, transactions:[...] }; tolerate a
	// bare array too so a contract change in either direction degrades gracefully.
	var wrap struct {
		Transactions []Entry `json:"transactions"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.Transactions != nil {
		return wrap.Transactions, nil
	}
	var rows []Entry
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("commerce ledger decode: %w", err)
	}
	return rows, nil
}

// Vendor is one line of what WE pay a vendor for a service in a period (COGS).
type Vendor struct {
	Name    string      `json:"vendor"`
	Service string      `json:"service"`
	Amount  money.Cents `json:"amountCents"`
	Source  string      `json:"source"` // "actual" | "estimated"
	Note    string      `json:"note,omitempty"`
}

// Costs is the fleet COGS god-view: every vendor line for a period plus the total.
type Costs struct {
	Period   string      `json:"period"`
	Vendors  []Vendor    `json:"vendors"`
	Total    money.Cents `json:"totalCents"`
	Currency string      `json:"currency"`
}

// Costs reads commerce's vendor-COGS god-view (GET /v1/costs) for a period — the
// SINGLE source of truth for what we pay every vendor. It authenticates with the
// admin S2S service token (no IAM user identity) and is org-INDEPENDENT, so it
// sends no subject. Zero (not an error) when commerce is unwired.
func (c *Client) Costs(ctx context.Context, period string) (Costs, error) {
	var out Costs
	if !c.Ready() {
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

// Receipt is the result of a Deposit — the transaction id of the credit that landed.
type Receipt struct {
	TxID     string      `json:"transactionId"`
	Amount   money.Cents `json:"amount"`
	Currency string      `json:"currency"`
}

// Deposit grants credit to a subject's wallet (POST /v1/billing/deposit) — the ONE
// money-in primitive. Symmetric with Credits: the same X-Org-Id namespace + `user`
// subject the reads resolve. amount must be positive (the handler validates + caps).
//
// idempotencyKey, when non-empty, is sent as X-Idempotency-Key so commerce dedupes a
// retried deposit AT MOST ONCE (a completed key REPLAYS the stored receipt, an in-flight
// key 409s; scoped billing-deposit:<subject>). This closes the commit-then-timeout double
// -credit: cloud's 15s client can time out AFTER commerce committed, and a retry carrying
// the SAME key lands nothing new. An EMPTY key preserves the additive default (distinct
// deposits to the same subject are legitimately cumulative) — commerce never dedupes by
// amount. See commerce api/billing/deposit.go.
func (c *Client) Deposit(ctx context.Context, subject string, amount money.Cents, currency, notes, tags, idempotencyKey string) (Receipt, error) {
	var out Receipt
	if !c.Ready() {
		return out, errUnconfigured
	}
	if currency == "" {
		currency = "usd"
	}
	body, err := json.Marshal(map[string]any{
		"user":     subject,
		"currency": currency,
		"amount":   amount,
		"notes":    notes,
		"tags":     tags,
	})
	if err != nil {
		return out, err
	}
	respBody, err := c.post(ctx, "/v1/billing/deposit", subject, body, idempotencyKey)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return out, fmt.Errorf("commerce deposit decode: %w", err)
	}
	return out, nil
}

// CreateCreditGrant forwards a credit-grant request verbatim to commerce's
// mint-gated POST /v1/billing/credit-grants (CreateCreditGrant), authenticated
// by the admin service token, with subject as the target-org namespace selector.
// Commerce is the sole credit-grant ledger; this relays its contract untouched
// (the raw response is returned to the caller) so the admin surface stays thin.
func (c *Client) CreateCreditGrant(ctx context.Context, subject string, body []byte, idempotencyKey string) ([]byte, error) {
	if !c.Ready() {
		return nil, errUnconfigured
	}
	return c.post(ctx, "/v1/billing/credit-grants", subject, body, idempotencyKey)
}

// ── SaaS-metrics god-view (fleet-wide, org-independent) ──────────────────────

// SaaSMetrics mirrors commerce's GET /v1/metrics/saas snapshot — the whole-business
// SaaS-operations aggregate (MRR/ARR, new/churn, plan mix, top customers, recent
// movements) computed IN commerce across every org namespace. It is org-INDEPENDENT
// (like Costs) so the reader sends NO subject. Only the fields the admin god-view
// renders are modeled; commerce fields we don't consume (upgrades/downgrades,
// untagged-request counts) are simply ignored by the decoder.
type SaaSMetrics struct {
	AsOf      string         `json:"asOf"`
	Currency  string         `json:"currency"`
	Window    string         `json:"window"`
	Revenue   SaaSRevenue    `json:"revenue"`
	Subs      SaaSSubs       `json:"subscriptions"`
	Usage     SaaSUsage      `json:"usage"`
	Customers []SaaSCustomer `json:"customers"`
	Orgs      int            `json:"orgs"`
	Gaps      []string       `json:"gaps"`
}

// SaaSRevenue is the recurring-revenue headline (run-rate MRR/ARR + windowed movement).
type SaaSRevenue struct {
	MRRCents            money.Cents    `json:"mrrCents"`
	ARRCents            money.Cents    `json:"arrCents"`
	ActiveSubscriptions int            `json:"activeSubscriptions"`
	PayingCustomers     int            `json:"payingCustomers"`
	Trials              int            `json:"trials"`
	NewMRRCents         money.Cents    `json:"newMrrCents"`
	ChurnedMRRCents     money.Cents    `json:"churnedMrrCents"`
	NetNewMRRCents      money.Cents    `json:"netNewMrrCents"`
	ByCategory          []SaaSCategory `json:"byCategory"`
}

// SaaSCategory is one plan-category bucket of run-rate MRR (the plan mix).
type SaaSCategory struct {
	Category      string      `json:"category"`
	MRRCents      money.Cents `json:"mrrCents"`
	Subscriptions int         `json:"subscriptions"`
}

// SaaSSubs is the subscription-operations panel (per-plan mix, trials, new/canceled,
// recent movements).
type SaaSSubs struct {
	ByPlan       []SaaSPlan  `json:"byPlan"`
	TrialsActive int         `json:"trialsActive"`
	New          int         `json:"new"`
	Canceled     int         `json:"canceled"`
	Recent       []SaaSEvent `json:"recent"`
}

// SaaSPlan is one plan's active/trialing counts, seats, and MRR contribution.
type SaaSPlan struct {
	Plan     string      `json:"plan"`
	Name     string      `json:"name"`
	Category string      `json:"category"`
	Active   int         `json:"active"`
	Trialing int         `json:"trialing"`
	Seats    int         `json:"seats"`
	MRRCents money.Cents `json:"mrrCents"`
}

// SaaSEvent is one recent subscription movement ("created" or "canceled").
type SaaSEvent struct {
	At            string      `json:"at"`
	Org           string      `json:"org"`
	Type          string      `json:"type"`
	Plan          string      `json:"plan"`
	Category      string      `json:"category"`
	MRRDeltaCents money.Cents `json:"mrrDeltaCents"`
}

// SaaSUsage is the metered / pay-as-you-go revenue headline for the window.
type SaaSUsage struct {
	Instrumented     bool        `json:"instrumented"`
	WindowUsageCents money.Cents `json:"windowUsageCents"`
	Requests         int64       `json:"requests"`
}

// SaaSCustomer is one top customer by MRR + windowed usage.
type SaaSCustomer struct {
	Org        string      `json:"org"`
	Plan       string      `json:"plan"`
	Category   string      `json:"category"`
	Status     string      `json:"status"`
	MRRCents   money.Cents `json:"mrrCents"`
	UsageCents money.Cents `json:"usageCents"`
	Seats      int         `json:"seats"`
	Since      string      `json:"since,omitempty"`
}

// Metrics reads the fleet SaaS-operations god-view (GET /v1/metrics/saas). Like Costs it
// is org-INDEPENDENT — the engine walks every org namespace itself — so it authenticates
// with the admin S2S service token and sends NO subject. Empty (not an error) when
// commerce is unwired, so a partial deploy degrades to an honest empty snapshot.
func (c *Client) Metrics(ctx context.Context, window string, limit int) (SaaSMetrics, error) {
	var out SaaSMetrics
	if !c.Ready() {
		return out, nil
	}
	q := url.Values{}
	if window != "" {
		q.Set("window", window)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	body, err := c.get(ctx, "/v1/metrics/saas", q, "")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("commerce metrics decode: %w", err)
	}
	return out, nil
}

// ── billing invoices + subscriptions (per-subject fleet rows) ────────────────

// Invoice is one issued invoice as the fleet god-view renders it: the id (for a future
// /v1/billing/invoices/:id detail fetch), the human number, status, amount due,
// currency, and the issue/due dates. Sourced from GET /v1/billing/invoices
// (invoiceResponse); all timestamps are RFC3339 strings.
type Invoice struct {
	ID        string      `json:"id"`
	Number    string      `json:"numberStr"`
	Status    string      `json:"status"`
	AmountDue money.Cents `json:"amountDue"`
	Currency  string      `json:"currency"`
	Issued    string      `json:"createdAt"`
	Due       string      `json:"dueDate"`
}

// Invoices lists a subject's invoices (GET /v1/billing/invoices), optionally filtered by
// status. The subject selects the org's billing namespace via X-Org-Id (trusted only
// after the service-token bearer verifies). Empty (not an error) when commerce is unwired.
func (c *Client) Invoices(ctx context.Context, subject, status string) ([]Invoice, error) {
	if !c.Ready() {
		return nil, nil
	}
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	body, err := c.get(ctx, "/v1/billing/invoices", q, subject)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Invoices []Invoice `json:"invoices"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("commerce invoices decode: %w", err)
	}
	return wrap.Invoices, nil
}

// Subscription is one subscription row the fleet god-view renders: the id, the buyer
// (userId), plan tier, status, monthly-normalized MRR, and the current-period
// start/end (started/renews). MRR reuses monthlyNormalized so a yearly plan is
// comparable to a monthly one in the fleet total.
type Subscription struct {
	ID      string      `json:"id"`
	User    string      `json:"user"`
	Plan    string      `json:"plan"`
	Status  string      `json:"status"`
	MRR     money.Cents `json:"mrrCents"`
	Started string      `json:"started"`
	Renews  string      `json:"renews"`
}

// subscriptionRowWire is the /v1/billing/subscriptions row shape the fleet view folds —
// richer than subscriptionsWire (which Plan() uses for the MRR sum alone).
type subscriptionRowWire struct {
	ID          string `json:"id"`
	UserID      string `json:"userId"`
	PlanID      string `json:"planId"`
	Status      string `json:"status"`
	Created     string `json:"createdAt"`
	PeriodStart string `json:"currentPeriodStart"`
	PeriodEnd   string `json:"currentPeriodEnd"`
	Plan        struct {
		Name     string      `json:"name"`
		Price    money.Cents `json:"price"`
		Interval string      `json:"interval"`
	} `json:"plan"`
}

// Subscriptions lists a subject's subscriptions (GET /v1/billing/subscriptions),
// optionally filtered by status, as fleet rows with a monthly-normalized MRR. Empty (not
// an error) when commerce is unwired.
func (c *Client) Subscriptions(ctx context.Context, subject, status string) ([]Subscription, error) {
	if !c.Ready() {
		return nil, nil
	}
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	body, err := c.get(ctx, "/v1/billing/subscriptions", q, subject)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Subscriptions []subscriptionRowWire `json:"subscriptions"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("commerce subscriptions decode: %w", err)
	}
	out := make([]Subscription, 0, len(wrap.Subscriptions))
	for _, s := range wrap.Subscriptions {
		name := strings.TrimSpace(s.Plan.Name)
		if name == "" {
			name = strings.TrimSpace(s.PlanID)
		}
		started := strings.TrimSpace(s.Created)
		if started == "" {
			started = s.PeriodStart
		}
		out = append(out, Subscription{
			ID:      s.ID,
			User:    s.UserID,
			Plan:    name,
			Status:  s.Status,
			MRR:     monthlyNormalized(s.Plan.Price, s.Plan.Interval),
			Started: started,
			Renews:  s.PeriodEnd,
		})
	}
	return out, nil
}

// post performs one admin-authenticated commerce POST (JSON body) and returns the
// raw response. The admin S2S service token is the bearer and X-Org-Id=<subject>
// the per-org namespace selector commerce's EdgeAuth trusts only after verifying
// the service token. idempotencyKey, when non-empty, is sent as X-Idempotency-Key so a
// retried write dedupes at commerce. A non-2xx is an error the caller surfaces + audits
// honestly.
func (c *Client) post(ctx context.Context, path, subject string, body []byte, idempotencyKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if subject != "" {
		req.Header.Set("X-Org-Id", subject)
	}
	if idempotencyKey != "" {
		req.Header.Set("X-Idempotency-Key", idempotencyKey)
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

// Forward proxies an admin-authenticated request to commerce VERBATIM and returns
// the raw body + status. It is the ONE seam a SuperAdmin surface drives commerce's
// own endpoints through — the platform plan-promo config (/v1/platform/promo) and a
// per-org spend-alert override (/v1/billing/spend-alerts) — without a typed method
// per shape. subject is the X-Org-Id namespace selector (the target org for a cap
// override, or the admin org for platform config); body is nil for GET/DELETE. The
// status is returned so the caller surfaces commerce's OWN verdict (400 validation,
// 403, 404) instead of flattening every non-2xx into one code.
func (c *Client) Forward(ctx context.Context, method, path, subject string, body []byte) ([]byte, int, error) {
	if !c.Ready() {
		return nil, 0, errUnconfigured
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if subject != "" {
		req.Header.Set("X-Org-Id", subject)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// get performs one admin-authenticated commerce GET and returns the raw body.
func (c *Client) get(ctx context.Context, path string, q url.Values, subject string) ([]byte, error) {
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
	if subject != "" {
		// Commerce's EdgeAuth trusts X-Org-Id ONLY after it verifies the bearer is the
		// COMMERCE_SERVICE_TOKEN, then resolves the per-org billing namespace from it.
		req.Header.Set("X-Org-Id", subject)
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
