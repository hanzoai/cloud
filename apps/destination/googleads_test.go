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
	cfg := Config{"customerId": "123", "purchaseAction": "456", "signupAction": "457", "leadAction": "458"}
	all := Consent{Stated: true, Analytics: true, Marketing: true, Ads: true}
	body := googleadsBuild(cfg, []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 49.5, Currency: "USD", EventID: "ord-1",
			Time: time.Unix(1700000000, 0), Consent: all,
			User: UserData{Email: "  Bob@Example.COM ", Phone: "+1 (555) 000-1111", Clicks: map[string]string{"gclid": "G1", "wbraid": "W1"}}},
		{Standard: EventPageView, Name: "$pageview", User: UserData{Clicks: map[string]string{"gclid": "G2"}}}, // not a conversion → dropped
		{Standard: EventLead, Name: "generate_lead", User: UserData{}},                                         // conversion but NO match key → dropped
		{Standard: EventSignUp, Name: "signup_completed", Consent: Consent{Stated: true, Marketing: true},
			User: UserData{Email: "c@d.com"}}, // enhanced-only match key
		{Standard: EventStartCheckout, Name: "checkout_started", User: UserData{Clicks: map[string]string{"gclid": "G3"}}}, // no checkoutAction → dropped
	})
	if !body.PartialFailure || body.ValidateOnly {
		t.Errorf("partialFailure must be true and validateOnly false: %+v", body)
	}
	if len(body.Conversions) != 2 {
		t.Fatalf("want 2 conversions (pageview, keyless lead and unconfigured checkout dropped), got %d", len(body.Conversions))
	}
	c0 := body.Conversions[0]
	if c0.ConversionAction != "customers/123/conversionActions/456" {
		t.Errorf("conversionAction = %q", c0.ConversionAction)
	}
	// Google refuses a conversion naming two click ids: the gclid wins.
	if c0.Gclid != "G1" || c0.Wbraid != "" {
		t.Errorf("click ids: gclid %q wbraid %q, want exactly the gclid", c0.Gclid, c0.Wbraid)
	}
	if c0.Consent == nil || c0.Consent.AdUserData != "GRANTED" {
		t.Errorf("consent: %+v", c0.Consent)
	}
	if raw, _ := json.Marshal(c0); strings.Contains(string(raw), "adPersonalization") {
		t.Errorf("Google refuses adPersonalization on a click conversion: %s", raw)
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
	// The enhanced-only signup carries no gclid but a hashed email, against its own
	// action.
	c1 := body.Conversions[1]
	if c1.Gclid != "" || len(c1.UserIdentifiers) != 1 || c1.UserIdentifiers[0].HashedEmail != sha("c@d.com") {
		t.Errorf("enhanced-only conversion: %+v", c1)
	}
	if c1.ConversionAction != "customers/123/conversionActions/457" {
		t.Errorf("signup action: %q", c1.ConversionAction)
	}
	// An unstated choice is not a grant Google is told of.
	if c := googleadsBuild(cfg, []Conversion{{Standard: EventPurchase, Consent: Consent{Marketing: true}, User: UserData{Clicks: map[string]string{"gclid": "G"}}}}).Conversions[0]; c.Consent != nil {
		t.Errorf("consent sent unstated: %+v", c.Consent)
	}
	// A gbraid is sent when it is the only click the person has.
	only := googleadsBuild(cfg, []Conversion{{Standard: EventPurchase, User: UserData{Clicks: map[string]string{"gbraid": "B1", "wbraid": "W1"}}}})
	if g := only.Conversions[0]; g.Gbraid != "B1" || g.Wbraid != "" || g.Gclid != "" {
		t.Errorf("gbraid-only: %+v", g)
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
		Config{"customerId": "123", "purchaseAction": "456", "loginCustomerId": "999"}, creds,
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
	if _, err := (googleads{}).Send(context.Background(), Config{"customerId": "1", "purchaseAction": "2"}, "not-json", nil); err == nil {
		t.Fatal("non-JSON credentials must error")
	}
	if _, err := (googleads{}).Send(context.Background(), Config{"customerId": "1", "purchaseAction": "2"}, `{"developer_token":"d"}`, nil); err == nil {
		t.Fatal("incomplete credentials must error")
	}
}
