package destinations

import (
	"context"
	"fmt"
)

// x.go scaffolds X (Twitter) conversions against the shared interface. Config: the
// web-event tag pixelId. The X Ads conversions endpoint authenticates with OAuth
// 1.0a request signing (a consumer key/secret + access token/secret), NOT a bearer
// token — a signer that is deliberately NOT wired here. The translator + payload
// builder are complete, so enabling X is adding the OAuth1 signer to Send, never a
// new interface. Until then Send returns an honest, credential-free error and the
// fan-out skips it.

const xID = "x"

type xDest struct{}

func init() { register(xDest{}) }

func (xDest) ID() string       { return xID }
func (xDest) Name() string     { return "X (Twitter)" }
func (xDest) Category() string { return categoryAdvertising }

func (xDest) Spec() Spec {
	return Spec{
		Fields: []Field{
			{Key: "pixelId", Label: "Pixel / Event Tag ID", Required: true, Example: "o1abc"},
		},
		Secrets: []string{"access_token"},
	}
}

type xConversion struct {
	ConversionTime string   `json:"conversion_time"`
	EventID        string   `json:"event_id,omitempty"`
	Identifiers    []xIDent `json:"identifiers"`
	NumberItems    int      `json:"number_items,omitempty"`
	Value          string   `json:"value,omitempty"`
	PriceCurrency  string   `json:"price_currency,omitempty"`
}

type xIDent struct {
	HashedEmail string `json:"hashed_email,omitempty"`
	TwClickID   string `json:"twclid,omitempty"`
}

// xBuild renders the batch into X's conversions payload. Pure — tests assert the
// hashed identifiers + event-name mapping — so the scaffold carries a real, reviewed
// payload the day the OAuth1 signer lands.
func xBuild(batch []Conversion) []xConversion {
	out := make([]xConversion, 0, len(batch))
	for _, cv := range batch {
		ids := make([]xIDent, 0, 2)
		if h := hashEmail(cv.User.Email); h != "" {
			ids = append(ids, xIDent{HashedEmail: h})
		}
		if tw := cv.User.click("twclid"); tw != "" {
			ids = append(ids, xIDent{TwClickID: tw})
		}
		c := xConversion{ConversionTime: redditTime(cv.Time), EventID: cv.EventID, Identifiers: ids}
		if cv.Value > 0 {
			c.Value = fmt.Sprintf("%.2f", cv.Value)
			c.PriceCurrency = cv.Currency
		}
		out = append(out, c)
	}
	return out
}

func (d xDest) Send(_ context.Context, cfg Config, _ string, _ []Conversion) (Result, error) {
	if cfg.get("pixelId") == "" {
		return Result{}, fmt.Errorf("x: pixelId is required")
	}
	// The X Ads conversions API requires OAuth 1.0a request signing, which is not
	// wired. Honest, credential-free: the fan-out logs and skips.
	return Result{}, fmt.Errorf("x: the X Ads conversions API requires OAuth 1.0a app credentials that are not yet enabled")
}
