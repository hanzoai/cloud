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
// Commerce is a PLUGIN in this binary, not a deployment of its own. Plan reads it
// over the internal plane — a call by name on commerce's own socket, which cannot
// reach the public edge by accident. The reads still on HTTP below are the ones
// whose plane ops do not exist yet; each crosses the commerce transport, which
// carries the platform's identity, and names its tenant in X-Org-Id. A per-subject
// read resolves the org's billing namespace from that header AND keys the wallet
// under the bare slug — one value, the subject, is both. The fleet Costs god-view
// is org-independent and sends no subject.
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

	"github.com/hanzoai/authz"
	"github.com/hanzoai/commerce/models/subscription"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
)

// errUnconfigured marks a write (Deposit) attempted against an unwired commerce.
var errUnconfigured = errors.New("commerce not configured")

// Client reads the commerce billing plane.
type Client struct {
	base string // e.g. http://commerce.hanzo.svc.cluster.local:8001
	http *http.Client
}

// New builds a commerce client for base + admin S2S token. The HTTP client uses the
// commerce transport self-routing dispatch: when commerce is CO-RESIDENT (base is the
// commerce.inproc placeholder) it dispatches in-process — a plain http.Client would
// instead DNS-resolve "commerce.inproc" and fail "no such host", silently breaking the
// admin cost/finance god-view. For a split-deploy (a real commerce URL) it falls
// through to plain HTTP unchanged.
func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(strings.TrimSpace(base), "/"),
		http: transport.Client(15 * time.Second),
	}
}

// Ready reports whether a commerce endpoint is wired on this deployment.
func (c *Client) Ready() bool { return c != nil && c.base != "" }

// Spend is a subject's month-to-date consumption.
// The month-to-date read and the balance read used to live here, over commerce's own
// HTTP /v1/billing/*. Neither reaches anything: those routes are behind `//go:build
// cloud` and are compiled into no binary here, so usage/rollup was an unrouted 404 and
// balance re-entered the CUSTOMER handler with no principal. The money question is asked
// by name now (core.OrgMoney → plane.FinanceSpend), which answers both halves at once
// from the process that owns the ledger. Nothing was left behind for the next reader to
// call by mistake.

// Plan is a subject's subscription: the active tier, the monthly-normalized
// recurring revenue, and whether any subscription is active. Name is
// "pay-as-you-go" for a metered subject with no active subscription (the honest
// default, never a fabricated tier).
type Plan struct {
	Name   string
	MRR    money.Cents
	Active bool
}

