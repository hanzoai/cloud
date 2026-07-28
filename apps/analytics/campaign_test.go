package analytics

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCampaignWhere_BindsOrgAndCampaignPositionally is the tenancy-invariant test
// for the campaign-metrics seam: the org (tenant_id) and campaign (utm_campaign)
// are ALWAYS bound parameters, never interpolated, so a caller can only read its
// own org's campaign and a hostile campaign id can never escape into SQL.
func TestCampaignWhere_BindsOrgAndCampaignPositionally(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	where, args := campaignWhere("acme", "cmp_1", "", start, end)

	if !strings.Contains(where, "tenant_id = ?") || !strings.Contains(where, "utm_campaign = ?") {
		t.Fatalf("org + campaign must be bound placeholders, got %q", where)
	}
	if strings.Contains(where, "utm_content") {
		t.Fatalf("no variant clause expected for whole-campaign read, got %q", where)
	}
	// args order: start, end, org, campaign.
	if len(args) != 4 || args[2] != "acme" || args[3] != "cmp_1" {
		t.Fatalf("args must bind [ts, ts, org, campaign], got %v", args)
	}
	// The hostile-slug proof: the org value is a bound arg, never text in the SQL.
	if strings.Contains(where, "acme") || strings.Contains(where, "cmp_1") {
		t.Fatalf("org/campaign must NOT be interpolated into SQL: %q", where)
	}
}

// TestCampaignWhere_VariantAppended: a non-empty variant adds a bound utm_content
// clause (the creative-A/B evidence read) — still fully parameterized.
func TestCampaignWhere_VariantAppended(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	end := time.Unix(1000, 0).UTC()
	where, args := campaignWhere("acme", "cmp_1", "hero-b", start, end)
	if !strings.Contains(where, "utm_content = ?") {
		t.Fatalf("variant must add a bound utm_content clause, got %q", where)
	}
	if len(args) != 5 || args[4] != "hero-b" {
		t.Fatalf("variant must be the trailing bound arg, got %v", args)
	}
}

// TestCampaignMetrics_HonestEmptyWhenDatastoreDisabled: with no warehouse
// connected (unit-test default), the seam returns honest-empty (Available=false)
// and NO error — the campaign metrics view still renders spend + channels.
func TestCampaignMetrics_HonestEmptyWhenDatastoreDisabled(t *testing.T) {
	ev, err := CampaignMetrics(context.Background(), "acme", "cmp_1", "", time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("datastore-disabled must be honest-empty, not an error: %v", err)
	}
	if ev.Available {
		t.Fatalf("no warehouse connected ⇒ Available must be false, got %+v", ev)
	}
	if ev.Source != eventsTable {
		t.Fatalf("source should name the events table even when empty, got %q", ev.Source)
	}
}

// TestCampaignMetrics_EmptyIdentifiersFailClosed: an empty org or campaign never
// queries — honest-empty, never a warehouse-wide read.
func TestCampaignMetrics_EmptyIdentifiersFailClosed(t *testing.T) {
	for _, tc := range []struct{ org, camp string }{{"", "cmp_1"}, {"acme", ""}} {
		ev, err := CampaignMetrics(context.Background(), tc.org, tc.camp, "", time.Now().Add(-time.Hour), time.Now())
		if err != nil || ev.Available {
			t.Fatalf("empty (%q,%q) must be honest-empty, got avail=%v err=%v", tc.org, tc.camp, ev.Available, err)
		}
	}
}
