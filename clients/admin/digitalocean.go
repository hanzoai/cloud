package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// doClient reads DigitalOcean's billing API for the finance dashboard's cost
// side. DO is our PRIMARY venue (a ~$40k promotional credit); this client turns
// the customer balance + billing history into the cents the finance aggregator
// folds into gross margin and runway.
//
// Auth is a single personal-access token, DO_API_TOKEN, sourced from a KMSSecret
// on the cloud env — NEVER hard-coded (kms.hanzo.ai is the only secret store).
// When the token is unset the client is UNCONFIGURED and every read reports the
// honest not-configured state; the finance endpoint then returns
// cost.digitalocean = {configured:false} rather than a fabricated number.
//
// DIGITALOCEAN SIGN CONVENTION (authoritative, from DO's public OpenAPI spec):
// GET /v2/customers/my/balance returns three DECIMAL-DOLLAR STRINGS —
//   - account_balance:      most-recent billing balance, accounts-receivable sign.
//     POSITIVE = the customer OWES DO; NEGATIVE = the customer
//     holds CREDIT (DO owes us). Our promo credit shows as a
//     NEGATIVE account_balance, so credit-remaining = -account_balance.
//   - month_to_date_usage:  spend in the current billing period (positive dollars).
//   - month_to_date_balance = account_balance + month_to_date_usage.
//
// We convert dollars→cents once at the edge and work in int64 cents everywhere after.
type doClient struct {
	base  string // DO API base; https://api.digitalocean.com in prod
	token string // DO_API_TOKEN (secret; never logged)
	http  *http.Client
}

// doAPIBase is DigitalOcean's public API host. Overridable in tests via
// newDOClientWithBase so a fake server can stand in.
const doAPIBase = "https://api.digitalocean.com"

func newDOClient(token string) *doClient {
	return newDOClientWithBase(doAPIBase, token)
}

func newDOClientWithBase(base, token string) *doClient {
	return &doClient{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// configured reports whether a DO token is present. Unconfigured → the finance
// endpoint returns cost.digitalocean = {configured:false}, never a fake balance.
func (c *doClient) configured() bool { return c != nil && c.token != "" }

// doBalance is the decoded /v2/customers/my/balance response. Dollars are parsed
// into cents at decode time so no float dollars leak past this boundary.
type doBalance struct {
	// AccountBalanceCents mirrors DO's account_balance (accounts-receivable sign:
	// positive = owed to DO, negative = credit we hold).
	AccountBalanceCents     int64
	MonthToDateBalanceCents int64
	MonthToDateUsageCents   int64
	GeneratedAt             string
}

// doBalanceWire is the raw DO JSON (all money fields are decimal-dollar strings).
type doBalanceWire struct {
	MonthToDateBalance string `json:"month_to_date_balance"`
	AccountBalance     string `json:"account_balance"`
	MonthToDateUsage   string `json:"month_to_date_usage"`
	GeneratedAt        string `json:"generated_at"`
}

// balance fetches the customer balance and converts every dollar string to cents.
func (c *doClient) balance(ctx context.Context) (doBalance, error) {
	var out doBalance
	if !c.configured() {
		return out, fmt.Errorf("DO_API_TOKEN not configured")
	}
	body, err := c.get(ctx, "/v2/customers/my/balance")
	if err != nil {
		return out, err
	}
	var w doBalanceWire
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("do balance decode: %w", err)
	}
	out = doBalance{
		AccountBalanceCents:     dollarsToCents(w.AccountBalance),
		MonthToDateBalanceCents: dollarsToCents(w.MonthToDateBalance),
		MonthToDateUsageCents:   dollarsToCents(w.MonthToDateUsage),
		GeneratedAt:             strings.TrimSpace(w.GeneratedAt),
	}
	return out, nil
}

// doHistoryEntry is one row of the billing history (used to build the burn-down
// timeseries). amount is a decimal-dollar string in DO's wire.
type doHistoryEntry struct {
	Description string `json:"description"`
	AmountCents int64  `json:"-"`
	Amount      string `json:"amount"`
	Date        string `json:"date"`
	Type        string `json:"type"`
	InvoiceID   string `json:"invoice_id"`
}

// history fetches recent billing history (Invoice/Credit/Payment entries). Used
// only to render the credit burn-down series; a failure here is non-fatal (the
// finance endpoint still returns the balance-derived tiles with an empty series).
func (c *doClient) history(ctx context.Context, perPage int) ([]doHistoryEntry, error) {
	if !c.configured() {
		return nil, fmt.Errorf("DO_API_TOKEN not configured")
	}
	if perPage <= 0 {
		perPage = 50
	}
	body, err := c.get(ctx, "/v2/customers/my/billing_history?per_page="+strconv.Itoa(perPage))
	if err != nil {
		return nil, err
	}
	var w struct {
		BillingHistory []doHistoryEntry `json:"billing_history"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("do billing_history decode: %w", err)
	}
	for i := range w.BillingHistory {
		w.BillingHistory[i].AmountCents = dollarsToCents(w.BillingHistory[i].Amount)
	}
	return w.BillingHistory, nil
}

// get performs one token-authenticated DO GET and returns the raw body.
func (c *doClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("digitalocean unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("digitalocean status %d", resp.StatusCode)
	}
	return body, nil
}

// dollarsToCents parses a DO decimal-dollar string ("23.44", "-40000.00") into
// integer cents, rounding to the nearest cent. A blank/invalid string is 0 —
// DO always sends a value, so this only guards against a malformed field, and a
// zero there is the honest fallback (never a fabricated amount).
func dollarsToCents(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 100))
}
