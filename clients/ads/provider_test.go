package ads

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubToken overrides the connector-custody seam so the provider path is
// exercised without standing up KMS + integrations. Restored on cleanup.
func stubToken(t *testing.T, fn func(ctx context.Context, org, provider, name string) ([]byte, error)) {
	t.Helper()
	prev := tokenFor
	tokenFor = fn
	t.Cleanup(func() { tokenFor = prev })
}

// stubMeta points the Meta base at an httptest server (restored on cleanup) and
// returns the server so the test can inspect what the provider received.
func stubMeta(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := metaAdsBase
	metaAdsBase = srv.URL
	t.Cleanup(func() { metaAdsBase = prev; srv.Close() })
	return srv
}

// TestLaunchPaid_MetaCreatesViaConnectorToken is the proof that /v1/ads consumes
// the connector plane: LaunchPaid resolves the ORG'S token via the TokenFor seam
// and creates the campaign on Meta with that token on the Authorization header.
func TestLaunchPaid_MetaCreatesViaConnectorToken(t *testing.T) {
	var (
		gotAuth, gotPath, gotBody string
		gotTokenOrg, gotProvider  string
	)
	stubToken(t, func(_ context.Context, org, provider, name string) ([]byte, error) {
		gotTokenOrg, gotProvider = org, provider
		if name != accessTokenSecret {
			t.Fatalf("token secret name want %q, got %q", accessTokenSecret, name)
		}
		return []byte("T0K3N-acme"), nil
	})
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotBody = r.Form.Encode()
		_, _ = w.Write([]byte(`{"id":"120210000000001"}`))
	})

	ref, err := LaunchPaid(context.Background(), "acme", PaidPlan{
		Platform: "meta", Account: "123", Name: "Spring Launch", Objective: "OUTCOME_TRAFFIC",
	})
	if err != nil {
		t.Fatalf("LaunchPaid: %v", err)
	}
	if ref.ExternalID != "120210000000001" || ref.Account != "act_123" || ref.Status != "live" {
		t.Fatalf("ref: %+v", ref)
	}
	if gotTokenOrg != "acme" || gotProvider != "meta_ads" {
		t.Fatalf("TokenFor called with (%q,%q), want (acme, meta_ads)", gotTokenOrg, gotProvider)
	}
	if gotAuth != "Bearer T0K3N-acme" {
		t.Fatalf("Authorization header want the org token, got %q", gotAuth)
	}
	if gotPath != "/act_123/campaigns" {
		t.Fatalf("create path want /act_123/campaigns, got %q", gotPath)
	}
	for _, want := range []string{"name=Spring", "objective=OUTCOME_TRAFFIC", "status=ACTIVE", "special_ad_categories="} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("create body missing %q: %q", want, gotBody)
		}
	}
}

// TestLaunchPaid_ConnectorDisabledNoProviderCall: when the org has not connected
// the ad account (TokenFor fails), LaunchPaid fails closed and NEVER calls the
// provider — no spend, no fabrication.
func TestLaunchPaid_ConnectorDisabledNoProviderCall(t *testing.T) {
	stubToken(t, func(_ context.Context, _, _, _ string) ([]byte, error) {
		return nil, errors.New("integrations: meta_ads not connected for org")
	})
	hit := false
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"id":"should-not-happen"}`))
	})

	_, err := LaunchPaid(context.Background(), "acme", PaidPlan{Platform: "meta", Account: "123", Name: "X"})
	if !errors.Is(err, errNotConnected) {
		t.Fatalf("want errNotConnected, got %v", err)
	}
	if hit {
		t.Fatalf("provider must NOT be called when the connector is disabled")
	}
}

// TestLaunchPaid_TenantIsolationTokenPerOrg: each org's launch carries ITS OWN
// org's token to the provider — org A can never spend on org B's connection.
func TestLaunchPaid_TenantIsolationTokenPerOrg(t *testing.T) {
	stubToken(t, func(_ context.Context, org, _, _ string) ([]byte, error) {
		return []byte("tok-" + org), nil // each org has a distinct token
	})
	var seen []string
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"id":"1"}`))
	})

	if _, err := LaunchPaid(context.Background(), "acme", PaidPlan{Platform: "meta", Account: "1", Name: "A"}); err != nil {
		t.Fatalf("acme launch: %v", err)
	}
	if _, err := LaunchPaid(context.Background(), "maxpower", PaidPlan{Platform: "meta", Account: "2", Name: "B"}); err != nil {
		t.Fatalf("maxpower launch: %v", err)
	}
	if len(seen) != 2 || seen[0] != "Bearer tok-acme" || seen[1] != "Bearer tok-maxpower" {
		t.Fatalf("each launch must carry its own org token, got %v", seen)
	}
}

