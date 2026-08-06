package destinations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// googleads.go forwards conversions to Google Ads via the Ads API offline-conversion
// import (uploadClickConversions) — this is GOOGLE ADS proper, not GA4. GA4 (ga4.go) is
// web analytics over the Measurement Protocol; this uploads real conversions against a
// configured conversion action, attributed by gclid and/or enhanced-conversion hashed
// identifiers, so Google Ads bidding optimizes on them.
//
// Config (non-secret): customerId (the Ads account), conversionActionId (the conversion
// action to import against), and an optional loginCustomerId (a manager account).
// Secret: a composite JSON blob {developer_token, client_id, client_secret,
// refresh_token} — the fan-out resolves ONE primary secret per destination, and Google
// Ads needs four OAuth2 app/refresh values, so they ride together. Send exchanges the
// refresh token for a short-lived access token, then uploads.
//
// Only CONVERSION-class events with a Google match key (a gclid, or a hashed email/phone
// for enhanced conversions) are uploaded; a pageview or a keyless event is skipped —
// offline conversion import is for conversions, not page traffic.

const googleadsID = "google-ads"

// googleAdsAPI / googleOAuthURL are the Ads API base and the OAuth2 token endpoint —
// package vars so tests point them at mock servers; never mutated in production.
var (
	googleAdsAPI   = "https://googleads.googleapis.com/v18"
	googleOAuthURL = "https://oauth2.googleapis.com/token"
)

// googleConversionEvents is the conversion-class subset of the taxonomy Google Ads
// accepts as offline conversions. Traffic events (page_view/view_content/search) are not
// conversions and are skipped.
var googleConversionEvents = map[StandardEvent]bool{
	EventPurchase:      true,
	EventLead:          true,
	EventSignUp:        true,
	EventStartCheckout: true,
	EventAddToCart:     true,
	EventContact:       true,
}

type googleads struct{}

func init() { register(googleads{}) }

func (googleads) ID() string       { return googleadsID }
func (googleads) Name() string     { return "Google Ads" }
func (googleads) Category() string { return categoryAdvertising }

func (googleads) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "customerId", Label: "Customer ID", Required: true, Example: "1234567890"},
			{Key: "conversionActionId", Label: "Conversion Action ID", Required: true, Example: "987654321"},
			{Key: "loginCustomerId", Label: "Login Customer ID (manager)", Required: false, Example: "1112223333"},
		},
		// One composite secret: the OAuth2 app + refresh credentials, as JSON.
		Secrets: []string{"oauth2"},
	}
}

