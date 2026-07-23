// Package do reads DigitalOcean's billing API for the finance dashboard's cost
// side. DO is our PRIMARY venue (a large promotional credit); this client turns
// the customer balance + billing history into money.Cents the finance aggregator
// folds into gross margin and runway.
//
// Auth is a single personal-access token, DO_API_TOKEN, sourced from a KMSSecret on
// the cloud env — NEVER hard-coded. When the token is unset the client is not Ready
// and every read reports the honest not-configured state.
//
// SIGN CONVENTION (from DO's public OpenAPI spec): GET /v2/customers/my/balance
// returns decimal-dollar strings; account_balance carries the accounts-receivable
// sign — POSITIVE = we OWE DO, NEGATIVE = we hold CREDIT. Our promo credit shows as
// a negative account balance, so credit-remaining = -Account. Dollars are converted
// to cents once at this edge; everything downstream is money.Cents.
package digitalocean

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

	"github.com/hanzoai/cloud/clients/admin/money"
)

// apiBase is DigitalOcean's public API host. Overridable in tests via NewWithBase.
const apiBase = "https://api.digitalocean.com"

// Client reads DigitalOcean billing with a personal-access token.
type Client struct {
	base  string
	token string // DO_API_TOKEN (secret; never logged)
	http  *http.Client
}

// New builds a DO client against the public API.
func New(token string) *Client { return NewWithBase(apiBase, token) }

// NewWithBase builds a DO client against base (a test may point it at a stub).
func NewWithBase(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Ready reports whether a DO token is present.
func (c *Client) Ready() bool { return c != nil && c.token != "" }

// Balance is the decoded /v2/customers/my/balance, in cents. Account carries DO's
// accounts-receivable sign (positive = owed to DO, negative = credit we hold).
type Balance struct {
	Account     money.Cents
	MonthToDate money.Cents
	Usage       money.Cents
	At          string
}

// balanceWire is the raw DO JSON (all money fields are decimal-dollar strings).
type balanceWire struct {
	MonthToDateBalance string `json:"month_to_date_balance"`
	AccountBalance     string `json:"account_balance"`
	MonthToDateUsage   string `json:"month_to_date_usage"`
	GeneratedAt        string `json:"generated_at"`
}

// Balance fetches the customer balance, converting every dollar string to cents.
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var out Balance
	if !c.Ready() {
		return out, fmt.Errorf("DO_API_TOKEN not configured")
	}
	body, err := c.get(ctx, "/v2/customers/my/balance")
	if err != nil {
		return out, err
	}
	var w balanceWire
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("do balance decode: %w", err)
	}
	return Balance{
		Account:     dollarsToCents(w.AccountBalance),
		MonthToDate: dollarsToCents(w.MonthToDateBalance),
		Usage:       dollarsToCents(w.MonthToDateUsage),
		At:          strings.TrimSpace(w.GeneratedAt),
	}, nil
}

// Entry is one billing-history row (used to build the credit burn-down series).
type Entry struct {
	Description string
	Amount      money.Cents
	Date        string
	Kind        string
	InvoiceID   string
}

// entryWire is the raw DO history row (amount is a decimal-dollar string).
type entryWire struct {
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Date        string `json:"date"`
	Type        string `json:"type"`
	InvoiceID   string `json:"invoice_id"`
}

// History fetches recent billing history. Used only for the burn-down series; a
// failure is non-fatal to the caller (it renders the balance tiles with no series).
func (c *Client) History(ctx context.Context, perPage int) ([]Entry, error) {
	if !c.Ready() {
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
		BillingHistory []entryWire `json:"billing_history"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("do billing_history decode: %w", err)
	}
	out := make([]Entry, len(w.BillingHistory))
	for i, e := range w.BillingHistory {
		out[i] = Entry{
			Description: e.Description,
			Amount:      dollarsToCents(e.Amount),
			Date:        e.Date,
			Kind:        e.Type,
			InvoiceID:   e.InvoiceID,
		}
	}
	return out, nil
}

// Volume is one DO block-storage volume: capacity + attachment + region. DO's API
// gives capacity and which droplets a volume is attached to, but NOT fill % — the
// caller enriches fill only where a filesystem source (the datastore's own
// system.disks) reports it, and renders an honest "—" everywhere else.
type Volume struct {
	ID         string
	Name       string
	Region     string
	SizeGiB    int
	DropletIDs []int
}

// volumeWire is the raw DO /v2/volumes row.
type volumeWire struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SizeGigabytes int    `json:"size_gigabytes"`
	Region        struct {
		Slug string `json:"slug"`
	} `json:"region"`
	DropletIDs []int `json:"droplet_ids"`
}

// Volumes lists ALL block-storage volumes across the account, following DO's
// page-number pagination (200/page, a hard 25-page cap so a runaway can never loop).
// The fleet inventory (count, capacity, monthly cost) is real; per-volume fill is NOT
// exposed by DO and stays absent (honest) until a filesystem source reports it.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("DO_API_TOKEN not configured")
	}
	var out []Volume
	for page := 1; page <= 25; page++ {
		body, err := c.get(ctx, "/v2/volumes?per_page=200&page="+strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		var w struct {
			Volumes []volumeWire `json:"volumes"`
			Meta    struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &w); err != nil {
			return nil, fmt.Errorf("do volumes decode: %w", err)
		}
		for _, v := range w.Volumes {
			out = append(out, Volume{
				ID:         v.ID,
				Name:       v.Name,
				Region:     v.Region.Slug,
				SizeGiB:    v.SizeGigabytes,
				DropletIDs: v.DropletIDs,
			})
		}
		// Stop on the last (short) page, or once we've collected the reported total.
		if len(w.Volumes) < 200 || (w.Meta.Total > 0 && len(out) >= w.Meta.Total) {
			break
		}
	}
	return out, nil
}

// get performs one token-authenticated DO GET and returns the raw body.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
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
// integer cents, rounding to the nearest cent. A blank/invalid string is 0 — DO
// always sends a value, so this only guards a malformed field, and zero there is
// the honest fallback (never a fabricated amount).
func dollarsToCents(s string) money.Cents {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return money.Cents(math.Round(f * 100))
}
