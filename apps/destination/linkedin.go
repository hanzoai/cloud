package destination

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/event"
)

// linkedin.go forwards conversions to the LinkedIn Conversions API (server-side).
// LinkedIn keys each conversion on a pre-created conversion rule URN, so config is one
// rule id per KIND of moment (sign_up, purchase, lead, begin_checkout, add_to_cart,
// view_content, checkout_abandoned: the `linkedin` column of the @hanzo/events platform
// table); a kind with no rule is not sent.
// Secret: access_token — its own token, or the org's linkedin_ads OAuth token via the
// integrations fallback. The token rides Authorization: Bearer; email is SHA-256
// hashed into SHA256_EMAIL, and li_fat_id maps to LINKEDIN_FIRST_PARTY_ADS_TRACKING_UUID,
// with shared eventId so browser Insight Tag and CAPI deduplicate.

const linkedinID = "linkedin"

var (
	linkedinEventsURL = "https://api.linkedin.com/rest/conversionEvents"
	linkedinVersion   = "202611"
)

// hanzoLinkedInDefaults holds Hanzo AI's non-secret LinkedIn Conversions API rule IDs
// on sponsoredAccount 557450290 (Insight Tag 10406532); legacy Boost account 560774110
// (10420116) is disabled. Populate as conversion rules are created under 557450290.
var hanzoLinkedInDefaults = Config{}

type linkedinDest struct{}

func init() { register(linkedinDest{}) }

func (linkedinDest) ID() string       { return linkedinID }
func (linkedinDest) Name() string     { return "LinkedIn" }
func (linkedinDest) Category() string { return categoryAdvertising }

func (linkedinDest) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "signUpConversionId", Label: "Sign-up conversion rule ID", Example: "30631244"},
			{Key: "purchaseConversionId", Label: "Purchase conversion rule ID", Example: "30631252"},
			{Key: "leadConversionId", Label: "Lead conversion rule ID", Example: "30631260"},
			{Key: "checkoutConversionId", Label: "Begin-checkout conversion rule ID", Example: "30631268"},
			{Key: "addToCartConversionId", Label: "Add-to-cart / plan-selected conversion rule ID", Example: "30631276"},
			{Key: "viewContentConversionId", Label: "Pricing & key-page-view conversion rule ID", Example: "30631284"},
			{Key: "abandonedCheckoutConversionId", Label: "Checkout-abandoned conversion rule ID", Example: "30631292"},
		},
		Secrets:  []string{"access_token"},
		Fallback: "linkedin_ads",
	}
}

type linkedinEvent struct {
	Conversion         string          `json:"conversion"`
	ConversionHappened int64           `json:"conversionHappenedAt"`
	ConversionValue    *linkedinValue  `json:"conversionValue,omitempty"`
	EventID            string          `json:"eventId,omitempty"`
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
// request). Pure — tests assert the URN, eventId, SHA256_EMAIL, and li_fat_id match keys.
// Returns ok=false when no rule applies or neither SHA256_EMAIL nor li_fat_id is present.
func linkedinBuild(cfg Config, cv Conversion) (linkedinEvent, bool) {
	rule := cfg.get(ruleKey(cv, linkedinID))
	if rule == "" {
		return linkedinEvent{}, false
	}
	var ids []linkedinUserID
	if email := cv.User.em(); email != "" {
		ids = append(ids, linkedinUserID{IDType: "SHA256_EMAIL", IDValue: email})
	}
	if lifat := cv.User.click("li_fat_id"); lifat != "" {
		ids = append(ids, linkedinUserID{IDType: "LINKEDIN_FIRST_PARTY_ADS_TRACKING_UUID", IDValue: lifat})
	}
	if len(ids) == 0 {
		return linkedinEvent{}, false
	}
	e := linkedinEvent{
		Conversion:         "urn:lla:llaPartnerConversion:" + rule,
		ConversionHappened: eventTimeMS(cv.Time),
		EventID:            strings.TrimSpace(cv.EventID),
		User:               linkedinUserObj{UserIDs: ids},
	}
	if cv.Value > 0 {
		e.ConversionValue = &linkedinValue{CurrencyCode: cv.Currency, Amount: strconv.FormatFloat(cv.Value, 'f', 2, 64)}
	}
	return e, true
}

func (d linkedinDest) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
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
			continue // no LinkedIn match key (email or li_fat_id) or rule
		}
		if err := postJSON(ctx, linkedinID, linkedinEventsURL, headers, e, nil); err != nil {
			return Result{Sent: sent}, err
		}
		sent++
	}
	return Result{Sent: sent}, nil
}

// ruleKinds is the config key of the conversion rule for each kind of moment, on
// LinkedIn and on X, which both name conversions by rule.
var ruleKinds = map[string]string{
	"sign_up":            "signUpConversionId",
	"purchase":           "purchaseConversionId",
	"lead":               "leadConversionId",
	"begin_checkout":     "checkoutConversionId",
	"add_to_cart":        "addToCartConversionId",
	"view_content":       "viewContentConversionId",
	"checkout_abandoned": "abandonedCheckoutConversionId",
}

// ruleKey is the config key holding the rule for a conversion on a platform: the
// kind the platform table gives the event there, else the kind of its normalized
// standard event.
func ruleKey(cv Conversion, platform string) string {
	if kinds := event.Platform(cv.Name, platform); len(kinds) > 0 {
		return ruleKinds[kinds[0]]
	}
	switch cv.Standard {
	case EventSignUp:
		return ruleKinds["sign_up"]
	case EventPurchase:
		return ruleKinds["purchase"]
	case EventLead:
		return ruleKinds["lead"]
	case EventStartCheckout:
		return ruleKinds["begin_checkout"]
	case EventAddToCart:
		return ruleKinds["add_to_cart"]
	case EventViewContent, EventViewList:
		return ruleKinds["view_content"]
	}
	return ""
}