// TestLaunchPaid_AuthFailureIsNotConnected: a token the provider rejects (401) is
// fail-closed as errNotConnected — the connection is not usable.
func TestLaunchPaid_AuthFailureIsNotConnected(t *testing.T) {
	stubToken(t, func(_ context.Context, _, _, _ string) ([]byte, error) { return []byte("stale"), nil })
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
	})
	_, err := LaunchPaid(context.Background(), "acme", PaidPlan{Platform: "meta", Account: "1", Name: "X"})
	if !errors.Is(err, errNotConnected) {
		t.Fatalf("401 want errNotConnected, got %v", err)
	}
}

// TestLaunchPaid_UnsupportedPlatformAfterToken: a mapped-but-unwired platform
// resolves the connector token (proving consumption) then honestly reports the
// execution gap — never a fabricated launch.
func TestLaunchPaid_UnsupportedPlatformAfterToken(t *testing.T) {
	tokenSought := false
	stubToken(t, func(_ context.Context, _, provider, _ string) ([]byte, error) {
		tokenSought = true
		if provider != "google_ads" {
			t.Fatalf("google platform must resolve google_ads, got %q", provider)
		}
		return []byte("g-token"), nil
	})
	_, err := LaunchPaid(context.Background(), "acme", PaidPlan{Platform: "google", Account: "1", Name: "X"})
	if !errors.Is(err, errUnsupportedPlatform) {
		t.Fatalf("want errUnsupportedPlatform, got %v", err)
	}
	if !tokenSought {
		t.Fatalf("a mapped platform must resolve its connector token before reporting the gap")
	}
}

// TestLaunchPaid_UnmappedPlatformNeverSeeksToken: a platform with no connector is
// rejected BEFORE any KMS/token lookup.
func TestLaunchPaid_UnmappedPlatformNeverSeeksToken(t *testing.T) {
	stubToken(t, func(_ context.Context, _, _, _ string) ([]byte, error) {
		t.Fatalf("token must not be sought for an unmapped platform")
		return nil, nil
	})
	_, err := LaunchPaid(context.Background(), "acme", PaidPlan{Platform: "snapchat", Account: "1", Name: "X"})
	if !errors.Is(err, errUnsupportedPlatform) {
		t.Fatalf("want errUnsupportedPlatform, got %v", err)
	}
}

// TestPaidSpend_MetaInsightsCents: spend is read from Meta insights and converted
// to minor units (cents), through the org's connector token.
func TestPaidSpend_MetaInsightsCents(t *testing.T) {
	stubToken(t, func(_ context.Context, _, _, _ string) ([]byte, error) { return []byte("tok"), nil })
	var gotPath string
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"spend":"12.34"}]}`))
	})
	cents, err := PaidSpend(context.Background(), "acme", PaidRef{Platform: "meta", ExternalID: "120210000000001"})
	if err != nil {
		t.Fatalf("PaidSpend: %v", err)
	}
	if cents != 1234 {
		t.Fatalf("spend want 1234 cents, got %d", cents)
	}
	if gotPath != "/120210000000001/insights" {
		t.Fatalf("insights path want /120210000000001/insights, got %q", gotPath)
	}
}

// TestPausePaid_MetaSetsPaused: pause posts status=PAUSED to the provider through
// the org's connector token.
func TestPausePaid_MetaSetsPaused(t *testing.T) {
	stubToken(t, func(_ context.Context, _, _, _ string) ([]byte, error) { return []byte("tok"), nil })
	var gotStatus string
	stubMeta(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotStatus = r.Form.Get("status")
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if err := PausePaid(context.Background(), "acme", PaidRef{Platform: "meta", ExternalID: "120210000000001"}); err != nil {
		t.Fatalf("PausePaid: %v", err)
	}
	if gotStatus != "PAUSED" {
		t.Fatalf("pause status want PAUSED, got %q", gotStatus)
	}
}