// Plan reads a subject's subscription tier + MRR in ONE decode (GET
// /v1/billing/subscriptions), so the customer + revenue surfaces share a single
// upstream read. MRR counts what commerce says counts; "active"/"trialing" both
// mark the subject subscribed. Honest zero/"pay-as-you-go" (not an error) when
// commerce is unwired.
func (c *Client) Plan(ctx context.Context, subject string) (Plan, error) {
	out := Plan{Name: "pay-as-you-go"}
	if !c.Ready() {
		return out, nil
	}
	// The org is the tenant, and the tenant IS the scope: commerce reads this list
	// out of that org's own namespace, so every row already belongs to the subject.
	//
	// No UserID filter, deliberately. The HTTP call this replaces sent `?user=`,
	// and the endpoint reads `?userId=` — so the filter never applied and this has
	// always folded over the org's whole list. Passing one now would silently
	// narrow a number the cockpit has been showing for as long as it has shown it.
	reply, err := commercepeer.FinanceSubs(cloud.For(ctx, subject), &plane.SubsIn{})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			// The router answered from the manifest it owns: this deployment runs
			// no commerce. Honest pay-as-you-go, which is what an unwired commerce
			// has always read as here.
			return out, nil
		}
		return out, fmt.Errorf("commerce plan read: %w", err)
	}
	if reply == nil {
		return out, nil
	}
	for _, s := range reply.Rows {
		// Revenue and entitlement are two questions, and this loop answers
		// both. commerce owns the revenue one — Status.CountsTowardMRR — so
		// this surface and commerce's own rollup can no longer report a
		// different MRR for the same account. They did: this counted trials as
		// revenue and the rollup did not, so the money board and the SaaS board
		// disagreed by the whole trial cohort.
		//
		// A trial is not revenue, but it IS a live plan, so it still names the
		// plan and marks the subject subscribed.
		if subscription.Status(s.Status).CountsTowardMRR() {
			out.MRR += money.Cents(s.MRRCents)
		}
		switch strings.ToLower(strings.TrimSpace(s.Status)) {
		case "active", "trialing":
			out.Active = true
			if name := strings.TrimSpace(s.PlanName); name != "" && out.Name == "pay-as-you-go" {
				out.Name = name
			}
		}
	}
	return out, nil
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
	// Name is who we pay — the vendor's own name ("digitalocean", "anthropic"). The wire
	// spells it `vendor`; a line is identified by this together with Service.
	Name string `json:"vendor"`
	// Service is what we pay them FOR, so one vendor can bill several lines. Commerce
	// chooses the vocabulary; this surface reports it verbatim.
	Service string `json:"service"`
	// Amount is the period's cost in USD cents, POSITIVE — money out is not negated
	// here. Summed across lines it is the platform COGS the margin subtracts.
	Amount money.Cents `json:"amountCents"`
	Source string      `json:"source"` // "actual" | "estimated"
	// Note is commerce's own caveat on the line — usually how an estimate was reached.
	// Omitted when there is none.
	Note string `json:"note,omitempty"`
}

// Costs is the fleet COGS god-view: every vendor line for a period plus the total.
type Costs struct {
	Period   string      `json:"period"`
	Vendors  []Vendor    `json:"vendors"`
	Total    money.Cents `json:"totalCents"`
	Currency string      `json:"currency"`
}

// Costs reads commerce's vendor-COGS god-view (GET /v1/costs) for a period — the
// SINGLE source of truth for what we pay every vendor. It is org-INDEPENDENT, so
// it sends no subject. Zero (not an error) when commerce is unwired.
func (c *Client) Costs(ctx context.Context, period string) (Costs, error) {
	var out Costs
	// The platform's own books live in the reserved admin org, and this read is a
	// fleet god-view rather than a tenant one — so it names that org rather than
	// a subject.
	reply, err := commercepeer.FinanceCosts(cloud.For(ctx, authz.AdminOrg), &plane.CostsIn{Period: period})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			// This deployment runs no commerce. An honest zero, which is what an
			// unwired commerce has always read as here.
			return out, nil
		}
		return out, fmt.Errorf("commerce costs read: %w", err)
	}
	if reply == nil {
		return out, nil
	}
	out.Period, out.Total, out.Currency = reply.Period, money.Cents(reply.TotalCents), reply.Currency
	out.Vendors = make([]Vendor, 0, len(reply.Vendors))
	for _, v := range reply.Vendors {
		out.Vendors = append(out.Vendors, Vendor{
			Name: v.Vendor, Service: v.Service, Amount: money.Cents(v.AmountCents),
			Source: v.Source, Note: v.Note,
		})
	}
	return out, nil
}

// Receipt is the result of a Deposit — the transaction id of the credit that landed.
type Receipt struct {
	TxID     string      `json:"transactionId"`
	Amount   money.Cents `json:"amount"`
	Currency string      `json:"currency"`
}

// Forward proxies an admin-authenticated request to commerce VERBATIM and returns
// the raw body + status. It is the ONE client a SuperAdmin surface drives commerce's
// own endpoints through — the platform plan-promo config (/v1/platform/promo) and a
// per-org spend-alert override (/v1/billing/alerts) — without a typed method
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
	if subject != "" {
		// Commerce resolves the per-org billing namespace from X-Org-Id, trusting it
		// because the transport carried the platform's identity across the hop.
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
