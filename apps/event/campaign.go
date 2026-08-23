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

// campaign.go is the in-process CAMPAIGN-METRICS client over the ONE analytics
// warehouse: the /v1/campaign plane (apps/campaigns) reads a campaign's funnel
// from HERE rather than opening a second store. A campaign's results ARE an
// analytics query scoped to the campaign — the utm_campaign-tagged acts on the
// event plane (the attributes['utm_campaign'] entry the plane normalizer stamps)
// — so there is one metrics plane, not a parallel one.
//
// TENANCY AND SIGNAL: identical to every other query this package builds, because
// campaignWhere composes eventsWhere. The org, the signal, the time bounds, the
// campaign id and the optional variant are all bound POSITIONALLY — nothing
// user-derived is ever interpolated, so a caller can only ever read its OWN org's
// campaign, and the utm_campaign filter can never escape into SQL. The variant arg
// powers the creative-A/B evidence read the experiment primitive composes.

package event

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

// CampaignEvents is the per-campaign funnel read from event.event, scoped to
// (org, utm_campaign[, utm_content]). Impressions/clicks/conversions are counts
// of the campaign's tagged events; Available is false (honest-empty) when the
// events warehouse is not connected or the plane is not yet provisioned —
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

// campaignWhere is the campaign NARROWING of the behavior lens: eventsWhere,
// plus the campaign (+ optional variant) the caller asked about. It composes
// eventsWhere rather than writing its own FROM-clause predicate, because the
// leading pair — whose rows, and which sort — is one decision and belongs in one
// place: this read named its own `org = ?` and time bounds and had no `signal`
// at all, so on the ONE fact table it counted every log line, span and error as
// a campaign impression the moment a campaign shipped.
//
// campaignID (attributes['utm_campaign']) and variant (attributes['utm_content'])
// are bound like everything else; the map ACCESSOR is a server constant and only
// the VALUE binds. Arg order is eventsWhere's [org, signal, start, end], then
// campaign, then the variant when there is one.
func campaignWhere(org, campaignID, variant string, start, end time.Time) (string, []any) {
	where, args := eventsWhere(org, start, end)
	where += " AND attributes['utm_campaign'] = ?"
	args = append(args, campaignID)
	if variant != "" {
		where += " AND attributes['utm_content'] = ?"
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
	out := CampaignEvents{Available: false, Source: factTable}
	if org == "" || campaignID == "" {
		return out, nil
	}
	if !datastore.Ready() {
		return out, nil // honest-empty: no warehouse connected
	}
	where, args := campaignWhere(org, campaignID, variant, start, end)
	// Each countIf predicate is a server-chosen constant expression (never user
	// input); the only user-derived values — org, campaign, variant, time — stay
	// bound parameters via campaignWhere. Names are the envelope's `name` column;
	// revenue reads back the attributes entry the plane normalizer stamped.
	sql := "SELECT " +
		"countIf(name = 'impression' OR name = 'ad_impression') AS impressions, " +
		"countIf(name = 'click' OR name = 'ad_click') AS clicks, " +
		// signup_completed is the terminal event of the signup funnel (@hanzo/event
		// EVENTS grammar: <object>_<verb-past>); a bare 'signup' was counted here
		// before, an event NOTHING emits — signup conversions always read zero.
		"countIf(name = 'order_completed' OR name = 'signup_completed' OR name = 'conversion') AS conversions, " +
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue, " +
		"uniqExact(distinct_id) AS visitors " +
		"FROM " + factTable + " WHERE " + where
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
		Source:      factTable,
	}, nil
}
