package costs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// openaiAPIBase is the OpenAI API root. The org-level costs endpoint
// (GET /v1/organization/costs) returns actual USD amounts bucketed by day; it
// requires an admin/org key. When the resold key lacks org-costs scope the call
// 401/403s and we fall back to the metered estimate (honest source label).
const openaiAPIBase = "https://api.openai.com"

// openaiAPIBaseOverride, when non-empty, replaces openaiAPIBase — a test seam for
// pointing the org-costs client at an httptest server. Empty in production.
var openaiAPIBaseOverride string

func openaiBase() string {
	if openaiAPIBaseOverride != "" {
		return openaiAPIBaseOverride
	}
	return openaiAPIBase
}

// openaiKey reads the OpenAI key from the environment — the `OPENAI_API_KEY` of
// the `cloud-api-llm-keys` secret (KMS llm-secrets, path /llm-secrets). Never
// inlined/logged/returned.
func openaiKey() string {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

// openaiCostsResponse mirrors GET /v1/organization/costs. Each bucket carries
// result rows whose amount.value is USD (float). We sum all buckets in the window.
type openaiCostsResponse struct {
	Data []struct {
		StartTime int64 `json:"start_time"`
		EndTime   int64 `json:"end_time"`
		Results   []struct {
			Amount struct {
				Value    float64 `json:"value"`
				Currency string  `json:"currency"`
			} `json:"amount"`
		} `json:"results"`
	} `json:"data"`
}

// openAICost pulls OpenAI spend for the period from the org-costs API
// (source=actual). If no key is set, the key lacks org-costs scope, or the API is
// unreachable, it returns the ledger-derived estimate (estimatedCents) with
// source=estimated and an honest note — never a fabricated number.
func openAICost(ctx context.Context, client *http.Client, p string, estimatedCents int64) VendorCost {
	line := VendorCost{Vendor: "openai", Service: "llm-inference", Period: p, Currency: "usd"}

	key := openaiKey()
	if key == "" {
		line.AmountCents = estimatedCents
		line.Source = SourceEstimated
		line.Note = "no OPENAI_API_KEY (cloud-api-llm-keys) — COGS estimated from metered tokens"
		return line
	}

	cents, err := openaiActualCents(ctx, client, key, p)
	if err != nil {
		line.AmountCents = estimatedCents
		line.Source = SourceEstimated
		line.Note = "OpenAI costs API unavailable (scope/reachability) — COGS estimated from metered tokens"
		return line
	}

	line.AmountCents = cents
	line.Source = SourceActual
	return line
}

// openaiActualCents queries GET /v1/organization/costs for the period and sums the
// bucketed USD amounts into cents.
func openaiActualCents(ctx context.Context, client *http.Client, key, p string) (int64, error) {
	start, end := periodBounds(p)
	q := url.Values{}
	q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
	q.Set("end_time", strconv.FormatInt(end.Unix(), 10))
	q.Set("bucket_width", "1d")
	q.Set("limit", "180")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openaiBase()+"/v1/organization/costs?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("openai: status %d", resp.StatusCode)
	}

	var cr openaiCostsResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return 0, err
	}

	var usd float64
	for _, b := range cr.Data {
		bt := time.Unix(b.StartTime, 0).UTC()
		if bt.Before(start) || !bt.Before(end) {
			continue
		}
		for _, r := range b.Results {
			usd += r.Amount.Value
		}
	}
	return int64(usd*100 + 0.5), nil
}