// googleCreds are the OAuth2 developer/app/refresh credentials, parsed from the single
// composite KMS secret the connect flow seals for Google Ads.
type googleCreds struct {
	DeveloperToken string `json:"developer_token"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	RefreshToken   string `json:"refresh_token"`
}

func (c googleCreds) complete() bool {
	return c.DeveloperToken != "" && c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != ""
}

type googleUploadBody struct {
	Conversions    []googleConversion `json:"conversions"`
	PartialFailure bool               `json:"partialFailure"`
}

type googleConversion struct {
	ConversionAction   string                 `json:"conversionAction"`
	ConversionDateTime string                 `json:"conversionDateTime"`
	ConversionValue    float64                `json:"conversionValue,omitempty"`
	CurrencyCode       string                 `json:"currencyCode,omitempty"`
	OrderID            string                 `json:"orderId,omitempty"`
	Gclid              string                 `json:"gclid,omitempty"`
	Gbraid             string                 `json:"gbraid,omitempty"`
	Wbraid             string                 `json:"wbraid,omitempty"`
	UserIdentifiers    []googleUserIdentifier `json:"userIdentifiers,omitempty"`
}

type googleUserIdentifier struct {
	HashedEmail       string `json:"hashedEmail,omitempty"`
	HashedPhoneNumber string `json:"hashedPhoneNumber,omitempty"`
}

// googleadsBuild renders the batch into the uploadClickConversions body. Pure — tests
// assert the conversion-action resource, the filtering (conversion-class + match key),
// hashed identifiers, and the datetime format without a network call. Events that are
// not conversions, or carry no Google match key, are dropped.
func googleadsBuild(cfg Config, batch []Conversion) googleUploadBody {
	action := "customers/" + cfg.get("customerId") + "/conversionActions/" + cfg.get("conversionActionId")
	out := make([]googleConversion, 0, len(batch))
	for _, cv := range batch {
		if !googleConversionEvents[cv.Standard] {
			continue // not a conversion — Google Ads offline import is not page tracking
		}
		gclid := cv.User.click("gclid")
		ids := googleIdentifiers(cv.User)
		if gclid == "" && cv.User.click("gbraid") == "" && cv.User.click("wbraid") == "" && len(ids) == 0 {
			continue // no match key Google can attribute — skip rather than send a blind row
		}
		gc := googleConversion{
			ConversionAction:   action,
			ConversionDateTime: googleTime(cv.Time),
			ConversionValue:    cv.Value,
			OrderID:            cv.EventID,
			Gclid:              gclid,
			Gbraid:             cv.User.click("gbraid"),
			Wbraid:             cv.User.click("wbraid"),
			UserIdentifiers:    ids,
		}
		if cv.Value > 0 {
			gc.CurrencyCode = cv.Currency
		}
		out = append(out, gc)
	}
	return googleUploadBody{Conversions: out, PartialFailure: true}
}

// googleIdentifiers builds the enhanced-conversion user identifiers: hashed email and/or
// phone. Google requires each as a separate UserIdentifier. Empty when neither present.
func googleIdentifiers(u UserData) []googleUserIdentifier {
	var ids []googleUserIdentifier
	if h := hashEmail(u.Email); h != "" {
		ids = append(ids, googleUserIdentifier{HashedEmail: h})
	}
	if h := hashPhone(u.Phone); h != "" {
		ids = append(ids, googleUserIdentifier{HashedPhoneNumber: h})
	}
	return ids
}

// googleTime formats an event time as Google Ads' required "yyyy-mm-dd hh:mm:ss+00:00"
// (a space separator and a colon in the zone offset), in UTC. Zero time clamps to now.
func googleTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format("2006-01-02 15:04:05-07:00")
}

// googleAccessToken exchanges the refresh token for a short-lived access token. The
// OAuth2 token endpoint is FORM-encoded (not JSON), so it does not go through postJSON;
// the error is credential-free (endpoint is a constant, never carries the secret).
func googleAccessToken(ctx context.Context, c googleCreds) (string, error) {
	form := url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"refresh_token": {c.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleOAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("google-ads: build token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := sendHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("google-ads: token request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSendRespBody))
	if err != nil {
		return "", fmt.Errorf("google-ads: read token response")
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("google-ads: token http %d: %s", resp.StatusCode, truncate(raw, 256))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("google-ads: token response had no access_token")
	}
	return tr.AccessToken, nil
}

func (d googleads) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	customer := cfg.get("customerId")
	if customer == "" {
		return Result{}, fmt.Errorf("google-ads: customerId is required")
	}
	if cfg.get("conversionActionId") == "" {
		return Result{}, fmt.Errorf("google-ads: conversionActionId is required")
	}
	var c googleCreds
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &c); err != nil {
		return Result{}, fmt.Errorf("google-ads: credentials must be a JSON object {developer_token, client_id, client_secret, refresh_token}")
	}
	if !c.complete() {
		return Result{}, fmt.Errorf("google-ads: OAuth2 credentials are incomplete")
	}
	body := googleadsBuild(cfg, batch)
	if len(body.Conversions) == 0 {
		return Result{}, nil // nothing conversion-class with a match key — send nothing
	}
	token, err := googleAccessToken(ctx, c)
	if err != nil {
		return Result{}, err
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + token,
		"developer-token": c.DeveloperToken,
	}
	// The manager account, when the upload runs under an MCC; else the customer itself.
	if login := cfg.get("loginCustomerId"); login != "" {
		headers["login-customer-id"] = login
	}
	endpoint := googleAdsAPI + "/customers/" + customer + ":uploadClickConversions"
	var resp struct {
		Results []struct {
			Gclid string `json:"gclid"`
		} `json:"results"`
	}
	if err := postJSON(ctx, googleadsID, endpoint, headers, body, &resp); err != nil {
		return Result{}, err
	}
	sent := len(resp.Results)
	if sent == 0 {
		sent = len(body.Conversions)
	}
	return Result{Sent: sent}, nil
}
