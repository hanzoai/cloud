package ads

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/clients/campaign"
)

// campaign_paid_test.go is the END-TO-END proof of the paid GTM channel: the SAME
// adapter apps/wire_seams.go registers (campaign.Plan → ads.PaidPlan → LaunchPaid)
// driven through the campaign.Channel interface, so the whole chain
// campaign → ads → integrations.TokenFor → provider is exercised against an
// httptest Meta stub. It lives in package ads because only this package can point
// the connector-custody seam (tokenFor) and the Meta base at test doubles;
// importing clients/campaign here is acyclic (campaign never imports ads).

// paidChannelForTest builds the exact paid-channel adapter the composition root
// wires, so the test exercises the real seam shape, not a bespoke one.
func paidChannelForTest() campaign.Channel {
	return campaign.NewChannel(campaign.KindPaid,
		func(ctx context.Context, org string, p campaign.Plan) (campaign.Ref, error) {
			r, err := LaunchPaid(ctx, org, PaidPlan{
				Platform: p.Platform, Account: p.Account, Name: p.Name,
				Objective: p.Objective, BudgetCents: p.BudgetCents, ScheduleAt: p.ScheduleAt,
			})
			return campaign.Ref{Platform: r.Platform, Account: r.Account, ExternalID: r.ExternalID, Status: r.Status, Detail: r.Detail}, err
		},
		func(ctx context.Context, org string, ref campaign.Ref) (int64, error) {
			return PaidSpend(ctx, org, PaidRef{Platform: ref.Platform, Account: ref.Account, ExternalID: ref.ExternalID})
		},
		func(ctx context.Context, org string, ref campaign.Ref) error {
			return PausePaid(ctx, org, PaidRef{Platform: ref.Platform, Account: ref.Account, ExternalID: ref.ExternalID})
		},
	)
}

// TestCampaignPaidChannel_EndToEnd: a campaign's paid channel launches an ad
// campaign on Meta using the ORG'S connected token, then reads spend — the full
// capability→connector→provider chain.
func TestCampaignPaidChannel_EndToEnd(t *testing.T) {
	stubToken(t, func(_ context.Context, org, provider, _ string) ([]byte, error) {
		if provider != "meta_ads" {
			t.Fatalf("paid meta channel must consume meta_ads, got %q", provider)
		}
		return []byte("tok-" + org), nil
	})
	var gotAuth string
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/act_123/campaigns" {
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"id":"120210000000009"}`))
			return
		}
		if r.URL.Path == "/120210000000009/insights" {
			_, _ = w.Write([]byte(`{"data":[{"spend":"7.50"}]}`))
			return
		}
		http.NotFound(w, r)
	})

	paid := paidChannelForTest()
	if paid.Kind() != campaign.KindPaid {
		t.Fatalf("channel kind want paid, got %q", paid.Kind())
	}

	ref, err := paid.Launch(context.Background(), "acme", campaign.Plan{
		CampaignID: "cmp_1", Platform: "meta", Account: "123", Name: "GTM Launch",
	})
	if err != nil {
		t.Fatalf("paid Launch: %v", err)
	}
	if ref.ExternalID != "120210000000009" || ref.Status != "live" {
		t.Fatalf("ref: %+v", ref)
	}
	if gotAuth != "Bearer tok-acme" {
		t.Fatalf("Meta must be called with acme's connector token, got %q", gotAuth)
	}

	// Spend read composes the same token door → provider insights.
	cents, err := paid.Spend(context.Background(), "acme", ref)
	if err != nil {
		t.Fatalf("paid Spend: %v", err)
	}
	if cents != 750 {
		t.Fatalf("spend want 750 cents, got %d", cents)
	}
}

// TestCampaignPaidChannel_ConnectorDisabledBlocksSend: a campaign whose org has
// not connected the ad account cannot launch — the channel fails closed and the
// provider is never called (no spend on an unmade connection).
func TestCampaignPaidChannel_ConnectorDisabledBlocksSend(t *testing.T) {
	stubToken(t, func(_ context.Context, _, _, _ string) ([]byte, error) {
		return nil, errors.New("integrations: meta_ads not connected for org")
	})
	hit := false
	stubMeta(t, func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"id":"nope"}`))
	})

	paid := paidChannelForTest()
	_, err := paid.Launch(context.Background(), "acme", campaign.Plan{
		CampaignID: "cmp_1", Platform: "meta", Account: "123", Name: "GTM",
	})
	if !errors.Is(err, errNotConnected) {
		t.Fatalf("want errNotConnected, got %v", err)
	}
	if hit {
		t.Fatalf("provider must NOT be called when the org's connector is disabled")
	}
}
