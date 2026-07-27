// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// campaign.go is the in-process CAMPAIGN-METRICS seam over the ONE analytics
// warehouse: the /v1/campaign plane (clients/campaign) reads a campaign's funnel
// from HERE rather than opening a second store. A campaign's results ARE an
// analytics query scoped to the campaign — the utm_campaign-tagged events in
// hanzo.events — so there is one metrics plane, not a parallel one.
//
// TENANCY: identical to every other query this package builds. campaignWhere
// binds the org (tenant_id) AND the campaign id (utm_campaign) AND the optional
// variant (utm_content) POSITIONALLY — nothing user-derived is ever interpolated,
// so a caller can only ever read its OWN org's campaign, and the utm_campaign
// filter can never escape into SQL. The variant arg powers the creative-A/B
// evidence read (utm_content) the experiment primitive composes.
package analytics

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
)

// CampaignEvents is the per-campaign funnel read from hanzo.events, scoped to
// (org, utm_campaign[, utm_content]). Impressions/clicks/conversions are counts
// of the campaign's tagged events; Available is false (honest-empty) when the
// events warehouse is not connected or the events table is not yet provisioned —
// never fabricated. Spend is deliberately absent: it is the channel connector's
// reported number, joined by the campaign plane, not an analytics value.
type CampaignEvents struct {
	Available   bool    `json:"available"`
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	Conversions int64   `json:"conversions"`
	Revenue     float64 `json:"revenue"`
	Visitors    int64   `json:"visitors"`
	Source      string  `json:"source"`
}

// campaignWhere is the org + campaign (+ optional variant) predicate over
// hanzo.events. org (tenant_id) and campaignID (utm_campaign) are ALWAYS bound;
// variant (utm_content) is appended only when non-empty. Time bounds are bound as
// datastore DateTime literals (the proven cloud_usage transport). Same isolation
// boundary as eventsWhere — the org is a bound parameter, never interpolated.
func campaignWhere(org, campaignID, variant string, start, end time.Time) (string, []any) {
	where := "timestamp >= ? AND timestamp < ? AND tenant_id = ? AND utm_campaign = ?"
	args := []any{tsLiteral(start), tsLiteral(end), org, campaignID}
	if variant != "" {
		where += " AND utm_content = ?"
		args = append(args, variant)
	}
	return where, args
}

// CampaignMetrics reads the (org, campaignID) funnel from the ONE analytics
// warehouse. variant=="" reads the whole campaign (all creatives); a non-empty
// variant reads a single creative's slice (utm_content) — the evidence read for a
// creative A/B. It degrades to honest-empty (Available=false, nil error) when the
// datastore is not connected, so a campaign metrics view still renders its spend +
// channels. A genuine query failure against a connected warehouse returns the
// error (the caller logs it and shows honest-empty) — never a fabricated funnel.
func CampaignMetrics(ctx context.Context, org, campaignID, variant string, start, end time.Time) (CampaignEvents, error) {
	out := CampaignEvents{Available: false, Source: eventsTable}
	if org == "" || campaignID == "" {
		return out, nil
	}
	if !datastore.Ready() {
		return out, nil // honest-empty: no warehouse connected
	}
	where, args := campaignWhere(org, campaignID, variant, start, end)
	// Each countIf predicate is a server-chosen constant expression (never user
	// input); the only user-derived values — org, campaign, variant, time — stay
	// bound parameters via campaignWhere.
	sql := "SELECT " +
		"countIf(event = 'impression' OR event = 'ad_impression') AS impressions, " +
		"countIf(event = 'click' OR event = 'ad_click') AS clicks, " +
		"countIf(event = 'order_completed' OR event = 'signup' OR event = 'conversion') AS conversions, " +
		"toFloat64(sum(revenue)) AS revenue, " +
		"uniqExact(distinct_id) AS visitors " +
		"FROM " + eventsTable + " WHERE " + where
	rows, err := datastore.Query(ctx, sql, args...)
	if err != nil {
		// Connected warehouse rejected/failed the query (or the events table is
		// absent): honest-empty for the caller, with the error surfaced for logs.
		return out, err
	}
	row := firstRow(rows)
	return CampaignEvents{
		Available:   true,
		Impressions: aInt64(row["impressions"]),
		Clicks:      aInt64(row["clicks"]),
		Conversions: aInt64(row["conversions"]),
		Revenue:     aFloat64(row["revenue"]),
		Visitors:    aInt64(row["visitors"]),
		Source:      eventsTable,
	}, nil
}
