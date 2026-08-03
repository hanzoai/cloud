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

	if !strings.Contains(where, "org = ?") || !strings.Contains(where, "attributes['utm_campaign'] = ?") {
		t.Fatalf("org + campaign must be bound placeholders (campaign via the attributes map), got %q", where)
	}
	if strings.Contains(where, "utm_content") {
		t.Fatalf("no variant clause expected for whole-campaign read, got %q", where)
	}
	// args order: org, signal, start, end, campaign — eventsWhere's leading pair first.
	if len(args) != 5 || args[0] != "acme" || args[4] != "cmp_1" {
		t.Fatalf("args must bind [org, signal, ts, ts, campaign], got %v", args)
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
	if !strings.Contains(where, "attributes['utm_content'] = ?") {
		t.Fatalf("variant must add a bound attributes['utm_content'] clause, got %q", where)
	}
	if len(args) != 6 || args[5] != "hero-b" {
		t.Fatalf("variant must be the trailing bound arg, got %v", args)
	}
}

// TestCampaignWhere_ScopesTheSignal is the regression gate for the read that
// counted the whole plane. On ONE fact table the signal is a PREDICATE, and a
// predicate can be forgotten: campaignWhere named org + time + utm_campaign and
// no signal, so uniqExact(distinct_id) and sum(revenue) ranged over logs, spans
// and errors as well as acts. It was latent only because nothing carries a
// utm_campaign yet. campaignWhere composes eventsWhere so it cannot be forgotten
// again — this pins the composition, not merely the string.
func TestCampaignWhere_ScopesTheSignal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	base, baseArgs := eventsWhere("acme", start, end)
	for _, variant := range []string{"", "hero-b"} {
		where, args := campaignWhere("acme", "cmp_1", variant, start, end)
		if !strings.HasPrefix(where, base) {
			t.Fatalf("campaign read must NARROW eventsWhere, not restate it:\n got %q\nwant prefix %q", where, base)
		}
		if !strings.Contains(where, "signal = ?") {
			t.Fatalf("campaign read must bind the signal, got %q", where)
		}
		if got, ok := args[1].(string); !ok || got != string(signalAct) {
			t.Fatalf("campaign read must scope to acts, got %v", args[1])
		}
		for i, want := range baseArgs {
			if args[i] != want {
				t.Fatalf("leading args must be eventsWhere's, got %v want prefix %v", args, baseArgs)
			}
		}
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
	if ev.Source != factTable {
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
