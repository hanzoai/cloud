package destination

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// report_meta.go pulls Meta's own numbers back through the Insights API — the
// reporting complement of the Conversions API meta.go pushes to. Same platform
// slug, so it reads the SAME per-org credential: a CAPI token from Events
// Manager, else the org's meta_ads OAuth token, through the custody resolveSecret
// already knows.
//
// WHICH AD ACCOUNT is not configuration. The token is the authority on which
// accounts an org can read, so the pull asks it (/me/adaccounts) rather than
// carrying an account id that can be typed wrong, go stale, or name an account
// the org no longer runs. A token without ads_read reads no account and the pull
// says exactly that, which is the honest answer for a credential that cannot see
// spend.
//
// The token rides an Authorization header, never the query string. An insights
// URL is long and ends up in logs, which is precisely where a credential must not
// be — and it is why paging follows Meta's CURSOR on our own endpoint rather than
// the absolute `next` URL Meta hands back.

// metaInsightFields is what one campaign-day must carry to become a Metric.
var metaInsightFields = strings.Join([]string{
	"campaign_id", "campaign_name", "spend", "impressions", "clicks",
	"account_currency", "actions", "action_values",
}, ",")

// metaPurchase is the ORDERED preference of Meta's purchase action types.
// omni_purchase is Meta's OWN deduplication across pixel, app and offline, so it
// is the honest count whenever an account reports it, and the pixel-only type is
// the fallback for an account that does not. Summing them would count one
// purchase twice — which is why this reads as a preference and never as a total.
var metaPurchase = []string{"omni_purchase", "offsite_conversion.fb_pixel_purchase", "purchase"}

type metaReport struct{}

func init() { registerReporter(metaReport{}) }

func (metaReport) ID() string { return metaID }

// metaAction is one entry of Meta's action breakdown: a type and its value,
// quoted the way Meta quotes every number.
type metaAction struct {
	Type  string `json:"action_type"`
	Value string `json:"value"`
}

// metaInsight is one campaign-day of the Insights response.
type metaInsight struct {
	CampaignID   string       `json:"campaign_id"`
	CampaignName string       `json:"campaign_name"`
	DateStart    string       `json:"date_start"`
	Spend        string       `json:"spend"`
	Impressions  string       `json:"impressions"`
	Clicks       string       `json:"clicks"`
	Currency     string       `json:"account_currency"`
	Actions      []metaAction `json:"actions"`
	Values       []metaAction `json:"action_values"`
}

// metaPage is the envelope Meta wraps every collection in.
type metaPage[T any] struct {
	Data   []T `json:"data"`
	Paging struct {
		Cursors struct {
			After string `json:"after"`
		} `json:"cursors"`
		Next string `json:"next"`
	} `json:"paging"`
}

// metaPick returns the first PREFERRED action type present, as a number. Absent
// ⇒ 0: a campaign with no purchases reported none, which is a fact and not a
// failure.
func metaPick(as []metaAction) float64 {
	for _, want := range metaPurchase {
		for _, a := range as {
			if a.Type == want {
				return num(a.Value)
			}
		}
	}
	return 0
}

func (d metaReport) Report(ctx context.Context, _ Config, secret string, w Window) ([]Metric, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("meta: access_token is required")
	}
	accounts, err := metaAccounts(ctx, secret)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("meta: this credential reads no ad account; reporting spend needs an ads_read token")
	}
	out := make([]Metric, 0, len(accounts)*32)
	for _, account := range accounts {
		// An account that fails FAILS THE PULL rather than being skipped: a
		// partial answer here is under-reported spend, and under-reported spend
		// silently inflates every return-on-spend figure computed from it.
		ms, err := metaInsights(ctx, secret, account, w)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	return out, nil
}

// metaAccounts asks the token which ad accounts it can read.
func metaAccounts(ctx context.Context, secret string) ([]string, error) {
	var out []string
	after := ""
	for range maxPages {
		q := url.Values{"fields": {"id"}, "limit": {"200"}}
		if after != "" {
			q.Set("after", after)
		}
		var resp metaPage[struct {
			ID string `json:"id"`
		}]
		if err := getJSON(ctx, metaID, metaGraph+"/me/adaccounts?"+q.Encode(), metaAuth(secret), &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.Data {
			if a.ID != "" {
				out = append(out, a.ID)
			}
		}
		if resp.Paging.Next == "" {
			return out, nil
		}
		after = resp.Paging.Cursors.After
	}
	return nil, fmt.Errorf("meta: ad account list did not end after %d pages", maxPages)
}

// metaInsights reads one ad account's campaign-days over w.
func metaInsights(ctx context.Context, secret, account string, w Window) ([]Metric, error) {
	out := make([]Metric, 0, 64)
	after := ""
	for range maxPages {
		// time_range is Meta's own JSON-in-a-query-parameter. Both dates render
		// from the window's time.Time, so nothing a caller typed reaches it.
		q := url.Values{
			"level":          {"campaign"},
			"fields":         {metaInsightFields},
			"time_increment": {"1"},
			"time_range":     {`{"since":"` + day(w.Start) + `","until":"` + day(w.End) + `"}`},
			"limit":          {"500"},
		}
		if after != "" {
			q.Set("after", after)
		}
		var resp metaPage[metaInsight]
		if err := getJSON(ctx, metaID, metaGraph+"/"+account+"/insights?"+q.Encode(), metaAuth(secret), &resp); err != nil {
			return nil, err
		}
		for _, in := range resp.Data {
			d, err := time.Parse("2006-01-02", in.DateStart)
			if err != nil {
				continue // a row Meta did not date cannot be placed on a day
			}
			out = append(out, Metric{
				Day:         d,
				Campaign:    in.CampaignID,
				Name:        in.CampaignName,
				Currency:    in.Currency,
				Spend:       num(in.Spend),
				Impressions: int64(num(in.Impressions)),
				Clicks:      int64(num(in.Clicks)),
				Conversions: metaPick(in.Actions),
				Revenue:     metaPick(in.Values),
			})
		}
		if resp.Paging.Next == "" {
			return out, nil
		}
		after = resp.Paging.Cursors.After
	}
	return nil, fmt.Errorf("meta: insights did not end after %d pages", maxPages)
}

// metaAuth carries the token in the header, for the reason at the top of the file.
func metaAuth(secret string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + strings.TrimSpace(secret)}
}
