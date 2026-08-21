package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// report_googleads.go pulls Google Ads' own numbers back through the reporting
// API — the complement of the offline-conversion upload googleads.go pushes. Same
// platform slug, so it reads the SAME composite OAuth2 secret and the SAME
// customerId the upload already needs: reporting adds no configuration and no
// second credential.
//
// The report is GAQL, and the only variable parts of the statement are the
// window's two dates, each rendered from a time.Time. Nothing a caller typed
// reaches it.

// googleQuery is the report, at the campaign-day grain.
//
// metrics.cost_micros is Google's money unit — a millionth of the account
// currency — and customer.currency_code names that currency, so the two are
// selected TOGETHER. A cost without its currency is a number that means nothing,
// and joining it back from a second request would be a second thing to get wrong.
const googleQuery = `SELECT campaign.id, campaign.name, segments.date, customer.currency_code, ` +
	`metrics.cost_micros, metrics.impressions, metrics.clicks, ` +
	`metrics.conversions, metrics.conversions_value ` +
	`FROM campaign WHERE segments.date BETWEEN '%s' AND '%s'`

// googleMicros is cost_micros per unit of currency.
const googleMicros = 1e6

// googlePageSize is the API's maximum rows per page. Fewer, larger pages is the
// cheaper shape for a grain this coarse.
const googlePageSize = 10000

type googleadsReport struct{}

func init() { registerReporter(googleadsReport{}) }

func (googleadsReport) ID() string { return googleadsID }

// googleRow is one campaign-day of the search response. Google renders an int64
// as a JSON STRING and a double as a JSON NUMBER (proto3's encoding), so the
// counts and the money arrive quoted while the conversion figures do not — which
// is why they are typed differently here rather than uniformly.
type googleRow struct {
	Campaign struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"campaign"`
	Customer struct {
		Currency string `json:"currencyCode"`
	} `json:"customer"`
	Segments struct {
		Date string `json:"date"`
	} `json:"segments"`
	Metrics struct {
		CostMicros  string  `json:"costMicros"`
		Impressions string  `json:"impressions"`
		Clicks      string  `json:"clicks"`
		Conversions float64 `json:"conversions"`
		Value       float64 `json:"conversionsValue"`
	} `json:"metrics"`
}

func (d googleadsReport) Report(ctx context.Context, cfg Config, secret string, w Window) ([]Metric, error) {
	customer := cfg.get("customerId")
	if customer == "" {
		return nil, fmt.Errorf("google-ads: customerId is required")
	}
	var c googleCreds
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &c); err != nil {
		return nil, fmt.Errorf("google-ads: credentials must be a JSON object {developer_token, client_id, client_secret, refresh_token}")
	}
	if !c.complete() {
		return nil, fmt.Errorf("google-ads: OAuth2 credentials are incomplete")
	}
	token, err := googleAccessToken(ctx, c)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + token,
		"developer-token": c.DeveloperToken,
	}
	// The manager account, when the report runs under an MCC; else the customer
	// itself — the same rule the upload keeps.
	if login := cfg.get("loginCustomerId"); login != "" {
		headers["login-customer-id"] = login
	}
	endpoint := googleAdsAPI + "/customers/" + customer + "/googleAds:search"
	query := fmt.Sprintf(googleQuery, day(w.Start), day(w.End))

	out := make([]Metric, 0, 256)
	pageToken := ""
	for range maxPages {
		body := map[string]any{"query": query, "pageSize": googlePageSize}
		if pageToken != "" {
			body["pageToken"] = pageToken
		}
		var resp struct {
			Results       []googleRow `json:"results"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := postJSON(ctx, googleadsID, endpoint, headers, body, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			d, err := time.Parse("2006-01-02", r.Segments.Date)
			if err != nil {
				continue // a row Google did not date cannot be placed on a day
			}
			out = append(out, Metric{
				Day:         d,
				Campaign:    r.Campaign.ID,
				Name:        r.Campaign.Name,
				Currency:    r.Customer.Currency,
				Spend:       num(r.Metrics.CostMicros) / googleMicros,
				Impressions: int64(num(r.Metrics.Impressions)),
				Clicks:      int64(num(r.Metrics.Clicks)),
				Conversions: r.Metrics.Conversions,
				Revenue:     r.Metrics.Value,
			})
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		pageToken = resp.NextPageToken
	}
	return nil, fmt.Errorf("google-ads: report did not end after %d pages", maxPages)
}
