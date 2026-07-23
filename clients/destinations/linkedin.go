package destinations

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// linkedin.go forwards conversions to the LinkedIn Conversions API (server-side).
// LinkedIn keys each conversion on a pre-created conversion rule URN, so config is a
// conversionId (the numeric rule id); optionally a per-event map is a follow-up.
// Secret: access_token — its own token, or the org's linkedin_ads OAuth token via the
// integrations fallback. The token rides Authorization: Bearer; email is SHA-256
// hashed into a SHA256_EMAIL userId. Same interface as GA4/Meta.

const linkedinID = "linkedin"

var (
	linkedinEventsURL = "https://api.linkedin.com/rest/conversionEvents"
	linkedinVersion   = "202401"
)

type linkedinDest struct{}

func init() { register(linkedinDest{}) }

func (linkedinDest) ID() string       { return linkedinID }
func (linkedinDest) Name() string     { return "LinkedIn" }
func (linkedinDest) Category() string { return categoryAdvertising }

func (linkedinDest) Spec() Spec {
	return Spec{
		Fields: []Field{
			{Key: "conversionId", Label: "Conversion Rule ID", Required: true, Example: "1234567"},
		},
		Secrets:  []string{"access_token"},
		Fallback: "linkedin_ads",
	}
}

type linkedinEvent struct {
	Conversion         string          `json:"conversion"`
	ConversionHappened int64           `json:"conversionHappenedAt"`
	ConversionValue    *linkedinValue  `json:"conversionValue,omitempty"`
	User               linkedinUserObj `json:"user"`
}

type linkedinValue struct {
	CurrencyCode string `json:"currencyCode"`
	Amount       string `json:"amount"`
}

type linkedinUserObj struct {
	UserIDs []linkedinUserID `json:"userIds"`
}

type linkedinUserID struct {
	IDType  string `json:"idType"`
	IDValue string `json:"idValue"`
}

// linkedinBuild renders ONE conversion event (LinkedIn's API takes a single event per
// request). Pure — tests assert the URN + hashed SHA256_EMAIL. Returns ok=false when
// the conversion carries no email (LinkedIn's minimum match key here).
func linkedinBuild(cfg Config, cv Conversion) (linkedinEvent, bool) {
	email := hashEmail(cv.User.Email)
	if email == "" {
		return linkedinEvent{}, false
	}
	e := linkedinEvent{
		Conversion:         "urn:lla:llaPartnerConversion:" + cfg.get("conversionId"),
		ConversionHappened: eventTimeMS(cv.Time),
		User:               linkedinUserObj{UserIDs: []linkedinUserID{{IDType: "SHA256_EMAIL", IDValue: email}}},
	}
	if cv.Value > 0 {
		e.ConversionValue = &linkedinValue{CurrencyCode: cv.Currency, Amount: strconv.FormatFloat(cv.Value, 'f', 2, 64)}
	}
	return e, true
}

func (d linkedinDest) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	if cfg.get("conversionId") == "" {
		return Result{}, fmt.Errorf("linkedin: conversionId is required")
	}
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("linkedin: access_token is required")
	}
	headers := map[string]string{
		"Authorization":             "Bearer " + secret,
		"LinkedIn-Version":          linkedinVersion,
		"X-Restli-Protocol-Version": "2.0.0",
	}
	sent := 0
	for _, cv := range batch {
		e, ok := linkedinBuild(cfg, cv)
		if !ok {
			continue // no email match key — LinkedIn CAPI needs one
		}
		if err := postJSON(ctx, linkedinID, linkedinEventsURL, headers, e, nil); err != nil {
			return Result{Sent: sent}, err
		}
		sent++
	}
	return Result{Sent: sent}, nil
}
