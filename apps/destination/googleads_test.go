package destination

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGoogleAdsBuild(t *testing.T) {
	cfg := Config{"customerId": "123", "conversionActionId": "456"}
	body := googleadsBuild(cfg, []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 49.5, Currency: "USD", EventID: "ord-1",
			Time: time.Unix(1700000000, 0),
			User: UserData{Email: "  Bob@Example.COM ", Phone: "+1 (555) 000-1111", Clicks: map[string]string{"gclid": "G1"}}},
		{Standard: EventPageView, Name: "$pageview", User: UserData{Clicks: map[string]string{"gclid": "G2"}}}, // not a conversion → dropped
		{Standard: EventLead, Name: "plan_clicked", User: UserData{}},                                          // conversion but NO match key → dropped
		{Standard: EventSignUp, Name: "signup_completed", User: UserData{Email: "c@d.com"}},                    // enhanced-only match key
	})
	if !body.PartialFailure {
		t.Error("partialFailure must be true")
	}
	if len(body.Conversions) != 2 {
		t.Fatalf("want 2 conversions (pageview + keyless lead dropped), got %d", len(body.Conversions))
	}
	c0 := body.Conversions[0]
	if c0.ConversionAction != "customers/123/conversionActions/456" {
		t.Errorf("conversionAction = %q", c0.ConversionAction)
	}
	if c0.Gclid != "G1" || c0.OrderID != "ord-1" || c0.ConversionValue != 49.5 || c0.CurrencyCode != "USD" {
		t.Errorf("conversion: %+v", c0)
	}
	// Google's required "yyyy-mm-dd hh:mm:ss+00:00" UTC format.
	if c0.ConversionDateTime != "2023-11-14 22:13:20+00:00" {
		t.Errorf("conversionDateTime = %q", c0.ConversionDateTime)
	}
	// Email + phone → two hashed enhanced-conversion identifiers.
	if len(c0.UserIdentifiers) != 2 ||
		c0.UserIdentifiers[0].HashedEmail != sha("bob@example.com") ||
		c0.UserIdentifiers[1].HashedPhoneNumber != sha("15550001111") {
		t.Errorf("userIdentifiers: %+v", c0.UserIdentifiers)
	}
	// The enhanced-only signup carries no gclid but a hashed email.
	c1 := body.Conversions[1]
	if c1.Gclid != "" || len(c1.UserIdentifiers) != 1 || c1.UserIdentifiers[0].HashedEmail != sha("c@d.com") {
		t.Errorf("enhanced-only conversion: %+v", c1)
	}
	// No raw PII in the marshalled payload.
	raw, _ := json.Marshal(body)
	if strings.Contains(strings.ToLower(string(raw)), "bob@example.com") {
		t.Fatal("raw email leaked into the Google Ads payload")
	}
}

func TestGoogleAdsSendEndToEnd(t *testing.T) {
	// Token endpoint (form-encoded) → returns a short-lived access token.
	var gotGrant string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		_, _ = w.Write([]byte(`{"access_token":"ya29.tok","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	// Upload endpoint → captures headers + body.
	var gotAuth, gotDevTok, gotLogin, gotPath string
	var gotBody googleUploadBody
	uploadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotDevTok = r.Header.Get("developer-token")
		gotLogin = r.Header.Get("login-customer-id")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"results":[{"gclid":"G1"}]}`))
	}))
	defer uploadSrv.Close()

	ot, ou := googleOAuthURL, googleAdsAPI
	googleOAuthURL, googleAdsAPI = tokenSrv.URL, uploadSrv.URL
	defer func() { googleOAuthURL, googleAdsAPI = ot, ou }()

	creds := `{"developer_token":"DEV","client_id":"CID","client_secret":"CSEC","refresh_token":"RT"}`
	res, err := googleads{}.Send(context.Background(),
		Config{"customerId": "123", "conversionActionId": "456", "loginCustomerId": "999"}, creds,
		[]Conversion{{Standard: EventPurchase, Name: "order_completed", Value: 10, Currency: "USD", EventID: "e1",
			User: UserData{Clicks: map[string]string{"gclid": "G1"}}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent = %d", res.Sent)
	}
	if gotGrant != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", gotGrant)
	}
	if gotAuth != "Bearer ya29.tok" || gotDevTok != "DEV" || gotLogin != "999" {
		t.Errorf("upload headers: auth=%q dev-token=%q login=%q", gotAuth, gotDevTok, gotLogin)
	}
	if gotPath != "/customers/123:uploadClickConversions" {
		t.Errorf("path = %q", gotPath)
	}
	if len(gotBody.Conversions) != 1 || gotBody.Conversions[0].Gclid != "G1" || !gotBody.PartialFailure {
		t.Errorf("body: %+v", gotBody)
	}
}

func TestGoogleAdsRequiresConfig(t *testing.T) {
	if _, err := (googleads{}).Send(context.Background(), Config{}, "{}", nil); err == nil {
		t.Fatal("missing customerId must error")
	}
	if _, err := (googleads{}).Send(context.Background(), Config{"customerId": "1"}, "{}", nil); err == nil {
		t.Fatal("missing conversionActionId must error")
	}
	if _, err := (googleads{}).Send(context.Background(), Config{"customerId": "1", "conversionActionId": "2"}, "not-json", nil); err == nil {
		t.Fatal("non-JSON credentials must error")
	}
	if _, err := (googleads{}).Send(context.Background(), Config{"customerId": "1", "conversionActionId": "2"}, `{"developer_token":"d"}`, nil); err == nil {
		t.Fatal("incomplete credentials must error")
	}
}
