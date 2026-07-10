package costs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// doAPIBase is the DigitalOcean API root. The billing endpoints are stable v2
// REST: GET /v2/customers/my/balance and GET /v2/customers/my/billing_history.
const doAPIBase = "https://api.digitalocean.com"

// doAPIBaseOverride, when non-empty, replaces doAPIBase — a test seam so the DO
// client can be pointed at an httptest server. Empty in production.
var doAPIBaseOverride string

func doBase() string {
	if doAPIBaseOverride != "" {
		return doAPIBaseOverride
	}
	return doAPIBase
}

// doToken reads the DigitalOcean API token from the environment. In production
// this is injected by KMS: the `DO_API_TOKEN` key of the `shared-credentials`
// secret (KMSSecret hanzo/shared-credentials, path /shared-credentials) — the
// same token the DOKS/visor stack uses. NEVER inlined, NEVER logged, NEVER
// returned to a client.
func doToken() string {
	for _, k := range []string{"DO_API_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// doBillingHistory mirrors the DO billing_history response. Amounts are decimal
// USD strings (e.g. "12.34"); Type is "Invoice"|"Payment"|"Credit". Only invoice
// rows in the period count as our spend.
type doBillingHistory struct {
	BillingHistory []struct {
		Description string `json:"description"`
		Amount      string `json:"amount"`
		InvoiceID   string `json:"invoice_id"`
		InvoiceUUID string `json:"invoice_uuid"`
		Date        string `json:"date"`
		Type        string `json:"type"`
	} `json:"billing_history"`
}

// digitalOceanCost pulls DigitalOcean spend for the period from the DO billing API
// (source=actual). With no token configured it returns an honest estimated-0 line
// (source=estimated, a note) rather than failing the whole report — the other
// vendors still render. Any transport/decoding error degrades the same way.
//
// The period's spend = the sum of Invoice rows dated within [start,end). DO issues
// one monthly invoice; summing invoice-typed rows in the window is the actual
// amount billed to us for that month.
func digitalOceanCost(ctx context.Context, client *http.Client, p string) VendorCost {
	line := VendorCost{Vendor: "digitalocean", Service: "compute", Period: p, Currency: "usd"}

	token := doToken()
	if token == "" {
		line.Source = SourceEstimated
		line.Note = "no DO_API_TOKEN configured (KMS shared-credentials) — actual spend unavailable"
		return line
	}

	body, err := doGet(ctx, client, token, "/v2/customers/my/billing_history")
	if err != nil {
		line.Source = SourceEstimated
		line.Note = "DigitalOcean billing API unreachable — actual spend unavailable"
		return line
	}

	var hist doBillingHistory
	if err := json.Unmarshal(body, &hist); err != nil {
		line.Source = SourceEstimated
		line.Note = "DigitalOcean billing response unrecognized — actual spend unavailable"
		return line
	}

	start, end := periodBounds(p)
	var cents int64
	for _, row := range hist.BillingHistory {
		if !strings.EqualFold(row.Type, "Invoice") {
			continue
		}
		d, err := time.Parse(time.RFC3339, row.Date)
		if err != nil {
			continue
		}
		if d.Before(start) || !d.Before(end) {
			continue
		}
		cents += usdStringToCents(row.Amount)
	}

	line.AmountCents = cents
	line.Source = SourceActual
	return line
}

// doGet issues an authenticated GET to the DO API and returns the body. The token
// is sent as a Bearer header only; it is never placed in the URL or any log line.
func doGet(ctx context.Context, client *http.Client, token, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doBase()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do NOT echo the body verbatim (it may reflect request context); a bare
		// status is enough for the honest degraded path. The token is never here.
		return nil, fmt.Errorf("digitalocean: status %d", resp.StatusCode)
	}
	return body, nil
}

// usdStringToCents parses a decimal USD string ("12.34", "1,234.56", "-5") to
// cents. Non-negative result (a credit/negative row is ignored as spend). Returns
// 0 for an unparseable value.
func usdStringToCents(s string) int64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	if f < 0 {
		return 0
	}
	return int64(f*100 + 0.5)
}
